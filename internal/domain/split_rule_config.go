package domain

import (
	"fmt"
	"strings"
)

// 合同拆解规则配置 v2（原型 PG-CFG-01）：默认分组规则 + 检测类别域 + 覆盖规则。
//
// 它与旧模型（pm_split_rule 的自由文本「适用范围」）的区别：
//   - 分组维度可配（原型：分组维度 1 × 分组维度 2 = 1 个服务项）；
//   - 初始状态由「默认进入状态」决定，而不是由"是否命中适用范围"反推；
//   - 检测类别域提供默认体系要求与特殊方法口径；
//   - 覆盖规则按客户/合同/服务项数覆盖默认规则，按优先级取第一条命中。

// 分组维度取值。
const (
	// 维度 1：合同清单里可用的分组字段。
	SplitDimensionBatch    = "batch"    // 批次
	SplitDimensionSite     = "site"     // 场所
	SplitDimensionCustomer = "customer" // 客户
	SplitDimensionContract = "contract" // 合同
	// 维度 2：服务项属性。
	SplitDimensionCategory       = "category"        // 检测类别
	SplitDimensionSystemStandard = "system_standard" // 体系要求
	SplitDimensionTestMode       = "test_mode"       // 方法类型
)

// 分组规则缺失时的处理口径。
const (
	// SplitMissingHumanConfirm 标记「待人工确认」并通知业务管理员（原型默认）。
	SplitMissingHumanConfirm = "HUMAN_CONFIRM"
	// SplitMissingDefaultRule 按默认规则生成。
	SplitMissingDefaultRule = "DEFAULT_RULE"
	// SplitMissingSilent 按默认规则生成但不通知（排障用，原型标注"不推荐"）。
	SplitMissingSilent = "SILENT"
)

// 检测类别的特殊方法口径。
const (
	// SpecialMethodNo 不是特殊方法（按合同清单给定的方法类型即可）。
	SpecialMethodNo = "NO"
	// SpecialMethodMarkable 可标记为特殊方法。
	SpecialMethodMarkable = "MARKABLE"
	// SpecialMethodRequired 必为特殊方法（拆解即置为渗透测试并进入复核窗口）。
	SpecialMethodRequired = "REQUIRED"
)

// 服务项初始状态（拆解产物只可能是这两个之一，其余状态由后续事件推进）。
const (
	ServiceItemStatusPendingConfirm = "待确认"
	ServiceItemStatusPendingAssign  = "待分配"
)

var splitDimensionPrimaryLabels = map[string]string{
	SplitDimensionBatch: "批次", SplitDimensionSite: "场所",
	SplitDimensionCustomer: "客户", SplitDimensionContract: "合同",
}

var splitDimensionSecondaryLabels = map[string]string{
	SplitDimensionCategory: "检测类别", SplitDimensionSystemStandard: "体系要求", SplitDimensionTestMode: "方法类型",
}

// validSplitDimension 判断维度取值是否合法。维度取值可以出现在任意位置：
// 原型的三维覆盖示例就是「场所 + 批次 + 检测类别」，前两维都来自清单/项目侧。
func validSplitDimension(value string) bool {
	value = strings.TrimSpace(value)
	if _, ok := splitDimensionPrimaryLabels[value]; ok {
		return true
	}
	_, ok := splitDimensionSecondaryLabels[value]
	return ok
}

// SplitDimensionLabel 返回维度取值的中文名，未知取值原样返回。
func SplitDimensionLabel(value string) string {
	value = strings.TrimSpace(value)
	if label, ok := splitDimensionPrimaryLabels[value]; ok {
		return label
	}
	if label, ok := splitDimensionSecondaryLabels[value]; ok {
		return label
	}
	return value
}

// SplitPolicy 是「默认分组规则」：一个租户一行。
type SplitPolicy struct {
	TenantID                   string `json:"-"`
	ID                         int64  `json:"id,omitempty"`
	DimensionPrimary           string `json:"dimension_primary"`
	DimensionSecondary         string `json:"dimension_secondary"`
	DimensionTertiary          string `json:"dimension_tertiary"`
	DefaultStatus              string `json:"default_status"`
	GenerateRequirementSummary bool   `json:"generate_requirement_summary"`
	RequirementSummaryLocked   bool   `json:"requirement_summary_locked"`
	MissingRuleAction          string `json:"missing_rule_action"`
	ScopeChangeDetection       bool   `json:"scope_change_detection"`
	Enabled                    bool   `json:"enabled"`
	Updated                    string `json:"updated,omitempty"`
}

