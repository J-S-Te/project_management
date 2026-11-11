package application

import (
	"context"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type MetricFactsRepository interface {
	CurrentMetricReportReviews(context.Context, string, string) ([]domain.ReportReviewRound, error)
	MetricReportReview(context.Context, platform.ScopeFilter, string, string, domain.ReportReviewInput) (domain.ReportReviewRound, error)
	ListMetricReportReviews(context.Context, string, string, int) ([]domain.ReportReviewRound, error)
	PublishMetricCapacity(context.Context, platform.ScopeFilter, string, domain.ResourceCapacityInput) (domain.ResourceCapacity, error)
	CreateMetricUsage(context.Context, platform.ScopeFilter, string, bool, domain.ResourceUsageInput) (domain.ResourceUsage, error)
	MutateMetricUsage(context.Context, platform.ScopeFilter, string, string, string, domain.ResourceUsageDecision) (domain.ResourceUsage, error)
	ListMetricCapacities(context.Context, platform.ScopeFilter, int) ([]domain.ResourceCapacity, error)
	ListMetricUsages(context.Context, platform.ScopeFilter, int) ([]domain.ResourceUsage, error)
	ReadMetricFacts(context.Context, string) (domain.MetricFactsSnapshot, error)
}

// metricRepo 取得具有持久事实能力的仓储，缺失时失败关闭。
// 参数：无。
// 返回值：事实仓储；未接线时返回服务不可用。
func (s *Service) metricRepo() (MetricFactsRepository, error) {
	if s == nil {
		return nil, ErrProjectSnapshotUnavailable
	}
	repo, ok := s.Repo.(MetricFactsRepository)
	if !ok {
		return nil, ErrProjectSnapshotUnavailable
	}
	return repo, nil
}

// metricKeyValid 验证幂等键为有界、无空白的稳定操作号。
// 参数：key 为浏览器生成的操作号。
// 返回值：格式合法时为true。
func metricKeyValid(key string) bool {
	return key != "" && len(key) <= 128 && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "\r\n\t ")
}

// ReviewMetricReport 提交或独立审核主报告及专项的真实轮次。
// 参数：ctx 为请求，p 为真实主体，itemID 为父项，in 为报告版本和决定。
// 返回值：真实轮次；明确权限、项目范围、受控原因或版本不合法时拒绝。
func (s *Service) ReviewMetricReport(ctx context.Context, p platform.Principal, itemID string, in domain.ReportReviewInput) (domain.ReportReviewRound, error) {
	permission := "project.report.prepare"
	if in.Decision != "" {
		permission = "project.report.review"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.ReportReviewRound{}, err
	}
	if in.SubjectType != "SERVICE_ITEM" && in.SubjectType != "PENETRATION_WORK_PACKAGE" || !metricKeyValid(in.IdempotencyKey) || in.ExpectedVersion == 0 {
		return domain.ReportReviewRound{}, ErrValidation
	}
	if in.Decision != "" && in.Decision != "PASSED" && in.Decision != "RETURNED" {
		return domain.ReportReviewRound{}, ErrValidation
	}
	if in.Decision != "" && in.RoundID == "" || len([]rune(in.Note)) > 1000 || in.Decision == "RETURNED" && !domain.MetricReviewReason(in.ReasonCode, in.Note) {
		return domain.ReportReviewRound{}, ValidationError("请选择受控退回原因并填写说明")
	}
	if _, err = s.Repo.GetServiceItem(ctx, filter, itemID); err != nil {
		return domain.ReportReviewRound{}, err
	}
	repo, err := s.metricRepo()
	if err != nil {
		return domain.ReportReviewRound{}, err
	}
	return repo.MetricReportReview(ctx, filter, itemID, p.UserID, in)
}

// MetricReportReviews 在父项范围内查询分页审核轮次，不从旧阶段猜测历史审核。
// 参数：ctx/p 为真实读取请求，itemID 为父服务项，page 为页码。
// 返回值：最多50条审核事实和错误。
func (s *Service) MetricReportReviews(ctx context.Context, p platform.Principal, itemID string, page int) ([]domain.ReportReviewRound, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if page < 1 || page > 10000 {
		return nil, ErrValidation
	}
	if _, err = s.Repo.GetServiceItem(ctx, filter, itemID); err != nil {
		return nil, err
	}
	repo, err := s.metricRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListMetricReportReviews(ctx, p.TenantID, itemID, page)
}

