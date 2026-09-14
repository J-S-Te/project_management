package application

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// 合同拆解规则配置 v2 的应用层边界：三块配置的读写，以及按配置解析一次拆解方案。
//
// 与旧实现的关键差异（对齐原型 PG-CFG-01 与「拆解流程」原型）：
//   - 分组维度由配置决定，不再硬编码「场所 + 批次 + 检测类别」；
//   - 「未命中分组规则」不再自动放行到待分配，而是按 missing_rule_action 处理，
//     默认标记「待人工确认」并通知业务管理员；
//   - 检测类别域提供默认体系要求与特殊方法口径（必为特殊方法 → 渗透测试 + 复核窗口）。

// SplitRuleConfigRepository 是拆解规则配置的持久化边界。与站点台账一样按需断言，
// 让只实现部分能力的仓储（含测试桩）继续可用。
type SplitRuleConfigRepository interface {
	GetSplitPolicy(context.Context, string) (domain.SplitPolicy, error)
	SaveSplitPolicy(context.Context, string, domain.SplitPolicy, string) (domain.SplitPolicy, error)
	ListDetectionCategories(context.Context, string) ([]domain.DetectionCategory, error)
	SaveDetectionCategory(context.Context, string, domain.DetectionCategory, string) (domain.DetectionCategory, error)
	DeleteDetectionCategory(context.Context, string, string) error
	ListSplitOverrides(context.Context, string) ([]domain.SplitOverride, error)
	SaveSplitOverride(context.Context, string, domain.SplitOverride, string) (domain.SplitOverride, error)
	DeleteSplitOverride(context.Context, string, int64) (domain.SplitOverride, error)
}

func (s *Service) splitConfigRepo() (SplitRuleConfigRepository, error) {
	repo, ok := s.Repo.(SplitRuleConfigRepository)
	if !ok {
		return nil, ErrNotFound
	}
	return repo, nil
}

// GetSplitPolicy 读取默认分组规则（配置页第一块）。
func (s *Service) GetSplitPolicy(ctx context.Context, p platform.Principal) (domain.SplitPolicy, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.SplitPolicy{}, err
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return domain.SplitPolicy{}, err
	}
	return repo.GetSplitPolicy(ctx, p.TenantID)
}

// SaveSplitPolicy 保存默认分组规则。
func (s *Service) SaveSplitPolicy(ctx context.Context, p platform.Principal, input domain.SplitPolicy) (domain.SplitPolicy, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.SplitPolicy{}, err
	}
	normalized := domain.SplitPolicy{
		DimensionPrimary:           strings.TrimSpace(input.DimensionPrimary),
		DimensionSecondary:         strings.TrimSpace(input.DimensionSecondary),
		DimensionTertiary:          strings.TrimSpace(input.DimensionTertiary),
		DefaultStatus:              strings.TrimSpace(input.DefaultStatus),
		GenerateRequirementSummary: input.GenerateRequirementSummary,
		RequirementSummaryLocked:   input.RequirementSummaryLocked,
		MissingRuleAction:          strings.ToUpper(strings.TrimSpace(input.MissingRuleAction)),
		ScopeChangeDetection:       input.ScopeChangeDetection,
		Enabled:                    input.Enabled,
	}
	if err := domain.ValidateSplitPolicy(normalized); err != nil {
		return domain.SplitPolicy{}, ValidationError(err.Error())
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return domain.SplitPolicy{}, err
	}
	return repo.SaveSplitPolicy(ctx, p.TenantID, normalized, p.UserID)
}

// ListDetectionCategories 读取检测类别域（配置页第二块）。
func (s *Service) ListDetectionCategories(ctx context.Context, p platform.Principal) ([]domain.DetectionCategory, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListDetectionCategories(ctx, p.TenantID)
}