// DefaultSplitPolicy 返回原型的默认分组规则：同一批次 + 同一检测类别 = 1 个服务项，
// 初始状态待确认，缺规则时标记待人工确认并通知业务管理员。
// 租户未配置时按此执行，页面首次打开也会落到这份默认值上。
func DefaultSplitPolicy() SplitPolicy {
	return SplitPolicy{
		DimensionPrimary:           SplitDimensionBatch,
		DimensionSecondary:         SplitDimensionCategory,
		DefaultStatus:              ServiceItemStatusPendingConfirm,
		GenerateRequirementSummary: true,
		RequirementSummaryLocked:   false,
		MissingRuleAction:          SplitMissingHumanConfirm,
		ScopeChangeDetection:       true,
		Enabled:                    true,
	}
}

// ValidateSplitPolicy 校验默认分组规则。分组维度必须成对且不重复，
// 否则「1 个服务项」的构成会有歧义；初始状态只允许待确认/待分配。
func ValidateSplitPolicy(policy SplitPolicy) error {
	if !validSplitDimension(policy.DimensionPrimary) {
		return fmt.Errorf("分组维度 1 取值不合法：%s", policy.DimensionPrimary)
	}
	if !validSplitDimension(policy.DimensionSecondary) {
		return fmt.Errorf("分组维度 2 取值不合法：%s", policy.DimensionSecondary)
	}
	// 维度 3 可选；一旦给出就必须合法且不与前两维重复，否则分组键里有重复字段。
	tertiary := strings.TrimSpace(policy.DimensionTertiary)
	if tertiary != "" {
		if !validSplitDimension(tertiary) {
			return fmt.Errorf("分组维度 3 取值不合法：%s", tertiary)
		}
		if tertiary == policy.DimensionPrimary || tertiary == policy.DimensionSecondary {
			return fmt.Errorf("分组维度 3 不能与前两维相同")
		}
	}
	// 维度 1 与维度 2 落到同一字段时分组会退化成单维，属于配置错误。
	if policy.DimensionPrimary == policy.DimensionSecondary {
		return fmt.Errorf("分组维度 1 与维度 2 不能相同")
	}
	switch policy.DefaultStatus {
	case ServiceItemStatusPendingConfirm, ServiceItemStatusPendingAssign:
	default:
		return fmt.Errorf("默认进入状态取值不合法：%s", policy.DefaultStatus)
	}
	switch policy.MissingRuleAction {
	case SplitMissingHumanConfirm, SplitMissingDefaultRule, SplitMissingSilent:
	default:
		return fmt.Errorf("分组规则缺失时的处理取值不合法：%s", policy.MissingRuleAction)
	}
	return nil
}

// SplitCapabilityCodes 把逗号分隔的必检能力码解析成去重列表（兼容中英文逗号）。
func SplitCapabilityCodes(value string) []string {
	codes := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' || r == ';' || r == '；' }) {
		code := strings.TrimSpace(part)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes
}

// DetectionCategory 是检测类别（服务类型）域的一项。
type DetectionCategory struct {
	TenantID string `json:"-"`
	ID       int64  `json:"id,omitempty"`
	Category string `json:"category"`
	// SystemStandard 是默认体系要求（等保 2.0 / ISO 9001 / ISO 27001 …）。
	SystemStandard string `json:"system_standard"`
	// RequiredQualifications 是必备资质（默认），自由文本，供展示与分配提示。
	RequiredQualifications string `json:"required_qualifications"`
	// RequiredCodes 是必检能力码（默认），逗号分隔；填写后拆解出来的服务项会带上它，
	// 分配工程师时按此做能力校验。留空表示不额外校验（渗透测试仍自动加 PENETRATION_TEST）。
	RequiredCodes string `json:"required_codes"`
	// SpecialMethod 取 SpecialMethodNo / SpecialMethodMarkable / SpecialMethodRequired。
	SpecialMethod string `json:"special_method"`
	Enabled       bool   `json:"enabled"`
	// ServiceItemCount 是关联服务项数（只读统计，随列表返回）。
	ServiceItemCount int    `json:"service_item_count,omitempty"`
	Updated          string `json:"updated,omitempty"`
}