// PublishMetricCapacity 明确发布资源日容量和业务归属组织，独立于历史人事任职。
// 参数：ctx/p 为真实管理请求，in 为资源、可用时段、目录组织见证和业务依据。
// 返回值：冻结容量；权限、组织范围或平台目录不可验证时拒绝。
func (s *Service) PublishMetricCapacity(ctx context.Context, p platform.Principal, in domain.ResourceCapacityInput) (domain.ResourceCapacity, error) {
	permission := "project.person_capacity.manage"
	if in.ResourceType == "EQUIPMENT" {
		permission = "project.equipment_capacity.manage"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.ResourceCapacity{}, err
	}
	if in.ResourceType != "PERSON" && in.ResourceType != "EQUIPMENT" || in.ResourceID == "" || len(in.ResourceID) > 64 || !metricKeyValid(in.IdempotencyKey) || strings.TrimSpace(in.Note) == "" || len([]rune(in.Note)) > 1000 {
		return domain.ResourceCapacity{}, ErrValidation
	}
	if _, err = domain.ValidateMetricIntervals(in.BusinessDate, in.Intervals); err != nil {
		return domain.ResourceCapacity{}, ValidationError(err.Error())
	}
	for index := range in.Intervals {
		in.Intervals[index].Start = in.Intervals[index].Start.UTC()
		in.Intervals[index].End = in.Intervals[index].End.UTC()
	}
	if in.OrganizationID == "" || !filter.AllowAll && !contains(filter.OrganizationIDs, in.OrganizationID) {
		return domain.ResourceCapacity{}, ErrForbidden
	}
	if err = s.metricVerifyOrganization(ctx, in.OrganizationID, in.OrganizationWitnessID); err != nil {
		return domain.ResourceCapacity{}, err
	}
	repo, err := s.metricRepo()
	if err != nil {
		return domain.ResourceCapacity{}, err
	}
	return repo.PublishMetricCapacity(ctx, filter, p.UserID, in)
}

// metricVerifyOrganization 使用受控人员目录核实显式选择的组织，而非信任浏览器文本。
// 参数：ctx 为请求，org 为容量业务组织，witness 为该组织目录中的在职人员标识。
// 返回值：目录返回精确人员及组织才成功，依赖失败或无匹配时关闭写入。
func (s *Service) metricVerifyOrganization(ctx context.Context, org, witness string) error {
	if s.Personnel == nil {
		return ErrPersonnelUnavailable
	}
	if witness == "" || len(witness) > 64 {
		return ValidationError("请选择平台组织目录中的在职人员作为组织依据")
	}
	page, err := s.Personnel.List(ctx, platform.OwnerDirectoryQuery{UserID: witness, Page: 1, PageSize: 1})
	if err != nil {
		return ErrPersonnelUnavailable
	}
	if len(page.Items) != 1 || page.Total != 1 || page.Items[0].UserID != witness {
		return ValidationError("平台组织依据已失效，请重新选择")
	}
	for _, item := range page.Items[0].Organizations {
		if item.OrganizationID == org {
			return nil
		}
	}
	return ValidationError("所选组织不在受控平台目录中")
}

// CreateMetricUsage 在真实项目范围内登记人员或设备的实际时段草稿。
// 参数：ctx/p 为请求，in 为实际时段、说明与证据。
// 返回值：草稿；非法日期、时长、未来事实或越权代登时拒绝。
func (s *Service) CreateMetricUsage(ctx context.Context, p platform.Principal, in domain.ResourceUsageInput) (domain.ResourceUsage, error) {
	filter, err := authorizeProjectScope(p, "project.resource_usage.record")
	if err != nil {
		return domain.ResourceUsage{}, err
	}
	// 通用登记动作不跨资源职责扩权；设备管理员即使登记本人也不能据此登记人员工时。
	personnelDuty := p.Has("project.implementation.plan") || p.Has("project.field.execute") || p.Has("project.person_capacity.manage")
	if in.ResourceType == "PERSON" && !personnelDuty {
		return domain.ResourceUsage{}, ErrForbidden
	}

	if in.ResourceType != "PERSON" && in.ResourceType != "EQUIPMENT" || !metricKeyValid(in.IdempotencyKey) || in.ResourceID == "" || len(in.ResourceID) > 64 || strings.TrimSpace(in.Note) == "" || strings.TrimSpace(in.Evidence) == "" || len([]rune(in.Note)) > 1000 || len([]rune(in.Evidence)) > 1000 {
		return domain.ResourceUsage{}, ErrValidation
	}
	if _, err = domain.ValidateMetricIntervals(in.BusinessDate, []domain.MetricInterval{{Start: in.Start, End: in.End}}); err != nil {
		return domain.ResourceUsage{}, ValidationError(err.Error())
	}
	if in.End.After(time.Now().UTC()) {
		return domain.ResourceUsage{}, ValidationError("实际用量不能登记未来尚未发生的时段")
	}
	repo, err := s.metricRepo()
	if err != nil {
		return domain.ResourceUsage{}, err
	}
	canPlan := p.Has("project.implementation.plan") || in.ResourceType == "EQUIPMENT" && p.Has("project.device.manage")
	return repo.CreateMetricUsage(ctx, filter, p.UserID, canPlan, in)
}

