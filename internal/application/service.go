package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

var (
	ErrNotFound   = errors.New("resource not found")
	ErrValidation = errors.New("validation failed")
	ErrConflict   = errors.New("resource state conflict")
	// ErrDuplicateContract 表示同一租户下 (contract_id, contract_version) 已被并发请求创建。
	// 调用方应按幂等处理：回读已存在的项目并同步盖章状态，而不是把唯一键冲突暴露成 500。
	ErrDuplicateContract = errors.New("contract version already activated")
	ErrForbidden         = errors.New("operation is outside the authorized project scope")
	// ErrServiceTimeout 表示同步等待后端工作流在约定时间内没有完成。
	// 前端应提示用户稍后重试，而不是让网关吞掉请求并返回 504。
	ErrServiceTimeout = errors.New("service processing timeout")
	// ErrPersonnelUnavailable 表示项目子系统尚未开通基础平台人员目录集成。
	ErrPersonnelUnavailable = errors.New("platform personnel directory is unavailable")
	// ErrPrecondition 表示请求本身合法，但服务项尚未满足该操作的前置状态，
	// 例如未完成执行指派、能力校验未通过、特殊方法未复核。它与 ErrValidation
	// 必须区分：前者要告诉用户"先去哪一步"，后者才提示"检查输入"。
	ErrPrecondition = errors.New("service item precondition is not satisfied")
	// ErrResourceConflict 表示与其他服务项的占用冲突（例如同一台设备的使用时段重叠）。
	// 与 ErrConflict 区分：后者是并发写入导致的状态冲突，前者是可解释、可照着核对的业务占用。
	ErrResourceConflict = errors.New("resource usage conflict")
)

// ReasonError 携带可直接展示给用户的原因说明，并保留底层错误类型，
// 使 errors.Is 仍能匹配 ErrValidation / ErrPrecondition 等语义。
type ReasonError struct {
	Kind   error
	Reason string
}

func (e ReasonError) Error() string { return e.Reason }
func (e ReasonError) Unwrap() error { return e.Kind }

// ValidationError 构造字段级参数错误，并把"哪个字段不合法"一并带给用户。
func ValidationError(reason string) error { return ReasonError{Kind: ErrValidation, Reason: reason} }

// PreconditionError 构造前置状态错误，用于提示用户还缺哪一步。
func PreconditionError(reason string) error {
	return ReasonError{Kind: ErrPrecondition, Reason: reason}
}

// ConflictError 构造资源占用冲突错误，并把占用方与日期一并带给用户。
func ConflictError(reason string) error {
	return ReasonError{Kind: ErrResourceConflict, Reason: reason}
}

// UserMessage 返回 ReasonError 中可直接展示的原因；其他错误返回空串。
func UserMessage(err error) string {
	var reason ReasonError
	if errors.As(err, &reason) {
		return reason.Reason
	}
	return ""
}

type Repository interface {
	ListProjects(context.Context, platform.ScopeFilter, string, string) ([]domain.Project, error)
	GetProject(context.Context, platform.ScopeFilter, string) (domain.Project, error)
	CreateProject(context.Context, domain.Project) error
	ListServiceItems(context.Context, platform.ScopeFilter, string) ([]domain.ServiceItem, error)
	GetServiceItem(context.Context, platform.ScopeFilter, string) (domain.ServiceItem, error)
	ConfirmServiceItems(context.Context, platform.ScopeFilter, []string, string) ([]domain.ServiceItem, error)
	ListRules(context.Context, string, string) ([]domain.Rule, error)
	CreateRule(context.Context, domain.Rule) (domain.Rule, error)
	UpdateRule(context.Context, string, string, int64, domain.Rule) (domain.Rule, error)
	SetRuleEnabled(context.Context, string, string, int64, bool, string) (domain.Rule, error)
	Dashboard(context.Context, platform.ScopeFilter) (domain.Dashboard, error)
}

// ProjectServiceCreator keeps manual project creation atomic with its initial
// service-item decomposition. Repositories that do not implement it remain
// compatible with the legacy project-only API.
type ProjectServiceCreator interface {
	CreateProjectWithServiceItems(context.Context, domain.Project, []domain.ServiceItem) error
}