// ValidateDetectionCategory 校验检测类别项。
func ValidateDetectionCategory(item DetectionCategory) error {
	if strings.TrimSpace(item.Category) == "" {
		return fmt.Errorf("检测类别不能为空")
	}
	if len([]rune(item.Category)) > 128 {
		return fmt.Errorf("检测类别过长")
	}
	switch item.SpecialMethod {
	case SpecialMethodNo, SpecialMethodMarkable, SpecialMethodRequired:
	default:
		return fmt.Errorf("是否特殊方法取值不合法：%s", item.SpecialMethod)
	}
	for _, code := range SplitCapabilityCodes(item.RequiredCodes) {
		if len([]rune(code)) > 64 {
			return fmt.Errorf("必检能力码过长：%s", code)
		}
	}
	return nil
}

// DefaultDetectionCategories 返回原型「Q1 已确认」的检测类别域初始值。
// 原型表格渲染了 17 行（页脚标注共 19 类），这里以可见行为准，缺的类别由管理员新增。
func DefaultDetectionCategories() []DetectionCategory {
	entries := []struct{ category, systemStandard, qualifications, special string }{
		{"等保测评", "等保 2.0", "等级保护测评师（中级+）", SpecialMethodNo},
		{"商用密码应用安全性评估", "商用密码", "商用密码评估人员", SpecialMethodMarkable},
		{"软件测试", "ISO 9001", "软件评测师", SpecialMethodNo},
		{"源代码审计", "等保 2.0", "等级保护测评师（中级+）", SpecialMethodNo},
		{"渗透测试", "ISO 27001", "CISP-PTE", SpecialMethodRequired},
		{"漏洞扫描", "", "", SpecialMethodNo},
		{"APP 安全加固", "", "", SpecialMethodNo},
		{"上线测试", "", "", SpecialMethodNo},
		{"网络安全风险评估", "ISO 27001", "", SpecialMethodNo},
		{"差距分析", "", "", SpecialMethodNo},
		{"机房检测", "", "", SpecialMethodNo},
		{"网络安全巡检服务", "", "", SpecialMethodNo},
		{"安全培训", "", "", SpecialMethodNo},
		{"安全性测试", "", "", SpecialMethodNo},
		{"应急响应服务", "", "", SpecialMethodMarkable},
		{"网络安全攻防演练", "", "", SpecialMethodMarkable},
		{"安全运维", "", "", SpecialMethodNo},
	}
	items := make([]DetectionCategory, 0, len(entries))
	for _, entry := range entries {
		items = append(items, DetectionCategory{
			Category: entry.category, SystemStandard: entry.systemStandard,
			RequiredQualifications: entry.qualifications, SpecialMethod: entry.special, Enabled: true,
		})
	}
	return items
}

// SplitOverrideMatch 是覆盖规则的匹配条件。只使用项目系统自己可判定的字段：
// 客户名/合同号包含关系、合同服务项数区间、检测类别集合。客户行业、客户标签等
// CRM 字段留待集成（见 docs/business-model.md 的待集成项）。
type SplitOverrideMatch struct {
	CustomerContains string   `json:"customer_contains,omitempty"`
	ContractContains string   `json:"contract_contains,omitempty"`
	MinServiceItems  int      `json:"min_service_items,omitempty"`
	MaxServiceItems  int      `json:"max_service_items,omitempty"`
	Categories       []string `json:"categories,omitempty"`
}

// Empty 判断匹配条件是否为空：空条件会匹配所有合同，容易被误配成"全局覆盖"。
func (m SplitOverrideMatch) Empty() bool {
	return strings.TrimSpace(m.CustomerContains) == "" && strings.TrimSpace(m.ContractContains) == "" &&
		m.MinServiceItems == 0 && m.MaxServiceItems == 0 && len(m.Categories) == 0
}

// SplitOverrideSettings 是覆盖规则的覆盖设置：只写需要覆盖的字段，nil 表示沿用默认规则。
type SplitOverrideSettings struct {
	DimensionPrimary           *string `json:"dimension_primary,omitempty"`
	DimensionSecondary         *string `json:"dimension_secondary,omitempty"`
	DimensionTertiary          *string `json:"dimension_tertiary,omitempty"`
	DefaultStatus              *string `json:"default_status,omitempty"`
	GenerateRequirementSummary *bool   `json:"generate_requirement_summary,omitempty"`
	RequirementSummaryLocked   *bool   `json:"requirement_summary_locked,omitempty"`
	MissingRuleAction          *string `json:"missing_rule_action,omitempty"`
	ScopeChangeDetection       *bool   `json:"scope_change_detection,omitempty"`
}

