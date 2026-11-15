package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// batchUsageRepository 在完整 Repository 替身上记录一次原子批量命令。
type batchUsageRepository struct {
	scopeRepository
	called domain.MetricUsageBatchInput
	items  []domain.ResourceUsage
}

func (r *batchUsageRepository) BatchMutateMetricUsages(_ context.Context, _ platform.ScopeFilter, _ string, in domain.MetricUsageBatchInput) ([]domain.ResourceUsage, error) {
	r.called = in
	return r.items, nil
}

func batchPrincipal() platform.Principal {
	return platform.Principal{TenantID: "tenant", UserID: "quality", Permissions: map[string]bool{"project.resource_usage.confirm": true}, DataScopes: []platform.DataScope{{RoleCode: "quality_manager", ScopeType: "APPLICATION"}}}
}

// TestBatchMetricUsagesDelegatesOneAtomicCommand 锁定批量命令只下发一次仓储事务。
func TestBatchMetricUsagesDelegatesOneAtomicCommand(t *testing.T) {
	repo := &batchUsageRepository{items: []domain.ResourceUsage{{ID: "U-1", Status: "CONFIRMED"}}}
	service := &Service{Repo: repo}
	out, err := service.BatchMutateMetricUsages(context.Background(), batchPrincipal(), domain.MetricUsageBatchInput{
		Items:          []domain.MetricUsageBatchItem{{ID: "U-1", ExpectedVersion: 3}},
		Action:         "decision",
		Decision:       "CONFIRMED",
		Note:           "批量确认",
		IdempotencyKey: "batch-command-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].Status != "CONFIRMED" {
		t.Fatalf("items=%+v", out.Items)
	}
	if repo.called.Action != "DECISION" || repo.called.Decision != "CONFIRMED" || repo.called.Items[0].ExpectedVersion != 3 {
		t.Fatalf("called=%+v", repo.called)
	}
}

// TestBatchMetricUsagesRejectsInvalidOrOversizedCommands 验证批量不能绕过版本、权限或上限。
func TestBatchMetricUsagesRejectsInvalidOrOversizedCommands(t *testing.T) {
	service := &Service{Repo: &batchUsageRepository{}}
	base := domain.MetricUsageBatchInput{Items: []domain.MetricUsageBatchItem{{ID: "U-1", ExpectedVersion: 1}}, Action: "DECISION", Decision: "CONFIRMED", Note: "批量确认", IdempotencyKey: "batch-command-2"}
	noPermission := batchPrincipal()
	noPermission.Permissions = map[string]bool{"project.read": true}
	if _, err := service.BatchMutateMetricUsages(context.Background(), noPermission, base); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permission: %v", err)
	}
	invalid := base
	invalid.Items = nil
	if _, err := service.BatchMutateMetricUsages(context.Background(), batchPrincipal(), invalid); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty items: %v", err)
	}
	oversized := base
	oversized.Items = make([]domain.MetricUsageBatchItem, domain.MetricUsageBatchMaxItems+1)
	if _, err := service.BatchMutateMetricUsages(context.Background(), batchPrincipal(), oversized); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversized: %v", err)
	}
}