type Service struct {
	Repo Repository
	// Personnel 是基础平台负责人目录；未开通该集成时为 nil，读取人员会返回
	// ErrPersonnelUnavailable，不影响其余项目功能。
	Personnel platform.OwnerDirectory
	// Logger 可选。派生事件（自动化/预警）是主事件提交后的 best-effort 副作用：
	// 写入失败不会回滚主流程，但必须留下可观测痕迹，否则"配置触发了但没落库"无人知晓。
	Logger *slog.Logger
}

func (s *Service) ListProjects(ctx context.Context, p platform.Principal, q, status string) ([]domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	projects, err := s.Repo.ListProjects(ctx, filter, q, status)
	if err != nil {
		return nil, err
	}
	masked, _, err := s.applyFieldPermissions(ctx, p, projects, nil)
	if err != nil {
		return nil, err
	}
	return masked, nil
}
func (s *Service) GetProject(ctx context.Context, p platform.Principal, id string) (domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.Project{}, err
	}
	project, err := s.Repo.GetProject(ctx, filter, id)
	if err != nil {
		return domain.Project{}, err
	}
	masked, _, err := s.applyFieldPermissions(ctx, p, []domain.Project{project}, nil)
	if err != nil {
		return domain.Project{}, err
	}
	return masked[0], nil
}
func (s *Service) Dashboard(ctx context.Context, p platform.Principal) (domain.Dashboard, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.Dashboard{}, err
	}
	return s.Repo.Dashboard(ctx, filter)
}
func (s *Service) ListServiceItems(ctx context.Context, p platform.Principal, projectID string) ([]domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	items, err := s.Repo.ListServiceItems(ctx, filter, projectID)
	if err != nil {
		return nil, err
	}
	_, masked, err := s.applyFieldPermissions(ctx, p, nil, items)
	if err != nil {
		return nil, err
	}
	return masked, nil
}

// ListPersonnel 从基础平台负责人目录读取可选人员，供服务项操作台选择团队负责人、
// 项目经理和工程师。只有具备分配权限的角色能读取，避免把平台人员清单暴露给纯查看角色。
// roleCodes 非空时只返回在这些应用角色上确有有效授权的人员：团队负责人、项目经理和
// 工程师的下拉据此只列岗位模板继承或直接授权过的候选人，不再把全平台人员都列出来。
// roleOrigins 进一步限定角色授权的来源，用于把候选人收敛到岗位授权模板产生的人。
func (s *Service) ListPersonnel(ctx context.Context, p platform.Principal, keyword, userID string, roleCodes, roleOrigins []string, page, pageSize int) (platform.OwnerDirectoryPage, error) {
	// 人员目录只读，与 /personnel 路由守卫保持一致：project.read 是基线，保留 assign 权限
	// 是为了兼容只授予分配权限的角色定义。
	if !p.Has("project.read") && !p.Has("project.team.assign") && !p.Has("project.execution.assign") {
		return platform.OwnerDirectoryPage{}, ErrForbidden
	}
	if s.Personnel == nil {
		return platform.OwnerDirectoryPage{}, ErrPersonnelUnavailable
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
		pageSize = 50
	}
	result, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{
		Keyword: strings.TrimSpace(keyword), UserID: strings.TrimSpace(userID),
		RoleCodes: normalizeDirectoryCodes(roleCodes), RoleOrigins: normalizeDirectoryCodes(roleOrigins),
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		// 目录不可用时对上层统一暴露“未配置/不可用”，不把平台内部错误细节透给浏览器。
		return platform.OwnerDirectoryPage{}, fmt.Errorf("%w: %v", ErrPersonnelUnavailable, err)
	}
	return result, nil
}

// maximumPersonnelRoleCodes 限制单次查询的角色码与来源数量。下拉最多只需要"团队负责人/
// 项目经理/工程师"这类少量角色，给参数设上限可避免把目录接口变成任意角色枚举入口。
const maximumPersonnelRoleCodes = 8