// Empty 判断覆盖设置是否为空：空设置等于没有覆盖任何东西，属于无效配置。
func (s SplitOverrideSettings) Empty() bool {
	return s.DimensionPrimary == nil && s.DimensionSecondary == nil && s.DimensionTertiary == nil && s.DefaultStatus == nil &&
		s.GenerateRequirementSummary == nil && s.RequirementSummaryLocked == nil &&
		s.MissingRuleAction == nil && s.ScopeChangeDetection == nil
}

// SplitOverride 是覆盖规则。
type SplitOverride struct {
	TenantID string                `json:"-"`
	ID       int64                 `json:"id,omitempty"`
	Name     string                `json:"name"`
	Match    SplitOverrideMatch    `json:"match_conditions"`
	Settings SplitOverrideSettings `json:"override_settings"`
	// Priority 越小越先匹配（原型示例：10 / 20 / 30）。
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
	Updated  string `json:"updated,omitempty"`
}

// ValidateSplitOverride 校验覆盖规则：名称、匹配条件与覆盖设置都不能为空，
// 否则「命中即覆盖」的语义会退化成无意义的规则堆。
func ValidateSplitOverride(item SplitOverride) error {
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("覆盖规则名称不能为空")
	}
	if item.Match.Empty() {
		return fmt.Errorf("覆盖规则至少要有一个匹配条件")
	}
	if item.Settings.Empty() {
		return fmt.Errorf("覆盖规则至少要覆盖一项设置")
	}
	if item.Match.MinServiceItems < 0 || item.Match.MaxServiceItems < 0 {
		return fmt.Errorf("服务项数区间不能为负")
	}
	if item.Match.MaxServiceItems > 0 && item.Match.MinServiceItems > item.Match.MaxServiceItems {
		return fmt.Errorf("服务项数区间起点不能大于终点")
	}
	if item.Priority <= 0 {
		return fmt.Errorf("优先级必须为正整数")
	}
	if item.Settings.DimensionPrimary != nil && !validSplitDimension(*item.Settings.DimensionPrimary) {
		return fmt.Errorf("覆盖的分组维度 1 取值不合法：%s", *item.Settings.DimensionPrimary)
	}
	if item.Settings.DimensionSecondary != nil && !validSplitDimension(*item.Settings.DimensionSecondary) {
		return fmt.Errorf("覆盖的分组维度 2 取值不合法：%s", *item.Settings.DimensionSecondary)
	}
	if item.Settings.DimensionTertiary != nil && strings.TrimSpace(*item.Settings.DimensionTertiary) != "" &&
		!validSplitDimension(*item.Settings.DimensionTertiary) {
		return fmt.Errorf("覆盖的分组维度 3 取值不合法：%s", *item.Settings.DimensionTertiary)
	}
	if item.Settings.DefaultStatus != nil {
		switch strings.TrimSpace(*item.Settings.DefaultStatus) {
		case ServiceItemStatusPendingConfirm, ServiceItemStatusPendingAssign:
		default:
			return fmt.Errorf("覆盖的默认进入状态取值不合法：%s", *item.Settings.DefaultStatus)
		}
	}
	if item.Settings.MissingRuleAction != nil {
		switch strings.TrimSpace(*item.Settings.MissingRuleAction) {
		case SplitMissingHumanConfirm, SplitMissingDefaultRule, SplitMissingSilent:
		default:
			return fmt.Errorf("覆盖的分组规则缺失处理取值不合法：%s", *item.Settings.MissingRuleAction)
		}
	}
	return nil
}

