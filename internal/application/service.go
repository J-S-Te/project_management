package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	// ErrDuplicateProject 表示同一租户下 (contract, contract_version) 已经有项目：
	// 手动新建项目没有合同激活那样的幂等回读，唯一键 uq_pm_project_contract_version 冲突
	// 会以 MySQL 1062 冒到接口层，若原样兜底就只剩「服务暂不可用」。
	ErrDuplicateProject = errors.New("project already exists for the contract version")
	ErrForbidden        = errors.New("operation is outside the authorized project scope")
	// ErrServiceTimeout 表示同步等待后端工作流在约定时间内没有完成。
	// 前端应提示用户稍后重试，而不是让网关吞掉请求并返回 504。
	ErrServiceTimeout = errors.New("service processing timeout")
	// ErrPersonnelUnavailable 表示项目子系统尚未开通基础平台人员目录集成。
	ErrPersonnelUnavailable = errors.New("platform personnel directory is unavailable")
	// ErrContractUnavailable 表示项目子系统无法通过机器身份读取合同审批结果。
	// 它与浏览器会话无关，应以 503 暴露为可重试的子系统依赖故障。
	ErrContractUnavailable = errors.New("approved contract service is unavailable")
	// ErrTriageUnavailable 表示只读偏差分诊试点未启用或上游暂不可用。它绝不能
	// 阻止正式偏差上报，也不会被降级成一个伪造的低风险结果。
	ErrTriageUnavailable = errors.New("deviation triage service is unavailable")
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
	DeleteRule(context.Context, string, string, int64) (domain.Rule, error)
	Dashboard(context.Context, platform.ScopeFilter) (domain.Dashboard, error)
}

// ProjectServiceCreator keeps manual project creation atomic with its initial
// service-item decomposition. Repositories that do not implement it remain
// compatible with the legacy project-only API.
type ProjectServiceCreator interface {
	CreateProjectWithServiceItems(context.Context, domain.Project, []domain.ServiceItem) error
}

type ExistingContractReferenceCounter interface {
	CountExistingContractReferences(context.Context, string, []platform.ApprovedContract) (int, error)
}

// AvailableApprovedContractFilter removes contract versions that are already
// bound to any project in the tenant. This must use tenant-wide data rather
// than the caller's project data scope, otherwise a restricted user could see
// and select a contract whose project is owned by another team.
type AvailableApprovedContractFilter interface {
	FilterUnreferencedApprovedContracts(context.Context, string, []platform.ApprovedContract) ([]platform.ApprovedContract, error)
}

type Service struct {
	Repo Repository
	// Personnel 是基础平台负责人目录；未开通该集成时为 nil，读取人员会返回
	// ErrPersonnelUnavailable，不影响其余项目功能。
	Personnel platform.OwnerDirectory
	// Notifications 是基础平台统一站内信 outbox；未开通该集成时为 nil，
	// 自动化规则仍会写入派生事件，只是不额外投递站内信。
	Notifications   platform.NotificationPublisher
	Contracts       platform.ApprovedContractVerifier
	EvidenceFiles   platform.EvidenceFileGateway
	DeviationTriage DeviationTriage
	// Logger 可选。派生事件（自动化/预警）是主事件提交后的 best-effort 副作用：
	// 写入失败不会回滚主流程，但必须留下可观测痕迹，否则"配置触发了但没落库"无人知晓。
	Logger *slog.Logger
}

type DeviationTriage interface {
	TriageDeviation(context.Context, domain.DeviationInput) (domain.DeviationTriageResult, error)
}

