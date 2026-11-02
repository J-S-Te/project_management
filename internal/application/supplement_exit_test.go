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

// supplementExitRepository 提供可注入的事件账本与项目补充协议状态：
// 「确认拆解是否被拦下」「是否真的落进仓储事务」「有没有重复记范围变更」都要能断言。
type supplementExitRepository struct {
	capabilityRepository
	project   domain.Project
	items     []domain.ServiceItem
	events    []domain.DeliveryEvent
	applied   []domain.DeliveryEvent
	confirmed bool
}

func (r *supplementExitRepository) GetProject(_ context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	project := r.project
	project.ID, project.TenantID = id, filter.TenantID
	return project, nil
}

func (r *supplementExitRepository) ListServiceItems(_ context.Context, _ platform.ScopeFilter, _ string, _, _ int) ([]domain.ServiceItem, int, error) {
	return r.items, len(r.items), nil
}

func (r *supplementExitRepository) ListDeliveryEvents(_ context.Context, _ platform.ScopeFilter, _ string) ([]domain.DeliveryEvent, error) {
	return r.events, nil
}

func (r *supplementExitRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.applied = append(r.applied, event)
	return nil
}

func (r *supplementExitRepository) ConfirmServiceItems(_ context.Context, _ platform.ScopeFilter, _ []string, _ string) ([]domain.ServiceItem, error) {
	r.confirmed = true
	return r.items, nil
}

func confirmPrincipal() platform.Principal {
	return principalWith("service_item.confirm", platform.DataScope{RoleCode: "business_admin", ScopeType: "PROJECT", ScopeID: "PJ-1"})
}

func decompositionPrincipal(permission string) platform.Principal {
	return principalWith(permission, platform.DataScope{RoleCode: "business_admin", ScopeType: "PROJECT", ScopeID: "PJ-1"})
}

// supplementScopeItems 是测试用的拆解结果：范围指纹由来源清单行/批次/场所/检测类别组成。
func supplementScopeItems() []domain.ServiceItem {
	return []domain.ServiceItem{
		{ID: "SI-1", ProjectID: "PJ-1", SourceServiceID: "S1", Batch: "B1", Site: "现场A", Category: "等保测评", Status: "待确认"},
	}
}

// contractActivationEvent 复刻合同激活时写入的范围指纹与检测开关。
func contractActivationEvent(snapshot ...string) domain.DeliveryEvent {
	return domain.DeliveryEvent{
		ID: "EV-ACT", ProjectID: "PJ-1", Type: EventContractActivated, CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		Payload: map[string]any{"scope_change_detection": true, "scope_snapshot": snapshot},
	}
}

// adjustmentEvent 复刻一次已应用的补充协议目录（拆解调整）。
func adjustmentEvent(items []domain.ServiceItem) domain.DeliveryEvent {
	return domain.DeliveryEvent{
		ID: "EV-ADJ", ProjectID: "PJ-1", Type: EventDecompositionAdjusted, CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		Payload: map[string]any{"reason": "客户追加范围", "supplement_contract_id": "C-SUP", "service_items": items},
	}
}

// BIZ-PROJECT-04：仍在补充协议分支且没有应用补充协议目录时，确认拆解必须被拒绝，
// 不能像修复前那样直接放行并由仓储把 supplement_status 静默清零。
func TestConfirmRejectsSupplementExitWithoutAppliedAdjustment(t *testing.T) {
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "REQUIRED"},
		items:   supplementScopeItems(),
		events:  []domain.DeliveryEvent{contractActivationEvent("S0|B0|旧现场|等保测评")},
	}
	service := &Service{Repo: repository}
	_, err := service.ConfirmServiceItems(context.Background(), confirmPrincipal(), []string{"SI-1"})
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("err = %v, want ErrPrecondition", err)
	}
	if message := UserMessage(err); !strings.Contains(message, "拆解调整") {
		t.Fatalf("提示必须给出可执行动作（先做拆解调整），got %q", message)
	}
	if repository.confirmed {
		t.Fatal("未满足受控退出条件时不得进入仓储确认事务")
	}
	if len(repository.applied) != 0 {
		t.Fatalf("补充协议分支内不应重复记录范围变更事件: %+v", repository.applied)
	}
}

// 受控退出：补充协议目录已通过「拆解调整」应用，且当前范围与之完全一致 → 允许确认。
func TestConfirmAllowsSupplementExitAfterAppliedAdjustment(t *testing.T) {
	items := supplementScopeItems()
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "REQUIRED"},
		items:   items,
		events:  []domain.DeliveryEvent{contractActivationEvent("S0|B0|旧现场|等保测评"), adjustmentEvent(items)},
	}
	service := &Service{Repo: repository}
	if _, err := service.ConfirmServiceItems(context.Background(), confirmPrincipal(), []string{"SI-1"}); err != nil {
		t.Fatalf("已应用补充协议目录后应允许退出: %v", err)
	}
	if !repository.confirmed {
		t.Fatal("满足受控退出条件时应进入仓储确认事务")
	}
	if len(repository.applied) != 0 {
		t.Fatalf("受控退出不应再追加范围变更事件（否则会无限钉回补充协议）: %+v", repository.applied)
	}
}

// 调整之后范围又被改动（批次被改）：必须以最新调整指纹为基准再次要求调整，而不是放行。
func TestConfirmRejectsSupplementExitAfterScopeDrift(t *testing.T) {
	applied := supplementScopeItems()
	current := supplementScopeItems()
	current[0].Batch = "B2"
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "REQUIRED"},
		items:   current,
		events:  []domain.DeliveryEvent{contractActivationEvent("S0|B0|旧现场|等保测评"), adjustmentEvent(applied)},
	}
	service := &Service{Repo: repository}
	_, err := service.ConfirmServiceItems(context.Background(), confirmPrincipal(), []string{"SI-1"})
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("err = %v, want ErrPrecondition", err)
	}
	if message := UserMessage(err); !strings.Contains(message, "不一致") {
		t.Fatalf("提示应说明范围与已应用目录不一致，got %q", message)
	}
	if repository.confirmed {
		t.Fatal("范围漂移时不得进入仓储确认事务")
	}
}