// SaveDetectionCategory 新增或更新一项检测类别。
func (s *Service) SaveDetectionCategory(ctx context.Context, p platform.Principal, input domain.DetectionCategory) (domain.DetectionCategory, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.DetectionCategory{}, err
	}
	normalized := domain.DetectionCategory{
		Category:               strings.TrimSpace(input.Category),
		SystemStandard:         strings.TrimSpace(input.SystemStandard),
		RequiredQualifications: strings.TrimSpace(input.RequiredQualifications),
		RequiredCodes:          strings.Join(domain.SplitCapabilityCodes(input.RequiredCodes), ","),
		SpecialMethod:          strings.ToUpper(strings.TrimSpace(input.SpecialMethod)),
		Enabled:                input.Enabled,
	}
	if normalized.SpecialMethod == "" {
		normalized.SpecialMethod = domain.SpecialMethodNo
	}
	if err := domain.ValidateDetectionCategory(normalized); err != nil {
		return domain.DetectionCategory{}, ValidationError(err.Error())
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return domain.DetectionCategory{}, err
	}
	return repo.SaveDetectionCategory(ctx, p.TenantID, normalized, p.UserID)
}

// DeleteDetectionCategory 删除检测类别；仍被服务项引用时返回冲突。
func (s *Service) DeleteDetectionCategory(ctx context.Context, p platform.Principal, category string) error {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return err
	}
	if strings.TrimSpace(category) == "" {
		return ValidationError("检测类别不能为空")
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return err
	}
	return repo.DeleteDetectionCategory(ctx, p.TenantID, category)
}

// ListSplitOverrides 读取覆盖规则（配置页第三块）。
func (s *Service) ListSplitOverrides(ctx context.Context, p platform.Principal) ([]domain.SplitOverride, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListSplitOverrides(ctx, p.TenantID)
}

// SaveSplitOverride 新增（ID=0）或更新一条覆盖规则。
func (s *Service) SaveSplitOverride(ctx context.Context, p platform.Principal, input domain.SplitOverride) (domain.SplitOverride, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.SplitOverride{}, err
	}
	normalized := domain.SplitOverride{
		ID: input.ID, Name: strings.TrimSpace(input.Name), Match: input.Match,
		Settings: input.Settings, Priority: input.Priority, Enabled: input.Enabled,
	}
	if normalized.Priority <= 0 {
		normalized.Priority = 100
	}
	if err := domain.ValidateSplitOverride(normalized); err != nil {
		return domain.SplitOverride{}, ValidationError(err.Error())
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return domain.SplitOverride{}, err
	}
	return repo.SaveSplitOverride(ctx, p.TenantID, normalized, p.UserID)
}

// DeleteSplitOverride 删除一条覆盖规则。
func (s *Service) DeleteSplitOverride(ctx context.Context, p platform.Principal, id int64) (domain.SplitOverride, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return domain.SplitOverride{}, err
	}
	if id <= 0 {
		return domain.SplitOverride{}, ValidationError("覆盖规则编号不合法")
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return domain.SplitOverride{}, err
	}
	return repo.DeleteSplitOverride(ctx, p.TenantID, id)
}

// SplitPlan 是一次拆解实际生效的方案：默认分组规则叠加命中的覆盖规则。
type SplitPlan struct {
	domain.SplitPolicy
	// AppliedOverrideID/Name 记录命中的覆盖规则（0/空表示只用默认规则）。
	AppliedOverrideID int64
	AppliedOverride   string
}

// SplitOutcome 是一个服务项分组的拆解结果：状态、方法类型、体系要求与缺规则标记。
type SplitOutcome struct {
	Status         string
	TestMode       string
	Special        string
	SystemStandard string
	// MissingRule 为真表示该分组的检测类别不在检测类别域内（缺规则）。
	MissingRule bool
	// RequiredQualifications 是检测类别域给出的必备资质（默认），供页面与分配环节提示。
	RequiredQualifications string
	// RequiredCodes 是检测类别域给出的必检能力码（默认），随服务项落库供分配校验。
	RequiredCodes []string
}

// resolveSplitPlan 解析一次合同拆解生效的分组与状态方案：
// 取优先级最高（值最小）的一条命中覆盖规则，未命中任何覆盖规则时使用默认分组规则。
func (s *Service) resolveSplitPlan(ctx context.Context, tenantID, customer, contract string, serviceItemCount int, categories []string) (SplitPlan, error) {
	repo, err := s.splitConfigRepo()
	if err != nil {
		// 仓储未实现配置能力（例如精简测试桩）时退化为原型默认规则。
		return SplitPlan{SplitPolicy: domain.DefaultSplitPolicy()}, nil
	}
	policy, err := repo.GetSplitPolicy(ctx, tenantID)
	if err != nil {
		return SplitPlan{}, err
	}
	plan := SplitPlan{SplitPolicy: policy}
	overrides, err := repo.ListSplitOverrides(ctx, tenantID)
	if err != nil {
		return SplitPlan{}, err
	}
	for _, override := range overrides {
		if !override.Matches(customer, contract, serviceItemCount, categories) {
			continue
		}
		plan.AppliedOverrideID = override.ID
		plan.AppliedOverride = override.Name
		applySplitOverride(&plan.SplitPolicy, override.Settings)
		break
	}
	return plan, nil
}