func (s *Service) TriageDeviation(ctx context.Context, p platform.Principal, input domain.DeviationInput) (domain.DeviationTriageResult, error) {
	if !p.Has("project.deviation.report") {
		return domain.DeviationTriageResult{}, ErrForbidden
	}
	input.Description = strings.TrimSpace(input.Description)
	if input.Description == "" || len(input.Description) > 4000 {
		return domain.DeviationTriageResult{}, ValidationError("偏差描述不能为空且不能超过 4000 个字符")
	}
	if s.DeviationTriage == nil {
		return domain.DeviationTriageResult{}, ErrTriageUnavailable
	}
	result, err := s.DeviationTriage.TriageDeviation(ctx, input)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("deviation triage failed", "error", err)
		}
		return domain.DeviationTriageResult{}, ErrTriageUnavailable
	}
	return result, nil
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

// ListApprovedContracts 通过项目后端的机器身份读取当前租户已审批合同。
// 浏览器只持有项目系统会话，不需要也不应跨子系统复用合同系统 Cookie。
func (s *Service) ListApprovedContracts(ctx context.Context, p platform.Principal, limit int) ([]platform.ApprovedContract, error) {
	if !mayCreateProject(p) {
		return nil, ErrForbidden
	}
	if _, err := authorizeProjectScope(p, "project.create"); err != nil {
		return nil, err
	}
	if s.Contracts == nil {
		return nil, ErrContractUnavailable
	}
	items, err := s.Contracts.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	approved := make([]platform.ApprovedContract, 0, len(items))
	for _, item := range items {
		if item.ID != "" && item.ApprovalPassed {
			approved = append(approved, item)
		}
	}
	filter, ok := s.Repo.(AvailableApprovedContractFilter)
	if !ok {
		return nil, fmt.Errorf("approved contract availability filter is not configured")
	}
	available, err := filter.FilterUnreferencedApprovedContracts(ctx, p.TenantID, approved)
	if err != nil {
		return nil, fmt.Errorf("filter approved contracts already linked to projects: %w", err)
	}
	return available, nil
}

// ListApprovedContractServiceItems returns the authoritative, project-facing
// service scope for one approved contract. The browser never calls Contract
// Management directly and receives no contract text, price, contact or file.
func (s *Service) ListApprovedContractServiceItems(ctx context.Context, p platform.Principal, contractID string) (platform.ApprovedContractServiceCatalog, error) {
	if !mayCreateProject(p) {
		return platform.ApprovedContractServiceCatalog{}, ErrForbidden
	}
	if _, err := authorizeProjectScope(p, "project.create"); err != nil {
		return platform.ApprovedContractServiceCatalog{}, err
	}
	contractID = strings.TrimSpace(contractID)
	if contractID == "" {
		return platform.ApprovedContractServiceCatalog{}, ValidationError("请选择已通过审批的合同")
	}
	if s.Contracts == nil {
		return platform.ApprovedContractServiceCatalog{}, ErrContractUnavailable
	}
	catalog, err := s.Contracts.GetServiceItems(ctx, contractID)
	if err != nil {
		return platform.ApprovedContractServiceCatalog{}, fmt.Errorf("%w: %v", ErrContractUnavailable, err)
	}
	if strings.TrimSpace(catalog.ContractID) != contractID {
		return platform.ApprovedContractServiceCatalog{}, fmt.Errorf("%w: contract service catalog returned mismatched contract", ErrContractUnavailable)
	}
	if catalog.ServiceItems == nil {
		catalog.ServiceItems = []platform.ApprovedContractService{}
	}
	return catalog, nil
}
func (s *Service) Dashboard(ctx context.Context, p platform.Principal) (domain.Dashboard, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return domain.Dashboard{}, err
	}
	result, err := s.Repo.Dashboard(ctx, filter)
	if err != nil {
		return result, err
	}
	// 未知服务项状态会被保守地按最滞后处理，会让项目状态看起来比实际更早期。
	// 这里显式告警，避免"数据异常导致项目状态静默变化"无人发现。
	if result.UnknownStatusItems > 0 && s.Logger != nil {
		s.Logger.Warn("unknown service item statuses detected",
			"tenant_id", p.TenantID, "project_count", result.UnknownStatusItems)
	}
	if mayViewPendingProjectCreation(p) {
		s.populatePendingProjectCreation(ctx, p.TenantID, &result)
	}
	return result, nil
}

