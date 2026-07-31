package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

var ErrNotFound = errors.New("resource not found")

type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot domain.Snapshot
}

func Open(path string) (*Store, error) {
	s := &Store{path: strings.TrimSpace(path)}
	if s.path == "" {
		s.snapshot = Seed()
		return s, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.snapshot = Seed()
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read data file: %w", err)
	}
	if err := json.Unmarshal(data, &s.snapshot); err != nil {
		return nil, fmt.Errorf("decode data file: %w", err)
	}
	return s, nil
}

func (s *Store) ListProjects(query, status string) []domain.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query = strings.ToLower(strings.TrimSpace(query))
	items := make([]domain.Project, 0, len(s.snapshot.Projects))
	for _, p := range s.snapshot.Projects {
		haystack := strings.ToLower(strings.Join([]string{p.ID, p.Name, p.Customer, p.Contract, p.Category, p.Manager}, " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		if status != "" && p.Status != status {
			continue
		}
		items = append(items, p)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items
}

func (s *Store) GetProject(id string) (domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.snapshot.Projects {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *Store) CreateProject(input domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	input.ID = fmt.Sprintf("PJ-%d-%04d", now.Year(), nextProjectSequence(s.snapshot.Projects))
	input.Name = strings.TrimSpace(input.Name)
	input.Customer = strings.TrimSpace(input.Customer)
	input.Contract = strings.TrimSpace(input.Contract)
	if input.Status == "" {
		input.Status = "待拆解确认"
	}
	if input.Health == "" {
		input.Health = "待确认"
	}
	if input.Team == "" {
		input.Team = "未分配"
	}
	if input.Manager == "" {
		input.Manager = "—"
	}
	input.CreatedAt, input.UpdatedAt = now, now
	s.snapshot.Projects = append(s.snapshot.Projects, input)
	if err := s.persistLocked(); err != nil {
		s.snapshot.Projects = s.snapshot.Projects[:len(s.snapshot.Projects)-1]
		return domain.Project{}, err
	}
	return input, nil
}

func nextProjectSequence(projects []domain.Project) int {
	max := 0
	for _, p := range projects {
		var year, seq int
		if _, err := fmt.Sscanf(p.ID, "PJ-%d-%d", &year, &seq); err == nil && seq > max {
			max = seq
		}
	}
	return max + 1
}

func (s *Store) ListServiceItems(projectID string) []domain.ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.ServiceItem, 0, len(s.snapshot.ServiceItems))
	for _, item := range s.snapshot.ServiceItems {
		if projectID == "" || item.ProjectID == projectID {
			items = append(items, item)
		}
	}
	return items
}

func (s *Store) ConfirmServiceItems(ids []string) ([]domain.ServiceItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	changed := make([]domain.ServiceItem, 0, len(ids))
	for i := range s.snapshot.ServiceItems {
		if wanted[s.snapshot.ServiceItems[i].ID] {
			s.snapshot.ServiceItems[i].Status = "待分配"
			changed = append(changed, s.snapshot.ServiceItems[i])
			delete(wanted, s.snapshot.ServiceItems[i].ID)
		}
	}
	if len(wanted) > 0 {
		return nil, ErrNotFound
	}
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	return changed, nil
}

func (s *Store) ListRules(kind string) []domain.Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Rule, 0, len(s.snapshot.Rules))
	for _, rule := range s.snapshot.Rules {
		if kind == "" || rule.Kind == kind {
			items = append(items, rule)
		}
	}
	return items
}

func (s *Store) CreateRule(input domain.Rule) (domain.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rule := range s.snapshot.Rules {
		if rule.ID >= input.ID {
			input.ID = rule.ID + 1
		}
	}
	if input.ID == 0 {
		input.ID = 1
	}
	if input.Kind == "" {
		input.Kind = "split-rules"
	}
	input.Updated = time.Now().Format("2006-01-02 15:04")
	s.snapshot.Rules = append(s.snapshot.Rules, input)
	if err := s.persistLocked(); err != nil {
		s.snapshot.Rules = s.snapshot.Rules[:len(s.snapshot.Rules)-1]
		return domain.Rule{}, err
	}
	return input, nil
}

func (s *Store) SetRuleEnabled(id int64, enabled bool) (domain.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.snapshot.Rules {
		if s.snapshot.Rules[i].ID != id {
			continue
		}
		before := s.snapshot.Rules[i]
		s.snapshot.Rules[i].Enabled = enabled
		s.snapshot.Rules[i].Updated = time.Now().Format("2006-01-02 15:04")
		if err := s.persistLocked(); err != nil {
			s.snapshot.Rules[i] = before
			return domain.Rule{}, err
		}
		return s.snapshot.Rules[i], nil
	}
	return domain.Rule{}, ErrNotFound
}

func (s *Store) Dashboard() domain.Dashboard {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := domain.Dashboard{ProjectCount: len(s.snapshot.Projects), ServiceItems: len(s.snapshot.ServiceItems), StatusCounts: map[string]int{}}
	for _, p := range s.snapshot.Projects {
		result.StatusCounts[p.Status]++
		if p.Health == "风险" {
			result.RiskProjects++
		}
		if p.Status != "已完成" {
			result.InFlightProjects++
		}
	}
	return result
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	data, err := json.MarshalIndent(s.snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".project-management-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