// normalizeDirectoryCodes 去空、去重并截断过滤值，保持查询串稳定且可预测。
func normalizeDirectoryCodes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
		if len(normalized) == maximumPersonnelRoleCodes {
			break
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// maximumPersonnelNameLookups 限制一次批量解析的人员数量：负责人目录只支持按单个
// user_id 精确查询，必须给子系统的扇出设上限，避免把目录接口当成自由查询入口。
const maximumPersonnelNameLookups = 50

// personnelNameLookupConcurrency 限制对负责人目录的并发查询数，兼顾时延与平台侧压力。
const personnelNameLookupConcurrency = 8

// ResolvePersonnelNames 把服务项里保存的平台 user_id 批量翻译成显示名。
// 团队负责人、项目经理、工程师在界面上必须显示姓名而不是 ULID；目录只支持单个
// user_id 查询，所以在服务端聚合并限制并发，让浏览器一次请求就能拿到全部姓名。
// 单个 ID 解析不到（例如人员已离职）不算错误，界面回落到占位文案；
// 只有整批都失败时才返回 ErrPersonnelUnavailable，让前端明确提示目录不可用。
func (s *Service) ResolvePersonnelNames(ctx context.Context, p platform.Principal, ids []string) (map[string]string, error) {
	if !p.Has("project.read") {
		return nil, ErrForbidden
	}
	if s.Personnel == nil {
		return nil, ErrPersonnelUnavailable
	}
	wanted := normalizePersonnelIDs(ids)
	if len(wanted) == 0 {
		return map[string]string{}, nil
	}
	lookup := s.lookupPersonnel(ctx, wanted)
	if len(lookup.failures) == len(wanted) {
		return nil, fmt.Errorf("%w: owner directory lookup failed", ErrPersonnelUnavailable)
	}
	return lookup.names, nil
}

// personnelLookup 是一次批量目录查询的结果。names 是确认存在的人员，
// failures 是目录本身报错的 ID——它与"查无此人"是两件事，调用方必须区分。
type personnelLookup struct {
	names    map[string]string
	failures map[string]struct{}
}

// normalizePersonnelIDs 去空白、去重并施加一次查询的数量上限。
func normalizePersonnelIDs(ids []string) []string {
	wanted := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		wanted = append(wanted, id)
		if len(wanted) >= maximumPersonnelNameLookups {
			break
		}
	}
	return wanted
}

// lookupPersonnel 并发查询负责人目录，逐个 ID 精确匹配。目录只支持单个 user_id 查询，
// 因此在服务端聚合并限制并发，让浏览器一次请求就能拿到结果。
func (s *Service) lookupPersonnel(ctx context.Context, ids []string) personnelLookup {
	result := personnelLookup{names: make(map[string]string, len(ids)), failures: make(map[string]struct{})}
	if s.Personnel == nil || len(ids) == 0 {
		return result
	}
	var (
		mutex  sync.Mutex
		group  sync.WaitGroup
		tokens = make(chan struct{}, personnelNameLookupConcurrency)
	)
	for _, userID := range ids {
		group.Add(1)
		go func(target string) {
			defer group.Done()
			tokens <- struct{}{}
			defer func() { <-tokens }()
			page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: target, Page: 1, PageSize: 1})
			mutex.Lock()
			defer mutex.Unlock()
			if err != nil {
				result.failures[target] = struct{}{}
				return
			}
			for _, item := range page.Items {
				if strings.TrimSpace(item.UserID) == target && strings.TrimSpace(item.DisplayName) != "" {
					result.names[target] = strings.TrimSpace(item.DisplayName)
					return
				}
			}
		}(userID)
	}
	group.Wait()
	return result
}

func (s *Service) ListRules(ctx context.Context, p platform.Principal, kind string) ([]domain.Rule, error) {
	if err := requireApplicationAuthorization(p, "project.read"); err != nil {
		return nil, err
	}
	return s.Repo.ListRules(ctx, p.TenantID, kind)
}

// ruleKindPermission 返回管理某一类配置所需的权限码。
// 六类配置共用 domain.Rule 结构、按 kind 分表存储，但职责并不相同：
// 字段级脱敏是安全策略，与拆解/预警/自动化/SLA/标准等运营参数必须分开授权，
// 否则"能配 SLA 的人就能改脱敏"。规则按客户端声明的 kind 选择数据表，
// 因此以 kind 判定权限是可靠的：不声明 permissions 就碰不到脱敏表。
func ruleKindPermission(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "permissions") {
		return "project.field_permission.manage"
	}
	return "project_rule.manage"
}

func (s *Service) CreateProject(ctx context.Context, p platform.Principal, input domain.Project) (domain.Project, error) {
	return s.CreateProjectWithServiceItems(ctx, p, input, nil)
}