// populatePendingProjectCreation enriches the dashboard without making the
// project list depend on Contract Management availability. The boolean field
// lets the UI distinguish a true zero from a temporarily unavailable metric.
func (s *Service) populatePendingProjectCreation(ctx context.Context, tenantID string, result *domain.Dashboard) {
	counter, ok := s.Repo.(ExistingContractReferenceCounter)
	if s.Contracts == nil || !ok {
		return
	}
	const pageSize = 500
	afterID := ""
	pending := 0
	for {
		references, nextAfterID, err := s.Contracts.ListReferences(ctx, afterID, pageSize)
		if err != nil {
			s.logPendingProjectCountError(tenantID, err)
			return
		}
		existing, err := counter.CountExistingContractReferences(ctx, tenantID, references)
		if err != nil {
			s.logPendingProjectCountError(tenantID, err)
			return
		}
		pending += max(0, len(references)-existing)
		if nextAfterID == "" {
			break
		}
		if nextAfterID == afterID {
			s.logPendingProjectCountError(tenantID, errors.New("contract reference cursor did not advance"))
			return
		}
		afterID = nextAfterID
	}
	result.PendingProjectCreation = pending
	result.PendingProjectCreationAvailable = true
}

func (s *Service) logPendingProjectCountError(tenantID string, err error) {
	if s.Logger != nil {
		s.Logger.Warn("pending project contract count unavailable", "tenant_id", tenantID, "error", err)
	}
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
	if !p.Has("project.read") && !p.Has("project.team.assign") && !p.Has("project.execution.assign") && !p.Has("project.resource.manage") {
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

// ruleKindPermissions 返回管理某一类配置可使用的权限码。
// 多类配置共用 domain.Rule 结构、按 kind 分表存储，但职责并不相同：
// 字段级脱敏是安全策略，与拆解/预警/自动化/SLA/标准等运营参数必须分开授权，
// 否则"能配 SLA 的人就能改脱敏"。规则按客户端声明的 kind 选择数据表，
// 因此以 kind 判定权限是可靠的：不声明 permissions 就碰不到脱敏表。
// 资质/能力编码允许设备管理员使用独立最小权限；原有项目规则管理者
// 仍然可管理该目录，保持管理员与技术总监的完整规则配置能力。
func ruleKindPermissions(kind string) []string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "permissions":
		return []string{"project.field_permission.manage"}
	case capabilityCodeRuleKind:
		return []string{"project.capability_code.manage", "project_rule.manage"}
	default:
		return []string{"project_rule.manage"}
	}
}

func (s *Service) CreateProject(ctx context.Context, p platform.Principal, input domain.Project) (domain.Project, error) {
	return s.CreateProjectWithServiceItems(ctx, p, input, nil)
}

// DuplicateProjectError 把"同一合同版本已有项目"表达成可执行提示：
// 带上已存在项目编号，并指引用拆解调整（走补充协议）而不是重复新建。
func DuplicateProjectError(existingID string) error {
	if strings.TrimSpace(existingID) == "" {
		return ReasonError{Kind: ErrDuplicateProject, Reason: "该合同版本已存在项目，不能重复创建；如需调整服务内容请在「服务项拆解确认」中调整拆解。"}
	}
	return ReasonError{Kind: ErrDuplicateProject, Reason: fmt.Sprintf("该合同版本已存在项目 %s，不能重复创建；如需调整服务内容请在「服务项拆解确认」中调整拆解（走补充协议）。", existingID)}
}

// findExistingProjectByContract 按租户边界查找同合同同版本的项目编号（空表示不存在）。
// 用租户边界而不是调用者数据范围：唯一键冲突本身是租户级的，范围过滤会让预检漏判、
// 随后仍然撞库并报 500。
func (s *Service) findExistingProjectByContract(ctx context.Context, tenantID, contract, version string) (string, error) {
	deliveryRepo, ok := s.Repo.(DeliveryRepository)
	if !ok || strings.TrimSpace(contract) == "" {
		return "", nil
	}
	existing, err := deliveryRepo.FindProjectByContractVersion(ctx, platform.ScopeFilter{TenantID: tenantID, AllowAll: true}, contract, version)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return existing.ID, nil
}

