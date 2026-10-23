package application

import (
	"context"
	"errors"
	"strings"
	"time"
)

// BackgroundLeaseReader is optional for normal application commands; controlled
// drain requires it and never interprets an unsupported repository as zero leases.
type BackgroundLeaseReader interface {
	CountActiveBackgroundLeases(context.Context, string, time.Time) (int64, error)
}

type BackgroundQueueStats struct {
	Kind        string     `json:"kind"`
	Pending     int64      `json:"pending"`
	RetryWait   int64      `json:"retry_wait"`
	Processing  int64      `json:"processing"`
	Dead        int64      `json:"dead"`
	OldestDueAt *time.Time `json:"oldest_due_at,omitempty"`
}
type BackgroundStatsReader interface {
	ReadBackgroundQueueStats(context.Context, string, time.Time) ([]BackgroundQueueStats, error)
}

func (s *Service) ReadBackgroundQueueStats(ctx context.Context, tenant string, now time.Time) ([]BackgroundQueueStats, error) {
	if !validBackgroundTenant(tenant) {
		return nil, errors.New("invalid background tenant")
	}
	r, ok := s.Repo.(BackgroundStatsReader)
	if !ok {
		return nil, errors.New("background queue stats unavailable")
	}
	stats, e := r.ReadBackgroundQueueStats(ctx, tenant, now)
	if e != nil || len(stats) != 2 {
		return nil, errors.New("background queue stats unavailable")
	}
	return stats, nil
}
func validBackgroundTenant(tenant string) bool {
	return strings.TrimSpace(tenant) != "" && strings.TrimSpace(tenant) == tenant && len(tenant) <= 64
}

func (s *Service) CountActiveBackgroundLeases(ctx context.Context, tenant string, now time.Time) (int64, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(tenant) != tenant || len(tenant) > 64 {
		return -1, errors.New("invalid background tenant")
	}
	reader, ok := s.Repo.(BackgroundLeaseReader)
	if !ok {
		return -1, errors.New("background lease inspection unavailable")
	}
	n, e := reader.CountActiveBackgroundLeases(ctx, tenant, now)
	if e != nil || n < 0 {
		return -1, errors.New("background lease inspection unavailable")
	}
	return n, nil
}
