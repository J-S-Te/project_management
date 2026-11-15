package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// MetricCalendarStore 是组织工作日历、例外和自动确认策略的持久化能力。
// 缺失实现时新自动化能力失败关闭，不复用旧容量事实猜测。
type MetricCalendarStore interface {
	GetMetricCalendarRules(context.Context, string, string) ([]domain.MetricCalendarRule, error)
	SaveMetricCalendarRule(context.Context, platform.ScopeFilter, string, domain.MetricCalendarRuleInput) (domain.MetricCalendarRule, error)
	ListMetricCalendarExceptions(context.Context, string, string, string, string) ([]domain.MetricCalendarException, error)
	SaveMetricCalendarException(context.Context, platform.ScopeFilter, string, domain.MetricCalendarExceptionInput) (domain.MetricCalendarException, error)
	DeleteMetricCalendarException(context.Context, platform.ScopeFilter, string, string, uint64, string) error
	CurrentMetricCapacityVersion(context.Context, string, string, string, string) (uint64, error)
	GetMetricAutoConfirmSetting(context.Context, string) (domain.MetricAutoConfirmSetting, error)
	SetMetricAutoConfirmSetting(context.Context, string, string, domain.MetricAutoConfirmSettingInput) (domain.MetricAutoConfirmSetting, error)
}

func metricCalendarStore(s *Service) (MetricCalendarStore, error) {
	store, ok := s.Repo.(MetricCalendarStore)
	if !ok {
		return nil, ErrProjectSnapshotUnavailable
	}
	return store, nil
}

// authorizeMetricCalendarScope 复用人员/设备容量管理权限；工作日历不新增权限码。
func authorizeMetricCalendarScope(p platform.Principal) (platform.ScopeFilter, error) {
	if p.Has("project.person_capacity.manage") {
		return authorizeProjectScope(p, "project.person_capacity.manage")
	}
	if p.Has("project.equipment_capacity.manage") {
		return authorizeProjectScope(p, "project.equipment_capacity.manage")
	}
	return platform.ScopeFilter{}, ErrForbidden
}

func metricOrgInScope(filter platform.ScopeFilter, organization string) bool {
	return filter.AllowAll || contains(filter.OrganizationIDs, organization)
}

const metricCalendarAnchor = "2026-01-05"

func validateMetricCalendarIntervals(intervals []domain.MetricInterval) error {
	if _, err := domain.ValidateMetricIntervals(metricCalendarAnchor, intervals); err != nil {
		return ValidationError(err.Error())
	}
	return nil
}

// metricNormalizeCalendarIntervals 把星期规则统一存到锚点日，生成时再平移到目标业务日。
func metricNormalizeCalendarIntervals(intervals []domain.MetricInterval) ([]domain.MetricInterval, error) {
	loc := time.FixedZone("Asia/Shanghai", 8*3600)
	out := make([]domain.MetricInterval, 0, len(intervals))
	for _, interval := range intervals {
		start, end := interval.Start.In(loc), interval.End.In(loc)
		if start.Hour()*60+start.Minute() >= end.Hour()*60+end.Minute() {
			return nil, ValidationError("工作时段必须同日且结束晚于开始")
		}
		out = append(out, domain.MetricInterval{
			Start: time.Date(2026, 1, 5, start.Hour(), start.Minute(), 0, 0, loc).UTC(),
			End:   time.Date(2026, 1, 5, end.Hour(), end.Minute(), 0, 0, loc).UTC(),
		})
	}
	if _, err := domain.ValidateMetricIntervals(metricCalendarAnchor, out); err != nil {
		return nil, ValidationError(err.Error())
	}
	return out, nil
}

// metricIntervalsOnDate 把锚点日的工作时段平移到目标北京业务日。
func metricIntervalsOnDate(intervals []domain.MetricInterval, date string) []domain.MetricInterval {
	day, err := time.ParseInLocation("2006-01-02", date, time.FixedZone("Asia/Shanghai", 8*3600))
	if err != nil {
		return nil
	}
	out := make([]domain.MetricInterval, 0, len(intervals))
	for _, interval := range intervals {
		start, end := interval.Start.In(day.Location()), interval.End.In(day.Location())
		out = append(out, domain.MetricInterval{
			Start: time.Date(day.Year(), day.Month(), day.Day(), start.Hour(), start.Minute(), 0, 0, day.Location()).UTC(),
			End:   time.Date(day.Year(), day.Month(), day.Day(), end.Hour(), end.Minute(), 0, 0, day.Location()).UTC(),
		})
	}
	return out
}

// MetricCalendar 读取组织全部工作日规则。
// 参数：ctx/p 为真实读取请求，organization 为组织。
// 返回值：规则列表；无权限、空组织或越界时拒绝。
func (s *Service) MetricCalendar(ctx context.Context, p platform.Principal, organization string) ([]domain.MetricCalendarRule, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if organization == "" || !metricOrgInScope(filter, organization) {
		return nil, ErrForbidden
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return nil, err
	}
	return store.GetMetricCalendarRules(ctx, p.TenantID, organization)
}