func (s *Service) CreateProjectWithServiceItems(ctx context.Context, p platform.Principal, input domain.Project, requested []domain.ContractService) (domain.Project, error) {
	if !mayCreateProject(p) {
		return input, ErrForbidden
	}
	filter, err := authorizeProjectScope(p, "project.create")
	if err != nil {
		return input, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return input, ErrValidation
	}
	input.ContractID = strings.TrimSpace(input.ContractID)
	if input.ContractID == "" {
		return input, ValidationError("请选择已通过审批的合同")
	}
	if len(requested) == 0 {
		return input, ValidationError("项目至少需要一个服务项")
	}
	if s.Contracts == nil {
		return input, ErrContractUnavailable
	}
	approved, approvalErr := s.Contracts.Get(ctx, input.ContractID)
	if approvalErr != nil {
		return input, fmt.Errorf("%w: %v", ErrContractUnavailable, approvalErr)
	}
	if !approved.ApprovalPassed {
		return input, PreconditionError("所选合同尚未通过审批")
	}
	if strings.TrimSpace(approved.ID) == "" || strings.TrimSpace(approved.ID) != input.ContractID {
		return input, fmt.Errorf("%w: contract approval service returned mismatched contract", ErrContractUnavailable)
	}
	if strings.TrimSpace(approved.Number) == "" || strings.TrimSpace(approved.CustomerName) == "" {
		return input, PreconditionError("所选合同缺少合同编号或客户信息，请先在合同管理系统中补全")
	}
	catalog, catalogErr := s.Contracts.GetServiceItems(ctx, input.ContractID)
	if catalogErr != nil {
		return input, fmt.Errorf("%w: %v", ErrContractUnavailable, catalogErr)
	}
	if strings.TrimSpace(catalog.ContractID) != input.ContractID || catalog.ContractVersion != approved.Version {
		return input, PreconditionError("合同服务范围已发生变化，请重新选择合同后再提交")
	}
	requested, err = approvedContractServices(requested, catalog.ServiceItems)
	if err != nil {
		return input, err
	}
	categories := make([]string, 0, len(requested))
	for _, item := range requested {
		categories = append(categories, item.Category)
	}
	if err := s.validateControlledDetectionCategories(ctx, p.TenantID, categories); err != nil {
		return input, err
	}
	// 合同编号、版本与客户资料只能来自合同管理系统。浏览器提交的同名字段即使被
	// 篡改也不会落库，避免项目台账与已审批合同产生无法审计的偏差。
	input.ContractID = strings.TrimSpace(approved.ID)
	input.Contract = strings.TrimSpace(approved.Number)
	input.Customer = strings.TrimSpace(approved.CustomerName)
	input.CustomerID = strings.TrimSpace(approved.CustomerID)
	input.ContractVersion = strconv.FormatUint(approved.Version, 10)
	// 提前判重：唯一键只会以 MySQL 1062 的形式暴露，落到接口就是 500「服务暂不可用」，
	// 用户完全不知道是自己重复建了项目。这里先查一次，给出带项目编号的可执行提示。
	if existingID, findErr := s.findExistingProjectByContract(ctx, p.TenantID, strings.TrimSpace(input.Contract), strings.TrimSpace(input.ContractVersion)); findErr != nil {
		return input, findErr
	} else if existingID != "" {
		return input, DuplicateProjectError(existingID)
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
	// 状态、进度与补充协议标记由服务端派生，不接受调用方指定：
	// 否则客户端可以伪造进度百分比，或用 supplement_status=REQUIRED 把新项目直接钉进
	// 不可逆的「补充协议处理中」分支。创建一律从待拆解确认、零进度、非补充协议开始。
	input.Status = "待拆解确认"
	input.Progress = 0
	input.SupplementStatus = "NONE"
	if input.Team == "" {
		input.Team = "未分配"
	}
	if input.Manager == "" {
		input.Manager = "—"
	}
	input.CreatedAt, input.UpdatedAt = now, now
	// 手动创建的项目一律从「待确认」开始，不套用拆解规则的自动放行：拆解规则服务于
	// 「合同激活 / 拆解调整」这类由系统生成的清单（常规批次可跳过人工确认），而手动清单
	// 是业务管理员逐条录入的，必须走「服务项拆解确认」——它同时也是特殊方法项进入技术总监
	// 复核窗口（tech_review_status=PENDING）的唯一入口，跳过它会让渗透测试项无法复核、
	// 无法发布实施计划，项目就此卡死。
	items := make([]domain.ServiceItem, 0, len(requested))
	for index, source := range requested {
		mode := strings.ToUpper(firstNonEmpty(source.TestMode, "STANDARD"))
		if strings.TrimSpace(source.Site) == "" || mode != "STANDARD" && mode != "PENETRATION" {
			return input, ErrValidation
		}
		source.Site, source.SiteCode, err = s.resolveActiveSite(ctx, p.TenantID, source.Site, source.SiteCode)
		if err != nil {
			return input, err
		}
		items = append(items, domain.ServiceItem{
			TenantID: p.TenantID, ID: fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(input.ID, "PJ-"), index+1),
			ProjectID: input.ID, SourceServiceID: firstNonEmpty(source.SourceID, fmt.Sprintf("MANUAL-%03d", index+1)),
			Batch: strings.TrimSpace(source.Batch), Site: strings.TrimSpace(source.Site), SiteCode: strings.TrimSpace(source.SiteCode), Category: strings.TrimSpace(source.Category),
			Requirement: strings.TrimSpace(source.Requirement), System: strings.TrimSpace(source.System), SystemLevel: strings.TrimSpace(source.SystemLevel), Special: yesNo(mode == "PENETRATION"), TestMode: mode,
			Status: "待确认", ConflictStatus: "UNCHECKED",
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

// approvedContractServices resolves browser selections against the latest
// approved contract catalog. Only source_id and the project-specific site
// supplement are accepted from the browser; contract-owned fields are rebuilt
// from the authoritative catalog to prevent scope spoofing.
func approvedContractServices(requested []domain.ContractService, catalog []platform.ApprovedContractService) ([]domain.ContractService, error) {
	byID := make(map[string]platform.ApprovedContractService, len(catalog))
	for _, item := range catalog {
		sourceID := strings.TrimSpace(item.SourceID)
		if sourceID == "" || byID[sourceID].SourceID != "" {
			return nil, fmt.Errorf("%w: contract service catalog contains invalid source identifiers", ErrContractUnavailable)
		}
		item.SourceID = sourceID
		byID[sourceID] = item
	}
	result := make([]domain.ContractService, 0, len(requested))
	selected := make(map[string]bool, len(requested))
	for _, selection := range requested {
		sourceID := strings.TrimSpace(selection.SourceID)
		item, ok := byID[sourceID]
		if sourceID == "" || !ok {
			return nil, ValidationError("所选服务项不属于当前合同，请刷新合同服务范围后重试")
		}
		if selected[sourceID] {
			return nil, ValidationError("同一合同服务项不能重复关联")
		}
		selected[sourceID] = true
		site := firstNonEmpty(selection.Site, item.Site)
		if strings.TrimSpace(site) == "" {
			return nil, ValidationError("请填写所选服务项的实施场所")
		}
		mode := strings.ToUpper(strings.TrimSpace(item.TestMode))
		if mode != "STANDARD" && mode != "PENETRATION" {
			return nil, fmt.Errorf("%w: contract service catalog contains invalid test mode", ErrContractUnavailable)
		}
		result = append(result, domain.ContractService{
			SourceID: sourceID, Name: strings.TrimSpace(item.Name), Site: strings.TrimSpace(site),
			Batch: strings.TrimSpace(item.Batch), Category: strings.TrimSpace(item.Category),
			System: strings.TrimSpace(item.System), SystemLevel: strings.TrimSpace(item.SystemLevel),
			Requirement: strings.TrimSpace(item.Requirement), TestMode: mode,
		})
	}
	return result, nil
}

// resolveActiveSite keeps implementation locations as free text for current clients. Legacy
// callers may still send a site code; when they do, preserve the historical relationship only
// after validating that it names an active row in the same tenant.
func (s *Service) resolveActiveSite(ctx context.Context, tenantID, name, code string) (string, string, error) {
	name, code = strings.TrimSpace(name), strings.TrimSpace(code)
	if code == "" {
		return name, "", nil
	}
	repo, err := s.siteRepo()
	if err != nil {
		return "", "", ValidationError("站点台账不可用")
	}
	site, err := repo.FindSiteByCode(ctx, tenantID, code)
	if err != nil || site.Status != "ACTIVE" {
		return "", "", ValidationError("所选站点不存在或已停用")
	}
	return site.Name, site.SiteCode, nil
}

// mayCreateProject 将项目创建限定到超级管理员（admin）和业务管理员。权限码仍是
// 第一层防线；角色判断拒绝被误授 project.create 的其他岗位，防止目录漂移把高影响
// 的项目创建动作扩散给非业务角色。
func mayCreateProject(p platform.Principal) bool {
	if !p.Has("project.create") {
		return false
	}
	for _, role := range p.Roles {
		switch strings.TrimSpace(role) {
		case "admin", "business_admin":
			return true
		}
	}
	return false
}

// mayViewPendingProjectCreation separates management visibility from the high-impact
// project creation action. Technical directors need the tenant-wide backlog metric for
// delivery governance, but must not gain project.create or the approved-contract picker.
func mayViewPendingProjectCreation(p platform.Principal) bool {
	for _, role := range p.Roles {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "admin", "business_admin", "technical_director":
			return true
		}
	}
	return false
}

func (s *Service) ConfirmServiceItems(ctx context.Context, p platform.Principal, ids []string) ([]domain.ServiceItem, error) {
	filter, err := authorizeProjectScope(p, "service_item.confirm")
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrValidation
	}
	// 范围变更检测先于确认执行：拆解结果与合同清单不一致时进入补充协议分支并中止本次确认，
	// 避免"范围变了却直接确认"把差异带进下游。
	if err := s.CheckScopeChange(ctx, p, filter, ids); err != nil {
		return nil, err
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
	if err := requireAnyApplicationAuthorization(p, ruleKindPermissions(input.Kind)...); err != nil {
		return input, err
	}
	if err := s.normalizeAndValidateRule(ctx, p, &input, 0, false); err != nil {
		return input, err
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	return s.Repo.CreateRule(ctx, input)
}

// UpdateRule 整行更新配置。载荷覆盖该 kind 的专属字段与启停开关。
func (s *Service) UpdateRule(ctx context.Context, p platform.Principal, id int64, input domain.Rule) (domain.Rule, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.ID = id
	if err := requireAnyApplicationAuthorization(p, ruleKindPermissions(input.Kind)...); err != nil {
		return domain.Rule{}, err
	}
	if err := s.normalizeAndValidateRule(ctx, p, &input, id, true); err != nil {
		return domain.Rule{}, err
	}
	input.TenantID = p.TenantID
	input.UpdatedBy = p.UserID
	input.Updated = time.Now().Format("2006-01-02 15:04")
	return s.Repo.UpdateRule(ctx, p.TenantID, strings.TrimSpace(input.Kind), id, input)
}

func (s *Service) SetRuleEnabled(ctx context.Context, p platform.Principal, kind string, id int64, enabled bool) (domain.Rule, error) {
	kind = strings.TrimSpace(kind)
	if err := requireAnyApplicationAuthorization(p, ruleKindPermissions(kind)...); err != nil {
		return domain.Rule{}, err
	}
	if enabled {
		rules, err := s.Repo.ListRules(ctx, p.TenantID, kind)
		if err != nil {
			return domain.Rule{}, err
		}
		var target *domain.Rule
		for index := range rules {
			if rules[index].ID == id {
				target = &rules[index]
				break
			}
		}
		if target == nil {
			return domain.Rule{}, ErrNotFound
		}
		if err := s.normalizeAndValidateRule(ctx, p, target, id, false); err != nil {
			return domain.Rule{}, err
		}
		// 重新启用既是一次规则写入：统一校验可能把历史值规范化（例如事件码大小写、
		// 角色/状态两端空白和旧阈值格式），必须连同启用状态整行持久化，不能只更新
		// enabled 后让“校验通过但运行时仍匹配不到”的旧值留在数据库。
		target.Enabled = true
		target.TenantID = p.TenantID
		target.UpdatedBy = p.UserID
		target.Updated = time.Now().Format("2006-01-02 15:04")
		return s.Repo.UpdateRule(ctx, p.TenantID, kind, id, *target)
	}
	return s.Repo.SetRuleEnabled(ctx, p.TenantID, kind, id, enabled, p.UserID)
}

// DeleteRule 删除一条配置规则。kind 必填：各类配置分别成表，只有 kind 才能确定目标表，
// 缺省「全部类型」的语义只适用于查询。删除同样按 kind 判权限（字段级权限走
// project.field_permission.manage），与新建、编辑保持一致，避免「能建不能删」。
func (s *Service) DeleteRule(ctx context.Context, p platform.Principal, kind string, id int64) (domain.Rule, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return domain.Rule{}, ValidationError("规则类型不能为空")
	}
	if err := requireAnyApplicationAuthorization(p, ruleKindPermissions(kind)...); err != nil {
		return domain.Rule{}, err
	}
	if id <= 0 {
		return domain.Rule{}, ValidationError("规则编号不合法")
	}
	if kind == capabilityCodeRuleKind {
		rules, err := s.Repo.ListRules(ctx, p.TenantID, kind)
		if err != nil {
			return domain.Rule{}, err
		}
		var target *domain.Rule
		for index := range rules {
			if rules[index].ID == id {
				target = &rules[index]
				break
			}
		}
		if target == nil {
			return domain.Rule{}, ErrNotFound
		}
		references, ok := s.Repo.(CapabilityCodeReferenceRepository)
		if !ok {
			// 无法确认引用关系时失败关闭，不能冒险删除可能仍被业务快照使用的编码。
			return domain.Rule{}, errors.New("capability code reference repository unavailable")
		}
		count, err := references.CountCapabilityCodeReferences(ctx, p.TenantID, target.CheckType, target.Scope)
		if err != nil {
			return domain.Rule{}, err
		}
		if count > 0 {
			return domain.Rule{}, ConflictError("该编码已被能力档案、项目服务项或检测类别引用，不能删除；如需停止新业务使用，请改为禁用。")
		}
	}
	return s.Repo.DeleteRule(ctx, p.TenantID, kind, id)
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

func authorizeAnyProjectScope(p platform.Principal, permissions ...string) (platform.ScopeFilter, error) {
	for _, permission := range permissions {
		if p.Has(permission) {
			return authorizeProjectScope(p, permission)
		}
	}
	return platform.ScopeFilter{}, ErrForbidden
}

func requireApplicationAuthorization(p platform.Principal, permission string) error {
	if !p.Has(permission) || !p.HasFullDataScope() {
		return ErrForbidden
	}
	return nil
}

func requireAnyApplicationAuthorization(p platform.Principal, permissions ...string) error {
	if !p.HasFullDataScope() {
		return ErrForbidden
	}
	for _, permission := range permissions {
		if p.Has(permission) {
			return nil
		}
	}
	return ErrForbidden
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
