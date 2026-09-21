package application

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type penetrationRepositoryStub struct {
	scopeRepository
	item          domain.ServiceItem
	workPackage   domain.PenetrationWorkPackage
	executionCall domain.PenetrationExecutionInput
}

func (r *penetrationRepositoryStub) GetServiceItem(context.Context, platform.ScopeFilter, string) (domain.ServiceItem, error) {
	return r.item, nil
}
func (r *penetrationRepositoryStub) EnsurePenetrationWorkPackage(context.Context, string, domain.ServiceItem, string, string, time.Time) (domain.PenetrationWorkPackage, error) {
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) GetPenetrationWorkPackage(context.Context, string, string) (domain.PenetrationWorkPackage, error) {
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) SavePenetrationDecision(_ context.Context, _, _ string, input domain.PenetrationDecisionInput, _ string, _ time.Time) (domain.PenetrationWorkPackage, error) {
	r.workPackage.DecisionStatus = input.DecisionStatus
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) SavePenetrationPlan(context.Context, string, string, domain.PenetrationPlanInput, string, time.Time) (domain.PenetrationWorkPackage, error) {
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) AdvancePenetrationExecution(_ context.Context, _, _ string, input domain.PenetrationExecutionInput, _ string, _ time.Time) (domain.PenetrationWorkPackage, error) {
	r.executionCall = input
	if input.Action == "CANCEL" {
		r.workPackage.DecisionStatus = "NOT_REQUIRED"
		r.workPackage.ExecutionStatus = "CANCELLED"
	}
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) AdvancePenetrationReport(context.Context, string, string, domain.PenetrationReportStatusInput, string, time.Time) (domain.PenetrationWorkPackage, error) {
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) RegisterPenetrationReportArtifact(context.Context, string, string, domain.PenetrationReportArtifactInput, string, time.Time) (domain.PenetrationWorkPackage, error) {
	return r.workPackage, nil
}
func (r *penetrationRepositoryStub) ListPenetrationReportRevisions(context.Context, string, string) ([]domain.ReportRevision, error) {
	return nil, nil
}
func (r *penetrationRepositoryStub) ListPenetrationPersonnelConflicts(context.Context, string, string, []string, time.Time, time.Time) ([]string, error) {
	return nil, nil
}
func (r *penetrationRepositoryStub) PenetrationWorkPackageStats(context.Context, platform.ScopeFilter) (domain.PenetrationWorkPackageStats, error) {
	return domain.PenetrationWorkPackageStats{}, nil
}

func penetrationPrincipal(userID string, permissions ...string) platform.Principal {
	grants := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		grants[permission] = true
	}
	return platform.Principal{
		TenantID: "tenant-1", UserID: userID, IdentityID: userID, Permissions: grants,
		DataScopes: []platform.DataScope{{RoleCode: "project_manager", ScopeType: "APPLICATION"}},
	}
}

func TestEmbeddedPenetrationPackageRejectsStandalonePenetrationService(t *testing.T) {
	repo := &penetrationRepositoryStub{item: domain.ServiceItem{
		ID: "SI-1", ProjectID: "PJ-1", Category: "渗透测试", TestMode: "PENETRATION", ProjectManagerID: "pm-1", Version: 2,
	}}
	service := &Service{Repo: repo}
	_, err := service.EnsurePenetrationWorkPackage(context.Background(), penetrationPrincipal("pm-1", "project.implementation.plan"), "SI-1", domain.EnsurePenetrationWorkPackageInput{ExpectedVersion: 2, IdempotencyKey: "ensure-1"})
	if err == nil {
		t.Fatal("standalone penetration service must not create an embedded work package")
	}
}

func TestEmbeddedPenetrationEnsureRequiresParentVersion(t *testing.T) {
	repo := &penetrationRepositoryStub{item: domain.ServiceItem{
		ID: "SI-1", ProjectID: "PJ-1", Category: "等级保护测评", TestMode: "STANDARD", ProjectManagerID: "pm-1", Version: 2,
	}}
	service := &Service{Repo: repo}
	_, err := service.EnsurePenetrationWorkPackage(context.Background(), penetrationPrincipal("pm-1", "project.implementation.plan"), "SI-1", domain.EnsurePenetrationWorkPackageInput{IdempotencyKey: "ensure-1"})
	if err == nil {
		t.Fatal("ensure without expected_version must be rejected")
	}
}

func TestEmbeddedPenetrationDecisionCannotBypassExecutionCancellation(t *testing.T) {
	repo := &penetrationRepositoryStub{
		item:        domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Category: "等保测评", TestMode: "STANDARD", ProjectManagerID: "pm-1", Version: 2},
		workPackage: domain.PenetrationWorkPackage{ParentServiceItemID: "SI-1", DecisionStatus: "REQUIRED", ExecutionStatus: "IN_PROGRESS", ReportStatus: "NONE", Version: 3},
	}
	service := &Service{Repo: repo}
	_, err := service.SavePenetrationDecision(context.Background(), penetrationPrincipal("pm-1", "project.implementation.plan"), "SI-1", domain.PenetrationDecisionInput{
		DecisionStatus: "NOT_REQUIRED", CustomerContact: "客户", CommunicatedAt: time.Now().UTC().Format(time.RFC3339),
		CommunicationSummary: "取消", ChangeReason: "客户调整", ExpectedVersion: 3, IdempotencyKey: "decision-1",
	})
	if err == nil {
		t.Fatal("an in-progress package must use the audited cancellation action")
	}
}

func TestProjectManagerCanCancelWithPlanPermissionAndCommunicationRecord(t *testing.T) {
	repo := &penetrationRepositoryStub{
		item:        domain.ServiceItem{ID: "SI-1", ProjectID: "PJ-1", Category: "等保测评", TestMode: "STANDARD", ProjectManagerID: "pm-1", Version: 2},
		workPackage: domain.PenetrationWorkPackage{ParentServiceItemID: "SI-1", DecisionStatus: "REQUIRED", ExecutionStatus: "IN_PROGRESS", ReportStatus: "NONE", Version: 3},
	}
	service := &Service{Repo: repo}
	result, err := service.AdvancePenetrationExecution(context.Background(), penetrationPrincipal("pm-1", "project.implementation.plan"), "SI-1", domain.PenetrationExecutionInput{
		Action: "CANCEL", Reason: "客户取消", CustomerContact: "张三", CommunicatedAt: time.Now().UTC().Format(time.RFC3339),
		CommunicationSummary: "确认不开展", ExpectedVersion: 3, IdempotencyKey: "cancel-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.executionCall.Action != "CANCEL" || result.DecisionStatus != "NOT_REQUIRED" || result.ExecutionStatus != "CANCELLED" {
		t.Fatalf("call=%+v result=%+v", repo.executionCall, result)
	}
}