// Matches 判断覆盖规则是否命中一次合同拆解。
func (o SplitOverride) Matches(customer, contract string, serviceItemCount int, categories []string) bool {
	if !o.Enabled {
		return false
	}
	if o.Match.Empty() {
		return false
	}
	// 已给出的条件必须全部满足（AND 语义）：任一条件不满足即不命中，
	// 否则「客户行业 + 服务项数」这类组合条件会退化成"满足其一即覆盖"。
	if text := strings.ToLower(strings.TrimSpace(o.Match.CustomerContains)); text != "" {
		if !strings.Contains(strings.ToLower(customer), text) {
			return false
		}
	}
	if text := strings.ToLower(strings.TrimSpace(o.Match.ContractContains)); text != "" {
		if !strings.Contains(strings.ToLower(contract), text) {
			return false
		}
	}
	if o.Match.MinServiceItems > 0 && serviceItemCount < o.Match.MinServiceItems {
		return false
	}
	if o.Match.MaxServiceItems > 0 && serviceItemCount > o.Match.MaxServiceItems {
		return false
	}
	if len(o.Match.Categories) > 0 {
		available := map[string]bool{}
		for _, category := range categories {
			available[strings.ToLower(strings.TrimSpace(category))] = true
		}
		hit := false
		for _, category := range o.Match.Categories {
			if available[strings.ToLower(strings.TrimSpace(category))] {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// SplitOverrideLabel 生成匹配条件与覆盖设置的可读摘要，供列表展示与审计。
func (o SplitOverride) MatchLabel() string {
	parts := []string{}
	if text := strings.TrimSpace(o.Match.CustomerContains); text != "" {
		parts = append(parts, "客户名称包含 "+text)
	}
	if text := strings.TrimSpace(o.Match.ContractContains); text != "" {
		parts = append(parts, "合同号包含 "+text)
	}
	if o.Match.MinServiceItems > 0 || o.Match.MaxServiceItems > 0 {
		switch {
		case o.Match.MinServiceItems > 0 && o.Match.MaxServiceItems > 0:
			parts = append(parts, fmt.Sprintf("合同服务项数 %d~%d", o.Match.MinServiceItems, o.Match.MaxServiceItems))
		case o.Match.MinServiceItems > 0:
			parts = append(parts, fmt.Sprintf("合同服务项数 ≥ %d", o.Match.MinServiceItems))
		default:
			parts = append(parts, fmt.Sprintf("合同服务项数 ≤ %d", o.Match.MaxServiceItems))
		}
	}
	if len(o.Match.Categories) > 0 {
		parts = append(parts, "检测类别包含 "+strings.Join(o.Match.Categories, " / "))
	}
	return strings.Join(parts, " 且 ")
}

// SettingsLabel 生成覆盖设置的可读摘要。
func (o SplitOverride) SettingsLabel() string {
	parts := []string{}
	if o.Settings.DimensionPrimary != nil || o.Settings.DimensionSecondary != nil || o.Settings.DimensionTertiary != nil {
		dims := []string{}
		for _, dim := range []*string{o.Settings.DimensionPrimary, o.Settings.DimensionSecondary, o.Settings.DimensionTertiary} {
			if dim != nil && strings.TrimSpace(*dim) != "" {
				dims = append(dims, SplitDimensionLabel(*dim))
			}
		}
		parts = append(parts, "分组维度 = "+strings.Join(dims, " + "))
	}
	if o.Settings.DefaultStatus != nil {
		parts = append(parts, "默认进入状态 = "+strings.TrimSpace(*o.Settings.DefaultStatus))
	}
	if o.Settings.GenerateRequirementSummary != nil {
		if *o.Settings.GenerateRequirementSummary {
			parts = append(parts, "生成技术要求摘要")
		} else {
			parts = append(parts, "技术要求摘要留空人工填写")
		}
	}
	if o.Settings.RequirementSummaryLocked != nil {
		if *o.Settings.RequirementSummaryLocked {
			parts = append(parts, "技术要求摘要锁定")
		} else {
			parts = append(parts, "技术要求摘要可编辑")
		}
	}
	if o.Settings.MissingRuleAction != nil {
		parts = append(parts, "缺规则处理 = "+missingRuleActionLabel(*o.Settings.MissingRuleAction))
	}
	if o.Settings.ScopeChangeDetection != nil {
		parts = append(parts, "范围变更检测 "+yesNoLabel(*o.Settings.ScopeChangeDetection))
	}
	return strings.Join(parts, "；")
}

func missingRuleActionLabel(value string) string {
	switch strings.TrimSpace(value) {
	case SplitMissingHumanConfirm:
		return "标记待人工确认并通知业务管理员"
	case SplitMissingDefaultRule:
		return "按默认规则生成"
	case SplitMissingSilent:
		return "按默认规则生成（不通知）"
	default:
		return value
	}
}

func yesNoLabel(value bool) string {
	if value {
		return "开启"
	}
	return "关闭"
}