// applySplitOverride 把覆盖设置叠加到默认规则上（只覆盖显式给出的字段）。
func applySplitOverride(policy *domain.SplitPolicy, settings domain.SplitOverrideSettings) {
	if settings.DimensionPrimary != nil {
		policy.DimensionPrimary = strings.TrimSpace(*settings.DimensionPrimary)
	}
	if settings.DimensionSecondary != nil {
		policy.DimensionSecondary = strings.TrimSpace(*settings.DimensionSecondary)
	}
	if settings.DimensionTertiary != nil {
		policy.DimensionTertiary = strings.TrimSpace(*settings.DimensionTertiary)
	}
	if settings.DefaultStatus != nil {
		policy.DefaultStatus = strings.TrimSpace(*settings.DefaultStatus)
	}
	if settings.GenerateRequirementSummary != nil {
		policy.GenerateRequirementSummary = *settings.GenerateRequirementSummary
	}
	if settings.RequirementSummaryLocked != nil {
		policy.RequirementSummaryLocked = *settings.RequirementSummaryLocked
	}
	if settings.MissingRuleAction != nil {
		policy.MissingRuleAction = strings.ToUpper(strings.TrimSpace(*settings.MissingRuleAction))
	}
	if settings.ScopeChangeDetection != nil {
		policy.ScopeChangeDetection = *settings.ScopeChangeDetection
	}
}

