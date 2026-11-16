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

// TestMetricUsageCandidateUsesCoveredWholeMinutes 用途：验证真实秒级现场事件可生成符合分钟精度的候选。
// 参数：t 为应用边界测试，事件均为同一人的完整现场链。
// 返回值：无；候选包含未观察到的秒数或无法通过现有精度校验时失败。
func TestMetricUsageCandidateUsesCoveredWholeMinutes(t *testing.T) {
	start := time.Date(2026, 11, 15, 1, 0, 30, 0, time.UTC)
	end := time.Date(2026, 11, 15, 2, 0, 15, 0, time.UTC)
	repo := &candidateEventRepository{capabilityRepository: capabilityRepository{capabilities: []domain.Capability{
		{ResourceType: "PERSON", ResourceID: "P-1", UserID: "person-a", Status: "ACTIVE"},
	}}, events: []domain.DeliveryEvent{
		{ID: "E1", ServiceItemID: "SI-a", ActorUserID: "person-a", Type: EventFieldCheckedIn, CreatedAt: start},
		{ID: "E2", ServiceItemID: "SI-a", ActorUserID: "person-a", Type: EventFieldRecordSubmitted, CreatedAt: end},
	}}
	p := platform.Principal{TenantID: "tenant", UserID: "person-a", Permissions: map[string]bool{"project.read": true},
		DataScopes: []platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}}}
	rows, err := (&Service{Repo: repo}).MetricUsageCandidates(t.Context(), p, "SI-a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("候选生成失败：%+v %v", rows, err)
	}
	row := rows[0]
	if !row.Start.Equal(start.Truncate(time.Minute).Add(time.Minute)) ||
		!row.End.Equal(end.Truncate(time.Minute)) || row.Minutes != 59 || !row.Complete {
		t.Fatalf("候选没有按完整覆盖分钟核算：%+v", row)
	}
	if _, err = domain.ValidateMetricIntervals(row.BusinessDate, []domain.MetricInterval{{Start: row.Start, End: row.End}}); err != nil {
		t.Fatalf("候选不能通过实际用量精度校验：%v", err)
	}
}
