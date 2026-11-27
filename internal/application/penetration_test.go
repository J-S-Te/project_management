package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// 用途：验证授权信息可以留空保存，且错误的已填日期仍会被拒绝。
// 参数：t 为测试上下文。
// 返回值：无；校验不符时使测试失败。
func TestEmbeddedPenetrationPlanAllowsBlankAuthorizationOnSave(t *testing.T) {
	input := domain.PenetrationPlanInput{
		PlannedStart: "2026-11-27T01:00:00Z", PlannedEnd: "2026-11-27T02:00:00Z",
	}
	if _, _, _, _, err := validateEmbeddedPenetrationPlan(input); err != nil {
		t.Fatalf("未填写授权信息的专项计划应允许保存：%v", err)
	}
	input.AuthStart = "invalid"
	if _, _, _, _, err := validateEmbeddedPenetrationPlan(input); err == nil {
		t.Fatal("已填写的授权日期仍须通过校验")
	}
}

// 用途：验证授权时间已填但其他信息未填时，无法开始测试。
// 参数：t 为测试上下文。
// 返回值：无；开始动作被转发或错误提示不符时使测试失败。
func TestEmbeddedPenetrationStartRequiresAllAuthorizationDetails(t *testing.T) {
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	repo := &penetrationRepositoryStub{
		item: domain.ServiceItem{ID: "SI-1", Category: "等保测评", TestMode: "STANDARD"},
		workPackage: domain.PenetrationWorkPackage{
			EngineerIDs: []string{"engineer"}, AuthStart: start, AuthEnd: end,
			PlannedStart: start, PlannedEnd: end, DecisionStatus: "REQUIRED",
			ExecutionStatus: "NOT_STARTED", Version: 1,
		},
	}
	service := &Service{Repo: repo}
	_, err := service.AdvancePenetrationExecution(context.Background(),
		penetrationPrincipal("engineer", "project.field.execute"), "SI-1",
		domain.PenetrationExecutionInput{Action: "START", ExpectedVersion: 1, IdempotencyKey: "start-1"})
	if err == nil || !strings.Contains(err.Error(), "授权书编号") || repo.executionCall.Action != "" {
		t.Fatalf("授权信息未完善应阻止开始并提示缺项，实际错误：%v", err)
	}
}

