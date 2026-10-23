package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

var (
	ErrProjectSnapshotLimit       = errors.New("project snapshot limit exceeded")
	ErrProjectSnapshotProtocol    = errors.New("invalid project snapshot protocol")
	ErrProjectSnapshotUnavailable = errors.New("project snapshot repository unavailable")
)

type ProjectSnapshotLimits struct {
	BatchSize, MaxProjects, MaxServiceItems, MaxBytes, MaxLineBytes int
	Timeout                                                         time.Duration
}

func DefaultProjectSnapshotLimits() ProjectSnapshotLimits {
	return ProjectSnapshotLimits{BatchSize: 200, MaxProjects: 50000, MaxServiceItems: 250000, MaxBytes: 64 * 1024 * 1024, MaxLineBytes: 64 * 1024, Timeout: 30 * time.Second}
}
func (l ProjectSnapshotLimits) Validate() error {
	d := DefaultProjectSnapshotLimits()
	if l.BatchSize < 1 || l.BatchSize > d.BatchSize || l.MaxProjects < 1 || l.MaxProjects > d.MaxProjects || l.MaxServiceItems < 1 || l.MaxServiceItems > d.MaxServiceItems || l.MaxBytes < 1 || l.MaxBytes > d.MaxBytes || l.MaxLineBytes < 1 || l.MaxLineBytes > d.MaxLineBytes || l.MaxLineBytes > l.MaxBytes || l.Timeout <= 0 || l.Timeout > d.Timeout {
		return fmt.Errorf("%w: invalid project snapshot limits", ErrValidation)
	}
	return nil
}

type ProjectSnapshotSink interface {
	Begin(domain.ProjectSnapshotHeader) error
	Project(domain.ProjectProjection) error
}

// ProjectSnapshotRepository is optional: older repositories fail closed for this export.
type ProjectSnapshotRepository interface {
	StreamProjectSnapshot(context.Context, string, ProjectSnapshotLimits, ProjectSnapshotSink) (domain.ProjectSnapshotSummary, error)
}

func (s *Service) ExportProjectSnapshot(ctx context.Context, p platform.Principal, limits ProjectSnapshotLimits, sink ProjectSnapshotSink) (domain.ProjectSnapshotSummary, error) {
	filter, err := p.ProjectScopeFilter()
	if !p.Has("project.read") || err != nil || !filter.AllowAll || p.TenantID != strings.TrimSpace(p.TenantID) {
		return domain.ProjectSnapshotSummary{}, ErrForbidden
	}
	if limits == (ProjectSnapshotLimits{}) {
		limits = DefaultProjectSnapshotLimits()
	}
	if err := limits.Validate(); err != nil {
		return domain.ProjectSnapshotSummary{}, err
	}
	if sink == nil {
		return domain.ProjectSnapshotSummary{}, ErrValidation
	}
	if s == nil {
		return domain.ProjectSnapshotSummary{}, ErrProjectSnapshotUnavailable
	}
	repo, ok := s.Repo.(ProjectSnapshotRepository)
	if !ok {
		return domain.ProjectSnapshotSummary{}, ErrProjectSnapshotUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return domain.ProjectSnapshotSummary{}, err
	}
	return repo.StreamProjectSnapshot(ctx, p.TenantID, limits, sink)
}

// ProjectSnapshotStream validates and accounts for the exact bytes sent to a sink.
// Summary only describes a complete stream; the caller must commit before publishing it.
type ProjectSnapshotStream struct {
	ctx                 context.Context
	tenant              string
	limits              ProjectSnapshotLimits
	sink                ProjectSnapshotSink
	header              domain.ProjectSnapshotHeader
	started             bool
	total, items, bytes int
	lastID              string
	checksum            hash.Hash
	err                 error
}