// splitCategoryDomain 读取检测类别域并按（小写、去空格）类别建立索引。
func (s *Service) splitCategoryDomain(ctx context.Context, tenantID string) (map[string]domain.DetectionCategory, error) {
	repo, err := s.splitConfigRepo()
	if err != nil {
		return map[string]domain.DetectionCategory{}, nil
	}
	items, err := repo.ListDetectionCategories(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	index := make(map[string]domain.DetectionCategory, len(items))
	for _, item := range items {
		index[splitCategoryKey(item.Category)] = item
	}
	return index, nil
}

func splitCategoryKey(category string) string {
	return strings.ToLower(strings.TrimSpace(category))
}

// resolveSplitOutcome 按生效方案与检测类别域决定一个分组的初始状态与方法类型。
//   - 类别在域内：按域的口径定方法类型（必为特殊方法 → 渗透测试；否则沿用清单给的模式），
//     体系要求缺省时取域的默认值；初始状态取「默认进入状态」。
//   - 类别不在域内（缺规则）：按 missing_rule_action 处理，默认标记待人工确认。
func resolveSplitOutcome(plan SplitPlan, category string, sourceMode, sourceSystemStandard string, domainIndex map[string]domain.DetectionCategory) SplitOutcome {
	mode := strings.ToUpper(firstNonEmpty(sourceMode, "STANDARD"))
	systemStandard := strings.TrimSpace(sourceSystemStandard)
	entry, known := domainIndex[splitCategoryKey(category)]
	if !known {
		status := domain.ServiceItemStatusPendingConfirm
		if plan.MissingRuleAction != domain.SplitMissingHumanConfirm {
			// DEFAULT_RULE / SILENT 都按默认规则生成，区别在于是否通知业务管理员。
			status = plan.DefaultStatus
		}
		return SplitOutcome{Status: status, TestMode: mode, Special: yesNo(mode == "PENETRATION"), SystemStandard: systemStandard, MissingRule: true}
	}
	switch entry.SpecialMethod {
	case domain.SpecialMethodRequired:
		mode = "PENETRATION"
	case domain.SpecialMethodNo:
		// 域明确「否」时，清单给的特殊方法标记不生效：以域为准，避免类别配置被绕过。
		mode = "STANDARD"
	}
	if systemStandard == "" {
		systemStandard = entry.SystemStandard
	}
	return SplitOutcome{
		Status: plan.DefaultStatus, TestMode: mode, Special: yesNo(mode == "PENETRATION"),
		SystemStandard: systemStandard, RequiredQualifications: entry.RequiredQualifications,
		RequiredCodes: domain.SplitCapabilityCodes(entry.RequiredCodes),
	}
}

// splitDimensionValue 取某个分组维度在一次拆解中的取值；未知维度返回空串。
func splitDimensionValue(dimension, customer, contract string, source domain.ContractService, mode, systemStandard string) string {
	switch strings.TrimSpace(dimension) {
	case domain.SplitDimensionBatch:
		return strings.TrimSpace(source.Batch)
	case domain.SplitDimensionSite:
		return strings.TrimSpace(source.Site)
	case domain.SplitDimensionCustomer:
		return strings.TrimSpace(customer)
	case domain.SplitDimensionContract:
		return strings.TrimSpace(contract)
	case domain.SplitDimensionCategory:
		return strings.TrimSpace(source.Category)
	case domain.SplitDimensionSystemStandard:
		return strings.TrimSpace(systemStandard)
	case domain.SplitDimensionTestMode:
		return mode
	default:
		return ""
	}
}

// splitGroupKey 按生效方案的分组维度生成分组键：维度 1 常来自合同清单/项目，维度 2/3 来自
// 服务项属性。原型的默认规则是两维（批次 + 检测类别），覆盖规则可以是三维
// （场所 + 批次 + 检测类别）。
func splitGroupKey(plan SplitPlan, customer, contract string, source domain.ContractService, mode, systemStandard string) string {
	parts := []string{
		splitDimensionValue(plan.DimensionPrimary, customer, contract, source, mode, systemStandard),
		splitDimensionValue(plan.DimensionSecondary, customer, contract, source, mode, systemStandard),
	}
	if tertiary := strings.TrimSpace(plan.DimensionTertiary); tertiary != "" {
		parts = append(parts, splitDimensionValue(tertiary, customer, contract, source, mode, systemStandard))
	}
	return strings.Join(parts, "\x00")
}

// splitPlanSummary 生成方案摘要，写入交付事件便于追溯"这条服务项是按哪条规则生成的"。
func splitPlanSummary(plan SplitPlan) string {
	if plan.AppliedOverride != "" {
		return fmt.Sprintf("覆盖规则：%s", plan.AppliedOverride)
	}
	return "默认分组规则"
}

// splitScopeSnapshot 生成"范围指纹"列表：只包含决定拆解范围与分组的字段
// （来源清单行、批次、场所、检测类别），不含技术要求/系统名称等核对阶段允许编辑的内容。
// 用于「范围变更检测」在确认拆解时勾对合同清单与拆解结果。
func splitScopeSnapshot(items []domain.ServiceItem) []string {
	snapshot := make([]string, 0, len(items))
	for _, item := range items {
		snapshot = append(snapshot, strings.Join([]string{
			strings.TrimSpace(item.SourceServiceID), strings.TrimSpace(item.Batch),
			strings.TrimSpace(item.Site), strings.TrimSpace(item.Category),
		}, "|"))
	}
	sort.Strings(snapshot)
	return snapshot
}

// CheckScopeChange 实现「范围变更检测」（原型：开启后确认拆解时勾对合同清单与拆解结果）。
//
// 语义：合同激活时把清单范围指纹写进 CONTRACT_ACTIVATED 事件；确认拆解前再次计算当前拆解
// 结果的范围指纹（只含来源清单行/批次/场所/检测类别，不含核对阶段允许编辑的技术要求与系统名称）。
// 不一致说明范围变了：
//   - 项目还没进补充协议分支 → 记录变更并进入补充协议处理中，要求先完成合同回写；
//   - 项目已在补充协议分支（合同已回写）→ 放行确认，由既有逻辑在全部确认后退回 NONE。
func (s *Service) CheckScopeChange(ctx context.Context, p platform.Principal, filter platform.ScopeFilter, ids []string) error {
	deliveryRepo, ok := s.Repo.(DeliveryRepository)
	if !ok {
		return nil
	}
	items, err := s.Repo.ListServiceItems(ctx, filter, "")
	if err != nil {
		return err
	}
	// 只对本次确认涉及的项目做勾对。
	projectIDs := map[string]bool{}
	for _, item := range items {
		if slices.Contains(ids, item.ID) {
			projectIDs[item.ProjectID] = true
		}
	}
	for projectID := range projectIDs {
		events, err := deliveryRepo.ListDeliveryEvents(ctx, filter, projectID)
		if err != nil {
			return err
		}
		snapshot := []string(nil)
		detection := false
		for index := range events {
			event := &events[index]
			if event.Type != EventContractActivated {
				continue
			}
			detection, _ = event.Payload["scope_change_detection"].(bool)
			snapshot = payloadStringSlice(event.Payload["scope_snapshot"])
			break
		}
		if !detection || len(snapshot) == 0 {
			continue
		}
		project, err := s.Repo.GetProject(ctx, filter, projectID)
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(project.SupplementStatus), "REQUIRED") {
			// 已经在补充协议分支：合同回写后允许确认，否则拆解永远无法收口。
			continue
		}
		current := make([]string, 0, len(items))
		for _, item := range items {
			if item.ProjectID != projectID {
				continue
			}
			current = append(current, strings.Join([]string{
				strings.TrimSpace(item.SourceServiceID), strings.TrimSpace(item.Batch),
				strings.TrimSpace(item.Site), strings.TrimSpace(item.Category),
			}, "|"))
		}
		sort.Strings(current)
		if slices.Equal(current, snapshot) {
			continue
		}
		event := deliveryEvent(p, projectID, "", EventScopeChangeDetected, map[string]any{
			"reason":               "拆解结果与合同清单范围不一致",
			"contract_id":          project.Contract,
			"contract_version":     project.ContractVersion,
			"previous_scope_count": len(snapshot),
			"current_scope_count":  len(current),
		})
		if err := s.applyEvent(ctx, event); err != nil {
			return err
		}
		return PreconditionError("拆解结果与合同清单范围不一致，已按「范围变更检测」进入补充协议处理中；合同回写后重新确认拆解。")
	}
	return nil
}

