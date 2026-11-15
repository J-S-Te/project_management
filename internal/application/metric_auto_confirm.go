package application

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// MetricAutoConfirmSetting 读取租户强证据自动确认策略。
func (s *Service) MetricAutoConfirmSetting(ctx context.Context, p platform.Principal) (domain.MetricAutoConfirmSetting, error) {
	if _, err := authorizeProjectScope(p, "project.resource_usage.confirm"); err != nil {
		return domain.MetricAutoConfirmSetting{}, err
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return domain.MetricAutoConfirmSetting{}, err
	}
	return store.GetMetricAutoConfirmSetting(ctx, p.TenantID)
}

// SetMetricAutoConfirmSetting 以乐观版本保存强证据自动确认策略。
func (s *Service) SetMetricAutoConfirmSetting(ctx context.Context, p platform.Principal, in domain.MetricAutoConfirmSettingInput) (domain.MetricAutoConfirmSetting, error) {
	if _, err := authorizeProjectScope(p, "project.resource_usage.confirm"); err != nil {
		return domain.MetricAutoConfirmSetting{}, err
	}
	if !metricKeyValid(in.IdempotencyKey) {
		return domain.MetricAutoConfirmSetting{}, ErrValidation
	}
	store, err := metricCalendarStore(s)
	if err != nil {
		return domain.MetricAutoConfirmSetting{}, err
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	return store.SetMetricAutoConfirmSetting(ctx, p.TenantID, p.UserID, in)
}