// MutateMetricUsage 按显式动作权限提交、确认、退回或申请取消用量。
// 参数：ctx/p 为请求，id/action 为目标与动作，in 为版本、决定和原因。
// 返回值：真实状态；自审和取消独立复核由同事务再次验证。
func (s *Service) MutateMetricUsage(ctx context.Context, p platform.Principal, id, action string, in domain.ResourceUsageDecision) (domain.ResourceUsage, error) {
	permission := "project.resource_usage.record"
	if action == "DECISION" {
		permission = "project.resource_usage.confirm"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.ResourceUsage{}, err
	}
	if action != "SUBMIT" && action != "DECISION" && action != "CANCEL" || in.ExpectedVersion == 0 || !metricKeyValid(in.IdempotencyKey) || len([]rune(in.Note)) > 1000 {
		return domain.ResourceUsage{}, ErrValidation
	}
	if action == "DECISION" && in.Decision != "CONFIRMED" && in.Decision != "RETURNED" || (action == "DECISION" || action == "CANCEL") && strings.TrimSpace(in.Note) == "" {
		return domain.ResourceUsage{}, ValidationError("请填写决定或更正说明")
	}
	repo, err := s.metricRepo()
	if err != nil {
		return domain.ResourceUsage{}, err
	}
	return repo.MutateMetricUsage(ctx, filter, p.UserID, id, action, in)
}

// MetricResources 只按真实范围分页读取容量或实际用量。
// 参数：ctx/p 为请求，kind 为capacities/usages，page 为页码。
// 返回值：真实记录数组；未知类型、无读取权限或越界时拒绝。
func (s *Service) MetricResources(ctx context.Context, p platform.Principal, kind string, page int) (any, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if page < 1 || page > 10000 {
		return nil, ErrValidation
	}
	repo, err := s.metricRepo()
	if err != nil {
		return nil, err
	}
	if kind == "capacities" {
		return repo.ListMetricCapacities(ctx, filter, page)
	}
	if kind == "usages" {
		return repo.ListMetricUsages(ctx, filter, page)
	}
	return nil, ErrValidation
}

// MetricFacts 仅为已验签的租户全范围机器主体提供有界完整事实快照。
// 参数：ctx 为读取上下文，p 为专用看板机器主体。
// 返回值：提交成功的只读快照；不完整和超限均返回错误。
func (s *Service) MetricFacts(ctx context.Context, p platform.Principal) (domain.MetricFactsSnapshot, error) {
	filter, err := p.ProjectScopeFilter()
	if err != nil || !filter.AllowAll || !p.Has("project.read") || p.TenantID == "" {
		return domain.MetricFactsSnapshot{}, ErrForbidden
	}
	repo, err := s.metricRepo()
	if err != nil {
		return domain.MetricFactsSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return repo.ReadMetricFacts(ctx, p.TenantID)
}

// CurrentMetricReportReviews 读取父项两个报告主体的当前版本最新轮次，与历史分页分离。
// 参数：ctx/p 为真实请求，itemID 为已授权父项。
// 返回值：最多两条当前轮次，未送审主体没有记录；越权或查询失败时返回错误。
func (s *Service) CurrentMetricReportReviews(ctx context.Context, p platform.Principal, itemID string) ([]domain.ReportReviewRound, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.GetServiceItem(ctx, filter, itemID); err != nil {
		return nil, err
	}
	repo, err := s.metricRepo()
	if err != nil {
		return nil, err
	}
	return repo.CurrentMetricReportReviews(ctx, p.TenantID, itemID)
}