func (s *Service) CreateProjectWithServiceItems(ctx context.Context, p platform.Principal, input domain.Project, requested []domain.ContractService) (domain.Project, error) {
	filter, err := authorizeProjectScope(p, "project.create")
	if err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Customer) == "" || strings.TrimSpace(input.Contract) == "" {
		return input, ErrValidation
	}
	now := time.Now().UTC()
	input.ID = "PJ-" + now.Format("2006") + "-" + strings.ToUpper(ulid.Make().String()[20:])
	input.TenantID = p.TenantID
	input.OwnerIdentityID = p.IdentityID
	if input.OwnerIdentityID == "" {
		input.OwnerIdentityID = p.UserID
	}
	if !filter.AllowAll {
		if input.OwnerOrgID != "" && !contains(filter.OrganizationIDs, input.OwnerOrgID) {
			return input, ErrForbidden
		}
		if input.OwnerOrgID == "" && len(filter.OrganizationIDs) == 1 {
			input.OwnerOrgID = filter.OrganizationIDs[0]
		}
		if !filter.AllowSelf && input.OwnerOrgID == "" {
			return input, ErrForbidden
		}
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Customer = strings.TrimSpace(input.Customer)
	input.Contract = strings.TrimSpace(input.Contract)
	if input.Status == "" {
		input.Status = "待拆解确认"
	}
	if input.Team == "" {
		input.Team = "未分配"
	}
	if input.Manager == "" {
		input.Manager = "—"
	}
	input.CreatedAt, input.UpdatedAt = now, now
	if len(requested) == 0 {
		if err := s.Repo.CreateProject(ctx, input); err != nil {
			return input, err
		}
		return input, nil
	}
	// 拆解规则执行语义统一由 splitRuleItemStatus 实现：存在启用规则时，未命中任何
	// 规则的常规批次自动确认（Status=待分配，跳过人工确认），命中规则范围的批次保留
	// 待确认以便重点复核；未配置规则时全部待确认。
	splitRules, err := s.Repo.ListRules(ctx, p.TenantID, "split-rules")
	if err != nil {
		return input, err
	}
	items := make([]domain.ServiceItem, 0, len(requested))
	for index, source := range requested {
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if strings.TrimSpace(source.Site) == "" || mode != "STANDARD" && mode != "PENETRATION" {
			return input, ErrValidation
		}
		// 与合同激活、拆解调整共用同一套规则语义，避免三条入口各自演化。
		itemStatus := splitRuleItemStatus(splitRules, source)
		items = append(items, domain.ServiceItem{
			TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(input.ID, "PJ-"), index+1),
			ProjectID: input.ID, SourceServiceID: firstNonEmpty(source.SourceID, fmt.Sprintf("MANUAL-%03d", index+1)),
			Batch: strings.TrimSpace(source.Batch), Site: strings.TrimSpace(source.Site), Category: strings.TrimSpace(source.Category),
			Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System), SystemLevel: strings.TrimSpace(source.SystemLevel), Special: yesNo(mode == "PENETRATION"), TestMode: mode,
			Status: itemStatus, ConflictStatus: "UNCHECKED",
		})
	}
	input.Services = len(items)
	if creator, ok := s.Repo.(ProjectServiceCreator); ok {
		if err := creator.CreateProjectWithServiceItems(ctx, input, items); err != nil {
			return input, err
		}
		return input, nil
	}
	return input, errors.New("project repository does not support atomic service-item creation")
}

func (s *Service) ConfirmServiceItems(ctx context.Context, p platform.Principal, ids []string) ([]domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, "service_item.confirm")
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrValidation
	}
	// 确认拆解是纯事务性行更新，直接走仓储层加锁事务；不再同步等待 Temporal 工作流，
	// 避免 Worker 未就绪或活动重试把请求打成网关 504。状态前置校验与数据范围过滤
	// 都在事务行锁内执行（repository.ConfirmServiceItems）。
	return s.Repo.ConfirmServiceItems(ctx, filter, ids, p.UserID)
}