// 正常路径不回归：项目不在补充协议分支且范围与合同清单一致时确认照常通过。
func TestConfirmNormalPathStillPassesWithoutSupplementBranch(t *testing.T) {
	items := supplementScopeItems()
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "NONE"},
		items:   items,
		events:  []domain.DeliveryEvent{contractActivationEvent(DecompositionScopeSnapshot(items)...)},
	}
	service := &Service{Repo: repository}
	if _, err := service.ConfirmServiceItems(context.Background(), confirmPrincipal(), []string{"SI-1"}); err != nil {
		t.Fatalf("正常确认路径不应被补充协议校验拦截: %v", err)
	}
	if !repository.confirmed {
		t.Fatal("正常路径应进入仓储确认事务")
	}
	if len(repository.applied) != 0 {
		t.Fatalf("范围一致时不应记录范围变更: %+v", repository.applied)
	}
}

// 补充协议关账是受控退出动作：权限、已审批补充协议与原因缺一不可。
func TestCloseSupplementRequiresPermissionContractAndReason(t *testing.T) {
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "REQUIRED", ContractID: "C-ORIG", Contract: "HT-ORIG", Customer: "客户"},
		items:   supplementScopeItems(),
	}
	service := &Service{Repo: repository, Contracts: approvedContractVerifierStub{}}

	if err := service.CloseSupplement(context.Background(), decompositionPrincipal("project.read"), "PJ-1", domain.SupplementClosureInput{SupplementContractID: "C-SUP", Reason: "补充协议已回写"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("缺少拆解管理权限时 err = %v, want ErrForbidden", err)
	}
	if err := service.CloseSupplement(context.Background(), decompositionPrincipal("project.decomposition.manage"), "PJ-1", domain.SupplementClosureInput{SupplementContractID: "C-SUP"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("缺少原因时 err = %v, want ErrValidation", err)
	}
	if err := service.CloseSupplement(context.Background(), decompositionPrincipal("project.decomposition.manage"), "PJ-1", domain.SupplementClosureInput{Reason: "补充协议已回写"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("缺少补充协议时 err = %v, want ErrValidation", err)
	}
	if err := service.CloseSupplement(context.Background(), decompositionPrincipal("project.decomposition.manage"), "PJ-1", domain.SupplementClosureInput{SupplementContractID: "C-ORIG", Reason: "补充协议已回写"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("用原合同冒充补充协议时 err = %v, want ErrValidation", err)
	}
	if len(repository.applied) != 0 {
		t.Fatalf("被拒绝的关账不得写事件: %+v", repository.applied)
	}
}

// 关账事件必须携带补充协议、原因与关账时接受的范围指纹，供范围变更检测换基准。
func TestCloseSupplementEmitsControlledExitWithScopeSnapshot(t *testing.T) {
	items := supplementScopeItems()
	repository := &supplementExitRepository{
		project: domain.Project{SupplementStatus: "REQUIRED", ContractID: "C-ORIG", Contract: "HT-ORIG", Customer: "客户"},
		items:   items,
	}
	service := &Service{Repo: repository, Contracts: approvedContractVerifierStub{}}
	if err := service.CloseSupplement(context.Background(), decompositionPrincipal("project.decomposition.manage"), "PJ-1", domain.SupplementClosureInput{SupplementContractID: "C-SUP", Reason: "补充协议已回写并确认范围"}); err != nil {
		t.Fatalf("受控关账失败: %v", err)
	}
	if len(repository.applied) != 1 || repository.applied[0].Type != EventSupplementClosed {
		t.Fatalf("关账应写入 %s 事件: %+v", EventSupplementClosed, repository.applied)
	}
	event := repository.applied[0]
	if event.Payload["supplement_contract_id"] != "C-SUP" || !strings.Contains(payloadText(event.Payload, "reason"), "补充协议已回写") {
		t.Fatalf("关账事件缺少补充协议或原因: %+v", event.Payload)
	}
	if got, want := payloadStringSlice(event.Payload["scope_snapshot"]), DecompositionScopeSnapshot(items); len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("关账应记录接受的范围指纹: got %v want %v", got, want)
	}
}

// 关账之后范围变更检测必须换用关账时接受的范围基准，否则确认又被钉回补充协议。
func TestScopeBaselineFollowsSupplementClosure(t *testing.T) {
	items := supplementScopeItems()
	accepted := DecompositionScopeSnapshot(items)
	closed := domain.DeliveryEvent{
		Type: EventSupplementClosed, CreatedAt: time.Now().UTC(),
		Payload: map[string]any{"scope_snapshot": accepted},
	}
	if got := ScopeChangeBaseline([]domain.DeliveryEvent{contractActivationEvent("S0|B0|旧现场|等保测评"), closed}); len(got) != len(accepted) || got[0] != accepted[0] {
		t.Fatalf("关账后基准 = %v, want %v", got, accepted)
	}
	// 拆解调整同样确立新基准（补充协议目录已应用的场景）。
	adjusted := adjustmentEvent(items)
	if got := ScopeChangeBaseline([]domain.DeliveryEvent{contractActivationEvent("S0|B0|旧现场|等保测评"), adjusted}); len(got) != len(accepted) || got[0] != accepted[0] {
		t.Fatalf("拆解调整后基准 = %v, want %v", got, accepted)
	}
}