// payloadStringSlice 读取事件载荷里的字符串数组（JSON 反序列化后是 []any）。
func payloadStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// DetectionCategoryImportResult 是检测类别域导入的结果摘要。
type DetectionCategoryImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors,omitempty"`
}

// ImportDetectionCategories 批量导入检测类别域（CSV 导入的后端入口）。
// 逐行校验：类别为空、特殊方法取值非法、必检能力码过长等行会被跳过并返回行号原因，
// 其余行按「租户 + 类别」幂等写入，不会因为一行错误整批失败。
func (s *Service) ImportDetectionCategories(ctx context.Context, p platform.Principal, rows []domain.DetectionCategory) (DetectionCategoryImportResult, error) {
	if err := requireApplicationAuthorization(p, "project_rule.manage"); err != nil {
		return DetectionCategoryImportResult{}, err
	}
	repo, err := s.splitConfigRepo()
	if err != nil {
		return DetectionCategoryImportResult{}, err
	}
	result := DetectionCategoryImportResult{}
	for index, row := range rows {
		line := fmt.Sprintf("数据行 %d", index+1)
		normalized := domain.DetectionCategory{
			Category:               strings.TrimSpace(row.Category),
			SystemStandard:         strings.TrimSpace(row.SystemStandard),
			RequiredQualifications: strings.TrimSpace(row.RequiredQualifications),
			RequiredCodes:          strings.Join(domain.SplitCapabilityCodes(row.RequiredCodes), ","),
			SpecialMethod:          strings.ToUpper(strings.TrimSpace(row.SpecialMethod)),
			Enabled:                row.Enabled,
		}
		if normalized.SpecialMethod == "" {
			normalized.SpecialMethod = domain.SpecialMethodNo
		}
		if err := domain.ValidateDetectionCategory(normalized); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": "+err.Error())
			continue
		}
		if _, err := repo.SaveDetectionCategory(ctx, p.TenantID, normalized, p.UserID); err != nil {
			result.Skipped++
			result.Errors = append(result.Errors, line+": "+err.Error())
			continue
		}
		result.Imported++
	}
	return result, nil
}
