package application

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type candidateEventRepository struct {
	capabilityRepository
	events []domain.DeliveryEvent
}

func (r *candidateEventRepository) ListDeliveryEvents(context.Context, platform.ScopeFilter, string) ([]domain.DeliveryEvent, error) {
	return r.events, nil
}

// TestMetricUsageCandidatesBuildsCompleteFieldChain 验证候选来自真实事件链，而不是计划或预约。
func TestMetricUsageCandidatesBuildsCompleteFieldChain(t *testing.T) {
	repo := &candidateEventRepository{
		capabilityRepository: capabilityRepository{capabilities: []domain.Capability{{ResourceType: "PERSON", ResourceID: "P-1", UserID: "user-1", Status: "ACTIVE"}}},
		events: []domain.DeliveryEvent{
			{ID: "E1", ServiceItemID: "SI-1", ActorUserID: "user-1", Type: EventFieldStarted, CreatedAt: time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)},
			{ID: "E2", ServiceItemID: "SI-1", ActorUserID: "user-1", Type: EventFieldCheckedIn, CreatedAt: time.Date(2026, 11, 15, 1, 5, 0, 0, time.UTC), CapturedAt: ptrTime(time.Date(2026, 11, 15, 1, 5, 0, 0, time.UTC))},
			{ID: "E3", ServiceItemID: "SI-1", ActorUserID: "user-1", Type: EventFieldRecordSubmitted, CreatedAt: time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC)},
			{ID: "E4", ServiceItemID: "SI-1", ActorUserID: "user-1", Type: EventFieldSignatureCaptured, CreatedAt: time.Date(2026, 11, 15, 2, 10, 0, 0, time.UTC)},
		},
	}
	service := &Service{Repo: repo}
	p := platform.Principal{TenantID: "tenant", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, DataScopes: []platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}}}
	out, err := service.MetricUsageCandidates(context.Background(), p, "SI-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("candidates=%+v", out)
	}
	candidate := out[0]
	if !candidate.Complete || candidate.ResourceID != "P-1" || candidate.Minutes != 70 || candidate.BusinessDate != "2026-11-15" {
		t.Fatalf("candidate=%+v", candidate)
	}
	if len(candidate.SourceEventIDs) != 4 || candidate.Reason != "" {
		t.Fatalf("source=%+v reason=%q", candidate.SourceEventIDs, candidate.Reason)
	}
}

// TestMetricCalendarIntervalsNormalizeAndShift 验证星期规则以锚点日保存、按目标日平移。
func TestMetricCalendarIntervalsNormalizeAndShift(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	input := []domain.MetricInterval{{Start: time.Date(2026, 11, 16, 9, 0, 0, 0, loc).UTC(), End: time.Date(2026, 11, 16, 12, 30, 0, 0, loc).UTC()}}
	normalized, err := metricNormalizeCalendarIntervals(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || metricBusinessDate(normalized[0].Start) != "2026-01-05" {
		t.Fatalf("normalized=%+v", normalized)
	}
	shifted := metricIntervalsOnDate(normalized, "2026-11-20")
	if len(shifted) != 1 || metricBusinessDate(shifted[0].Start) != "2026-11-20" || shifted[0].End.Sub(shifted[0].Start) != 3*time.Hour+30*time.Minute {
		t.Fatalf("shifted=%+v", shifted)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