type penetrationRepositoryStub struct {
	capabilityRepository
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

// FindCapabilities 返回测试人员档案，实际资格条件由服务层核对。
// 参数：请求上下文和查询条件由测试用例固定。
// 返回值：当前夹具的能力档案及错误。
func (r *penetrationRepositoryStub) FindCapabilities(context.Context, string, string, []string) ([]domain.Capability, error) {
	return r.capabilities, nil
}
func (r *penetrationRepositoryStub) SavePenetrationDecision(_ context.Context, _, _ string, input domain.PenetrationDecisionInput, _ string, _ time.Time) (domain.PenetrationWorkPackage, error) {
	r.workPackage.DecisionStatus = input.DecisionStatus
	return r.workPackage, nil
}

// SavePenetrationPlan 用于让执行校验读取本次保存的测试计划。
// 参数：input 为测试计划，其他参数为夹具无需持久化的请求上下文。
// 返回值：保存后的专项视图及错误。
func (r *penetrationRepositoryStub) SavePenetrationPlan(_ context.Context, _, _ string, input domain.PenetrationPlanInput, _ string, _ time.Time) (domain.PenetrationWorkPackage, error) {
	r.workPackage.EngineerIDs = input.EngineerIDs
	r.workPackage.PlannedStart = input.PlannedStart
	r.workPackage.PlannedEnd = input.PlannedEnd
	r.workPackage.AuthStart = input.AuthStart
	r.workPackage.AuthEnd = input.AuthEnd
	r.workPackage.AuthDocNo = input.AuthDocNo
	r.workPackage.AuthScope = input.AuthScope
	r.workPackage.TestScope = input.TestScope
	r.workPackage.TestWindow = input.TestWindow
	r.workPackage.EmergencyContact = input.EmergencyContact
	r.workPackage.RollbackPlan = input.RollbackPlan
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

// TestEmbeddedPenetrationPlanAcceptsPersonnelQualificationCodes 验证现场资质编码贯通保存和开始。
// 参数：t 为测试上下文。
// 返回值：无。
func TestEmbeddedPenetrationPlanAcceptsPersonnelQualificationCodes(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	start, end := now.Add(-time.Hour).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)
	for _, code := range []string{"渗透测试工程师", " penetration_test "} {
		t.Run(code, func(t *testing.T) {
			repo := &penetrationRepositoryStub{
				capabilityRepository: capabilityRepository{capabilities: []domain.Capability{{
					ResourceType: "PERSON", ResourceID: "P-01", UserID: "eng-1",
					Status: "ACTIVE", IdentityStatus: domain.IdentityStatusActive, Codes: []string{code},
				}}},
				item: domain.ServiceItem{ID: "SI-1", Category: "等保测评", ProjectManagerID: "pm-1"},
			}
			service := &Service{Repo: repo}
			plan := domain.PenetrationPlanInput{
				PlannedStart: start, PlannedEnd: end, AuthStart: start, AuthEnd: end,
				AuthDocNo: "AUTH-1", AuthScope: "合成测试对象", TestScope: "合成测试对象",
				TestWindow: "约定时段", EmergencyContact: "值班人员", RollbackPlan: "停止专项",
				EngineerIDs: []string{"eng-1"}, IdempotencyKey: "plan-1",
			}
			if _, err := service.SavePenetrationPlan(ctx, penetrationPrincipal("pm-1", "project.implementation.plan"), "SI-1", plan); err != nil {
				t.Fatalf("保存有效人员的专项计划: %v", err)
			}
			input := domain.PenetrationExecutionInput{Action: "START", IdempotencyKey: "start-1"}
			if _, err := service.AdvancePenetrationExecution(ctx, penetrationPrincipal("eng-2", "project.field.execute"), "SI-1", input); !errors.Is(err, ErrForbidden) {
				t.Fatalf("未指派人员不允许开始: %v", err)
			}
			if _, err := service.AdvancePenetrationExecution(ctx, penetrationPrincipal("eng-1", "project.field.execute"), "SI-1", input); err != nil {
				t.Fatalf("已指派人员开始专项: %v", err)
			}
		})
	}
}

// TestFindPenetrationEngineerByUsername 验证精确账号结果仍须通过经理权限和人员资质检查。
// 参数：t 为测试上下文。
// 返回值：无。
func TestFindPenetrationEngineerByUsername(t *testing.T) {
	ctx := context.Background()
	repo := &penetrationRepositoryStub{
		capabilityRepository: capabilityRepository{capabilities: []domain.Capability{{
			ResourceType: "PERSON", ResourceID: "P-01", UserID: "eng-1",
			Status: "ACTIVE", IdentityStatus: domain.IdentityStatusActive,
			Codes: []string{"渗透测试工程师"},
		}}},
		item: domain.ServiceItem{ID: "SI-1", Category: "等保测评", ProjectManagerID: "pm-1"},
	}
	directory := &personnelStub{page: platform.OwnerDirectoryPage{
		UsernameMatch: true, Total: 1, Items: []platform.OwnerDirectoryUser{{UserID: "eng-1"}},
	}}
	service := &Service{Repo: repo, Personnel: directory}
	manager := penetrationPrincipal("pm-1", "project.implementation.plan")
	other := penetrationPrincipal("pm-2", "project.implementation.plan")
	if _, err := service.FindPenetrationEngineerByUsername(ctx, other, "SI-1", "fixture_login"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("非当前项目经理: %v", err)
	}
	userID, err := service.FindPenetrationEngineerByUsername(ctx, manager, "SI-1", " fixture_login ")
	if err != nil || userID != "eng-1" || directory.lastQuery.Username != "fixture_login" {
		t.Fatalf("账号查询=%+v user=%q err=%v", directory.lastQuery, userID, err)
	}
	repo.capabilities[0].Status = "DISABLED"
	userID, err = service.FindPenetrationEngineerByUsername(ctx, manager, "SI-1", "fixture_login")
	if err != nil || userID != "" {
		t.Fatalf("停用档案不应参与搜索: user=%q err=%v", userID, err)
	}
	directory.page.UsernameMatch = false
	if _, err = service.FindPenetrationEngineerByUsername(ctx, manager, "SI-1", "fixture_login"); !errors.Is(err, ErrPersonnelUnavailable) {
		t.Fatalf("旧目录的未过滤结果须拒绝: %v", err)
	}
}