// SaveMetricCalendarRule 版本化保存组织某个星期的工作时段。
func (s *Service) SaveMetricCalendarRule(ctx context.Context, p platform.Principal, in domain.MetricCalendarRuleInput) (domain.MetricCalendarRule, error) {
	filter, err := authorizeMetricCalendarScope(p)
	if err != nil {
		return domain.MetricCalendarRule{}, err
	}
	if in.Weekday < 1 || in.Weekday > 7 || !metricKeyValid(in.IdempotencyKey) || len([]rune(in.Note)) > 1000 || strings.TrimSpace(in.Note) == "" || in.OrganizationWitnessID == "" {
		return domain.MetricCalendarRule{}, ErrValidation
	}
	if !metricOrgInScope(filter, in.OrganizationID) {
		return domain.MetricCalendarRule{}, ErrForbidden
	}
	in.Intervals, err = metricNormalizeCalendarIntervals(in.Intervals)
	if err != nil {
		return domain.MetricCalendarRule{}, err
	}
	if err = s.metricVerifyOrganization(ctx, in.OrganizationID, in.OrganizationWitnessID); err != nil {
		return domain.MetricCalendarRule{}, err
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return domain.MetricCalendarRule{}, err
	}
	return store.SaveMetricCalendarRule(ctx, filter, p.UserID, in)
}

// MetricCalendarExceptions 读取组织日期范围内的资源容量例外。
func (s *Service) MetricCalendarExceptions(ctx context.Context, p platform.Principal, organization, from, to string) ([]domain.MetricCalendarException, error) {
	filter, err := authorizeProjectScope(p, "project.read")
	if err != nil {
		return nil, err
	}
	if organization == "" || !metricOrgInScope(filter, organization) || !validMetricDate(from) || !validMetricDate(to) || from > to {
		return nil, ErrValidation
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return nil, err
	}
	return store.ListMetricCalendarExceptions(ctx, p.TenantID, organization, from, to)
}

// SaveMetricCalendarException 保存请假、加班、停机或组织假日例外。
func (s *Service) SaveMetricCalendarException(ctx context.Context, p platform.Principal, in domain.MetricCalendarExceptionInput) (domain.MetricCalendarException, error) {
	permission := "project.person_capacity.manage"
	if in.ResourceType == "EQUIPMENT" {
		permission = "project.equipment_capacity.manage"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return domain.MetricCalendarException{}, err
	}
	kind := strings.ToUpper(strings.TrimSpace(in.Kind))
	if kind != "LEAVE" && kind != "OVERTIME" && kind != "DOWNTIME" && kind != "HOLIDAY" || in.ResourceType != "PERSON" && in.ResourceType != "EQUIPMENT" && in.ResourceType != "ORG" || in.ResourceID == "" || in.OrganizationID == "" || !validMetricDate(in.BusinessDate) || !metricKeyValid(in.IdempotencyKey) || strings.TrimSpace(in.Note) == "" || len([]rune(in.Note)) > 1000 {
		return domain.MetricCalendarException{}, ErrValidation
	}
	if !metricOrgInScope(filter, in.OrganizationID) {
		return domain.MetricCalendarException{}, ErrForbidden
	}
	if err = validateMetricCalendarIntervals(in.Intervals); err != nil {
		return domain.MetricCalendarException{}, err
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return domain.MetricCalendarException{}, err
	}
	return store.SaveMetricCalendarException(ctx, filter, p.UserID, in)
}

// DeleteMetricCalendarException 删除一条组织或资源例外。
func (s *Service) DeleteMetricCalendarException(ctx context.Context, p platform.Principal, id string, expectedVersion uint64, key string) error {
	filter, err := authorizeMetricCalendarScope(p)
	if err != nil {
		return err
	}
	if id == "" || expectedVersion == 0 || !metricKeyValid(key) {
		return ErrValidation
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return err
	}
	return store.DeleteMetricCalendarException(ctx, filter, p.UserID, id, expectedVersion, key)
}