func NewProjectSnapshotStream(ctx context.Context, tenant string, limits ProjectSnapshotLimits, sink ProjectSnapshotSink) *ProjectSnapshotStream {
	return &ProjectSnapshotStream{ctx: ctx, tenant: tenant, limits: limits, sink: sink, checksum: sha256.New()}
}
func (s *ProjectSnapshotStream) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}
func (s *ProjectSnapshotStream) check() error {
	if s.err != nil {
		return s.err
	}
	if err := s.ctx.Err(); err != nil {
		return s.fail(err)
	}
	return nil
}
func validSnapshotTime(t time.Time) bool { return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 }
func (s *ProjectSnapshotStream) Begin(h domain.ProjectSnapshotHeader) error {
	if err := s.check(); err != nil {
		return err
	}
	if err := s.limits.Validate(); err != nil {
		return s.fail(err)
	}
	if s.started || s.sink == nil || s.tenant == "" || h.Type != "header" || h.SchemaVersion != 1 || h.TenantID != s.tenant || strings.TrimSpace(h.SnapshotID) == "" || !validSnapshotTime(h.SourceSnapshotAt) {
		return s.fail(ErrProjectSnapshotProtocol)
	}
	h.SourceSnapshotAt = h.SourceSnapshotAt.UTC()
	s.header = h
	line, err := domain.ProjectSnapshotLine(h)
	if err != nil {
		return s.fail(err)
	}
	if err = s.reserve(line, 0); err != nil {
		return s.fail(err)
	}
	if err = s.sink.Begin(h); err != nil {
		return s.fail(err)
	}
	s.started = true
	s.bytes += len(line)
	return s.check()
}
func (s *ProjectSnapshotStream) Project(p domain.ProjectProjection) error {
	if err := s.check(); err != nil {
		return err
	}
	if !s.started || p.Type != "project" || p.TenantID != s.tenant || p.SnapshotID != s.header.SnapshotID || strings.TrimSpace(p.ID) == "" || p.ID <= s.lastID || p.Progress < 0 || p.Progress > 100 || p.ServiceItems < 0 || !validSnapshotTime(p.CreatedAt) || !validSnapshotTime(p.UpdatedAt) || !validSnapshotTime(p.SourceUpdatedAt) {
		return s.fail(ErrProjectSnapshotProtocol)
	}
	if s.total >= s.limits.MaxProjects || p.ServiceItems > s.limits.MaxServiceItems-s.items {
		return s.fail(ErrProjectSnapshotLimit)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	p.SourceUpdatedAt = p.SourceUpdatedAt.UTC()
	line, err := domain.ProjectSnapshotLine(p)
	if err != nil {
		return s.fail(err)
	}
	if err = s.reserve(line, s.total+1); err != nil {
		return s.fail(err)
	}
	if err = s.sink.Project(p); err != nil {
		return s.fail(err)
	}
	_, _ = s.checksum.Write(line)
	s.total++
	s.items += p.ServiceItems
	s.bytes += len(line)
	s.lastID = p.ID
	return s.check()
}
func (s *ProjectSnapshotStream) summary(total int) domain.ProjectSnapshotSummary {
	return domain.ProjectSnapshotSummary{Type: "end", SchemaVersion: 1, TenantID: s.tenant, SnapshotID: s.header.SnapshotID, Total: total, ChecksumSHA256: hex.EncodeToString(s.checksum.Sum(nil))}
}

// Reserve the end record too; a response that fits only without its end is incomplete.
func (s *ProjectSnapshotStream) reserve(line []byte, total int) error {
	end, err := domain.ProjectSnapshotLine(s.summary(total))
	if err != nil {
		return err
	}
	if len(line) > s.limits.MaxLineBytes || len(end) > s.limits.MaxLineBytes || s.bytes+len(line)+len(end) > s.limits.MaxBytes {
		return ErrProjectSnapshotLimit
	}
	return nil
}
func (s *ProjectSnapshotStream) Summary() (domain.ProjectSnapshotSummary, error) {
	if err := s.check(); err != nil {
		return domain.ProjectSnapshotSummary{}, err
	}
	if !s.started {
		return domain.ProjectSnapshotSummary{}, s.fail(ErrProjectSnapshotProtocol)
	}
	return s.summary(s.total), nil
}
