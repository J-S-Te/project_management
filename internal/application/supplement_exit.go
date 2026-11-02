package application

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// BIZ-PROJECT-04：退出补充协议必须经过受控路径。
//
// 修复前的实现有两处互相配合的漏洞：
//  1. CheckScopeChange 在 supplement_status=REQUIRED 时直接放行（"合同已回写"）；
//  2. 仓储 ConfirmServiceItems 只要项目里没有待确认/待复核服务项，就无条件把
//     supplement_status 清零。
//
// 于是"范围变更检测把项目钉进补充协议 → 直接再点一次确认"就能把补充协议静默
// 清掉：既没有用补充协议服务目录重建拆解，也没有任何审计证据说明差异已被消化。
//
// 现在 REQUIRED 分支的退出必须满足受控条件：
//   - 项目上存在一次成功的拆解调整（DECOMPOSITION_ADJUSTED），即补充协议服务
//     目录已经应用到拆解结果；
//   - 当前拆解范围指纹与该次调整生效的范围指纹完全一致（调整后又被改动就必须
//     重新调整）。
//
// 上述条件由既有的「拆解调整」动作满足，因此不会死锁：缺少证据时拒绝确认并提示
// 先做拆解调整。另提供显式关账动作 CloseSupplement，用于拆解已经推进、无法再
// 重建清单的项目（例如部分服务项已确认后才发生范围漂移），同样要求权限、已审批
// 的补充协议与原因，并记录当时接受的范围指纹。
//
// 判定逻辑是纯函数，应用层前置校验与仓储事务内校验共用同一口径，避免"前置校验
// 拦住、事务内又按旧快照清零"。

// DecompositionScopeSnapshot 生成范围指纹（来源清单行|批次|场所|检测类别）。
// 「范围变更检测」与「补充协议受控退出」共用同一口径，避免两处比较的字段集合漂移。
func DecompositionScopeSnapshot(items []domain.ServiceItem) []string {
	return splitScopeSnapshot(items)
}

// SupplementAppliedScope 返回最近一次拆解调整生效的范围指纹；没有调整时返回 nil。
func SupplementAppliedScope(events []domain.DeliveryEvent) []string {
	var latest *domain.DeliveryEvent
	for index := range events {
		event := &events[index]
		if event.Type != EventDecompositionAdjusted {
			continue
		}
		if latest == nil || event.CreatedAt.After(latest.CreatedAt) {
			latest = event
		}
	}
	if latest == nil {
		return nil
	}
	return adjustedEventScope(*latest)
}

// adjustedEventScope 从拆解调整事件载荷里还原已应用的补充协议范围指纹。
// 载荷无法解析时返回 nil：无法证明"目录已应用"就不能当作证据。
func adjustedEventScope(event domain.DeliveryEvent) []string {
	items, err := serviceItemsFromPayload(event.Payload["service_items"])
	if err != nil || len(items) == 0 {
		return nil
	}
	return DecompositionScopeSnapshot(items)
}

// ScopeChangeBaseline 返回项目当前权威的范围基准指纹。
//
// 合同激活、拆解调整（补充协议目录应用）、补充协议关账都会确立新的范围基准，
// 取其中最新的一条：否则补充协议已经应用/关账之后，仍然会用最初合同的清单反复
// 判为"范围漂移"，把项目再次钉回补充协议，形成关不掉的循环。
func ScopeChangeBaseline(events []domain.DeliveryEvent) []string {
	var baseline []string
	var latest time.Time
	for index := range events {
		event := &events[index]
		var snapshot []string
		switch event.Type {
		case EventDecompositionAdjusted:
			snapshot = adjustedEventScope(*event)
		case EventContractActivated, EventSupplementClosed:
			snapshot = payloadStringSlice(event.Payload["scope_snapshot"])
		default:
			continue
		}
		// 只采纳能读出范围指纹的事件：老事件/被截断的载荷不能把基准降级成"无基准"，
		// 否则范围变更检测会静默失效。
		if len(snapshot) == 0 {
			continue
		}
		if baseline == nil || event.CreatedAt.After(latest) {
			baseline, latest = snapshot, event.CreatedAt
		}
	}
	return baseline
}

// scopeChangeDetectionEnabled 读取最新一次合同激活时的「范围变更检测」开关。
func scopeChangeDetectionEnabled(events []domain.DeliveryEvent) bool {
	for index := range events {
		if events[index].Type != EventContractActivated {
			continue
		}
		enabled, _ := events[index].Payload["scope_change_detection"].(bool)
		return enabled
	}
	return false
}