// GenerateMetricCapacities 按组织日历和例外逐日发布明确容量。
// 该命令不是整批数据库事务：每天复用既有容量发布版本与幂等键，结果逐日返回。
func (s *Service) GenerateMetricCapacities(ctx context.Context, p platform.Principal, in domain.MetricCapacityGenerateInput) ([]domain.MetricCapacityGenerateResult, error) {
	permission := "project.person_capacity.manage"
	if in.ResourceType == "EQUIPMENT" {
		permission = "project.equipment_capacity.manage"
	}
	filter, err := authorizeProjectScope(p, permission)
	if err != nil {
		return nil, err
	}
	if in.ResourceType != "PERSON" && in.ResourceType != "EQUIPMENT" || len(in.ResourceIDs) == 0 || len(in.ResourceIDs) > 20 || in.OrganizationID == "" || !metricKeyValid(in.IdempotencyKey) || !validMetricDate(in.From) || !validMetricDate(in.To) || in.From > in.To {
		return nil, ErrValidation
	}
	from, _ := time.Parse("2006-01-02", in.From)
	to, _ := time.Parse("2006-01-02", in.To)
	if to.Sub(from) > 30*24*time.Hour {
		return nil, ValidationError("单次容量生成最多31天")
	}
	days := int(to.Sub(from)/(24*time.Hour)) + 1
	if days*len(in.ResourceIDs) > 200 {
		return nil, ValidationError("单次容量生成最多200个资源日")
	}
	if !metricOrgInScope(filter, in.OrganizationID) {
		return nil, ErrForbidden
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return nil, err
	}
	rules, err := store.GetMetricCalendarRules(ctx, p.TenantID, in.OrganizationID)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, ValidationError("请先配置组织工作日历")
	}
	witness := ""
	for _, rule := range rules {
		if rule.OrganizationWitnessID != "" {
			witness = rule.OrganizationWitnessID
			break
		}
	}
	if witness == "" {
		return nil, ValidationError("组织工作日历缺少组织依据人员，请重新保存日历")
	}
	exceptions, err := store.ListMetricCalendarExceptions(ctx, p.TenantID, in.OrganizationID, in.From, in.To)
	if err != nil {
		return nil, err
	}
	ruleByWeekday := make(map[int]domain.MetricCalendarRule, len(rules))
	for _, rule := range rules {
		ruleByWeekday[rule.Weekday] = rule
	}
	exceptionByKey := make(map[string]domain.MetricCalendarException, len(exceptions))
	for _, exception := range exceptions {
		exceptionByKey[exception.ResourceType+"|"+exception.ResourceID+"|"+exception.BusinessDate] = exception
	}
	results := make([]domain.MetricCapacityGenerateResult, 0, days*len(in.ResourceIDs))
	for _, resourceID := range in.ResourceIDs {
		for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
			date := day.Format("2006-01-02")
			result := domain.MetricCapacityGenerateResult{ResourceType: in.ResourceType, ResourceID: resourceID, BusinessDate: date}
			exception, hasException := exceptionByKey[in.ResourceType+"|"+resourceID+"|"+date]
			orgException, hasOrgException := exceptionByKey["ORG|"+in.OrganizationID+"|"+date]
			var intervals []domain.MetricInterval
			source := ""
			if hasException {
				intervals, source = exception.Intervals, exception.Kind
			} else if hasOrgException {
				intervals, source = orgException.Intervals, orgException.Kind
			} else {
				weekday := int(day.Weekday())
				if weekday == 0 {
					weekday = 7
				}
				rule, ok := ruleByWeekday[weekday]
				if !ok {
					result.Status, result.Message = "SKIPPED", "该星期未配置组织工作日历"
					results = append(results, result)
					continue
				}
				intervals, source = metricIntervalsOnDate(rule.Intervals, date), "CALENDAR"
			}
			minutes, err := domain.ValidateMetricIntervals(date, intervals)
			if err != nil {
				result.Status, result.Message = "FAILED", err.Error()
				results = append(results, result)
				continue
			}
			version, err := store.CurrentMetricCapacityVersion(ctx, p.TenantID, in.ResourceType, resourceID, date)
			if err != nil {
				result.Status, result.Message = "FAILED", "读取当前容量版本失败"
				results = append(results, result)
				continue
			}
			capacity, err := s.PublishMetricCapacity(ctx, p, domain.ResourceCapacityInput{
				ResourceType: in.ResourceType, ResourceID: resourceID, BusinessDate: date,
				OrganizationID: in.OrganizationID, OrganizationWitnessID: witness,
				Intervals: intervals, Note: "自动生成：" + source, ExpectedVersion: version,
				IdempotencyKey: metricGenerationKey(in.IdempotencyKey, in.ResourceType, resourceID, date),
			})
			if err != nil {
				result.Status, result.Message = "FAILED", metricGenerationMessage(err)
				results = append(results, result)
				continue
			}
			result.Status, result.Minutes, result.Capacity = "PUBLISHED", minutes, &capacity
			results = append(results, result)
		}
	}
	return results, nil
}

func validMetricDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func metricGenerationKey(prefix, kind, id, date string) string {
	sum := sha256.Sum256([]byte(prefix + "|" + kind + "|" + id + "|" + date))
	return "gen:" + hex.EncodeToString(sum[:])
}

func metricGenerationMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "容量发布未完成"
	}
	if len([]rune(message)) > 200 {
		return string([]rune(message)[:200])
	}
	return message
}