func (s *Service) CreateRule(ctx context.Context, p platform.Principal, input domain.Rule) (domain.Rule, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Kind == "" {
		input.Kind = "split-rules"
	}
	if err := requireApplicationAuthorization(p, ruleKindPermission(input.Kind)); err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return input, ErrValidation
	}
	switch input.Kind {
	case "split-rules":
		if strings.TrimSpace(input.Scope) == "" {
			return input, ErrValidation
		}
	case "warning-rules":
		if strings.TrimSpace(input.CheckType) == "" {
			return input, ErrValidation
		}
	case "automations":
		if strings.TrimSpace(input.Trigger) == "" || strings.TrimSpace(input.Target) == "" {
			return input, ErrValidation
		}
	case "permissions":
		if strings.TrimSpace(input.RoleCode) == "" || strings.TrimSpace(input.FieldName) == "" {
			return input, ErrValidation
		}
	case "sla":
		if strings.TrimSpace(input.Status) == "" || input.DeadlineHours <= 0 {
			return input, ErrValidation
		}
	case "standards":
		if strings.TrimSpace(input.Scope) == "" {
			return input, ErrValidation
		}
	default:
		return input, ErrValidation
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	if input.AccessLevel == "" {
		input.AccessLevel = "view"
	}
	return s.Repo.CreateRule(ctx, input)
}

// UpdateRule 整行更新配置。载荷覆盖该 kind 的专属字段与启停开关。
func (s *Service) UpdateRule(ctx context.Context, p platform.Principal, id int64, input domain.Rule) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, ruleKindPermission(input.Kind)); err != nil {
		return domain.Rule{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return domain.Rule{}, ErrValidation
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	return s.Repo.UpdateRule(ctx, p.TenantID, strings.TrimSpace(input.Kind), id, input)
}

func (s *Service) SetRuleEnabled(ctx context.Context, p platform.Principal, kind string, id int64, enabled bool) (domain.Rule, error) {
	if err := requireApplicationAuthorization(p, ruleKindPermission(kind)); err != nil {
		return domain.Rule{}, err
	}
	return s.Repo.SetRuleEnabled(ctx, p.TenantID, strings.TrimSpace(kind), id, enabled, p.UserID)
}

func authorizeProjectScope(p platform.Principal, permission string) (platform.ScopeFilter, error) {
	if !p.Has(permission) {
		return platform.ScopeFilter{}, ErrForbidden
	}
	filter, err := p.ProjectScopeFilter()
	if err != nil {
		return platform.ScopeFilter{}, ErrForbidden
	}
	return filter, nil
}

func requireApplicationAuthorization(p platform.Principal, permission string) error {
	if !p.Has(permission) || !p.HasFullDataScope() {
		return ErrForbidden
	}
	return nil
}

// requireDirectoryRead 授权只读目录接口（人员目录、设备、能力码）。这些接口只提供操作台
// 表单的下拉数据源，凡是参与项目工作的角色都要能渲染表单，因此统一以 project.read 为基线；
// 分配、指派、维护等写操作仍由各自的 assign/manage 权限把守。数据范围约束保持不变——
// 组织级范围（包括 ORG）可读，PROJECT/SELF 范围仍禁止，避免跨项目暴露租户级主数据。
func requireDirectoryRead(p platform.Principal, permissions ...string) error {
	if !p.HasOrganizationalScope() {
		return ErrForbidden
	}
	for _, permission := range permissions {
		if p.Has(permission) {
			return nil
		}
	}
	return ErrForbidden
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// hasEnabledSplitRule 判断租户是否配置了至少一条启用的拆解规则。无规则时
// 拆解行为保持历史一致（全部待确认），规则存在后才启用自动确认分支。
func hasEnabledSplitRule(rules []domain.Rule) bool {
	for i := range rules {
		if rules[i].Enabled {
			return true
		}
	}
	return false
}

// matchesSplitRule 按双向包含匹配判断服务项批次/站点/类别是否命中规则适用范围。
// 规则 Scope 是自由文本（如"单批次金额超过 50 万元"），因此用包含关系近似匹配：
// 文本与范围任一方向包含即视为命中，便于把"大额/特殊批次"写进适用范围。
func matchesSplitRule(rules []domain.Rule, texts ...string) bool {
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled {
			continue
		}
		scope := strings.ToLower(strings.TrimSpace(rule.Scope))
		if scope == "" {
			continue
		}
		for _, text := range texts {
			value := strings.ToLower(strings.TrimSpace(text))
			if value == "" {
				continue
			}
			if strings.Contains(value, scope) || strings.Contains(scope, value) {
				return true
			}
		}
	}
	return false
}