// VerifySupplementExit 判定处于补充协议分支的项目能否受控退出。
// 参数：applied 为最近一次拆解调整已应用的范围指纹（为空表示尚未应用），
// current 为当前拆解结果。
// 返回值：可以退出时为 nil，否则返回可执行的前置条件错误。
func VerifySupplementExit(applied []string, current []domain.ServiceItem) error {
	if len(applied) == 0 {
		return PreconditionError("项目处于补充协议处理中：请先完成「拆解调整」并应用补充协议服务目录，再确认拆解")
	}
	if !slices.Equal(applied, DecompositionScopeSnapshot(current)) {
		return PreconditionError("当前拆解范围与已应用的补充协议服务目录不一致：请重新提交「拆解调整」后再确认拆解")
	}
	return nil
}

// serviceItemsFromPayload 把事件载荷里的服务项清单还原成领域对象。
// 进程内事件是 []domain.ServiceItem，落库后经 JSON 反序列化是 []any，两者都要支持。
func serviceItemsFromPayload(value any) ([]domain.ServiceItem, error) {
	switch typed := value.(type) {
	case []domain.ServiceItem:
		return typed, nil
	case nil:
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var items []domain.ServiceItem
	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// verifySupplementExitForConfirm 在写事务之前拒绝不满足受控退出条件的确认请求，
// 并给出"先做拆解调整"的可执行提示。仓储在事务内用同一判定再复核一次。
func (s *Service) verifySupplementExitForConfirm(ctx context.Context, filter platform.ScopeFilter, ids []string) error {
	deliveryRepo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil
	}
	items, _, err := s.Repo.ListServiceItems(ctx, filter, "", 0, 0)
	if err != nil {
		return err
	}
	projectIDs := make([]string, 0)
	seen := map[string]bool{}
	for _, item := range items {
		projectID := strings.TrimSpace(item.ProjectID)
		// 合并后的服务项一定带项目归属；空归属只可能来自历史/测试桩数据，跳过而不是
		// 用空 ID 去读项目（否则一个脏行会让所有确认请求返回 404）。
		if projectID == "" || !slices.Contains(ids, item.ID) || seen[projectID] {
			continue
		}
		seen[projectID] = true
		projectIDs = append(projectIDs, projectID)
	}
	// 固定项目遍历顺序：多项目批量确认时错误提示必须稳定可复现。
	sort.Strings(projectIDs)
	for _, projectID := range projectIDs {
		project, err := s.Repo.GetProject(ctx, filter, projectID)
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(project.SupplementStatus), "REQUIRED") {
			continue
		}
		events, err := deliveryRepo.ListDeliveryEvents(ctx, filter, projectID)
		if err != nil {
			return err
		}
		current := make([]domain.ServiceItem, 0, len(items))
		for _, item := range items {
			if item.ProjectID == projectID {
				current = append(current, item)
			}
		}
		if err := VerifySupplementExit(SupplementAppliedScope(events), current); err != nil {
			return err
		}
	}
	return nil
}

// CloseSupplement 是补充协议分支的受控退出动作。
//
// 退出补充协议不是"再点一次确认"的副作用：必须由具备拆解管理权限的人显式提交，
// 绑定一份同客户、已审批通过的补充协议，并留下原因与关账时接受的范围指纹。这样
// 即使拆解已经推进、无法再走「拆解调整」重建清单，也有可审计的受控出口，不会
// 把项目永久钉在补充协议处理中。
func (s *Service) CloseSupplement(ctx context.Context, p platform.Principal, projectID string, input domain.SupplementClosureInput) error {
	filter, err := authorizeProjectScope(p, "project.decomposition.manage")
	if err != nil {
		return err
	}
	project, err := s.Repo.GetProject(ctx, filter, projectID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(project.SupplementStatus), "REQUIRED") {
		return PreconditionError("当前项目不在补充协议处理中，无需关账")
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return ValidationError("请填写关闭补充协议的原因")
	}
	if err := validateProjectTextLength(reason, 1000, "关闭补充协议的原因"); err != nil {
		return err
	}
	supplement, err := s.verifyApprovedSupplementContract(ctx, project, input.SupplementContractID)
	if err != nil {
		return err
	}
	items, _, err := s.Repo.ListServiceItems(ctx, filter, projectID, 0, 0)
	if err != nil {
		return err
	}
	return s.applyEvent(ctx, deliveryEvent(p, projectID, "", EventSupplementClosed, map[string]any{
		"reason":                 reason,
		"supplement_contract_id": strings.TrimSpace(supplement.ID),
		// 关账即承认"当前拆解范围"为新的权威基准：范围变更检测随后以它为准，
		// 否则下一次确认会拿最初合同的清单再判一次漂移，项目又被钉回补充协议。
		"scope_snapshot": DecompositionScopeSnapshot(items),
	}))
}
