package config

import (
	"fmt"
	"strconv"
	"strings"
)

// FieldCheckInConfig 是现场签到与站点主数据一致性校验的策略（BIZ-PROJECT-05）。
// 距离阈值与站点缺失策略必须可配置：不同客户站点的定位精度、站房尺度差异很大，
// 而历史项目可能根本没有站点主数据。
type FieldCheckInConfig struct {
	SiteDistanceMeters float64
	SiteMissingPolicy  string
}

const (
	// 与 application.DefaultFieldCheckInDistanceMeters 保持同一默认值；配置层不引用
	// 应用层类型，避免配置包反向依赖业务包。
	defaultFieldCheckInDistanceMeters = 1000
	// maxFieldCheckInDistanceMeters 限制阈值上限：超过一百公里等于关闭校验。
	maxFieldCheckInDistanceMeters = 100000

	fieldCheckInSiteMissingAllow         = "ALLOW"
	fieldCheckInSiteMissingRequireReason = "REQUIRE_REASON"
	fieldCheckInSiteMissingBlock         = "BLOCK"
)

// loadFieldCheckIn 读取现场签到策略的配置并校验取值范围。
func (c *Config) loadFieldCheckIn() error {
	distance := float64(defaultFieldCheckInDistanceMeters)
	if raw := strings.TrimSpace(env("PROJECT_FIELD_CHECKIN_DISTANCE_METERS", "")); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || parsed <= 0 || parsed > maxFieldCheckInDistanceMeters {
			return fmt.Errorf("PROJECT_FIELD_CHECKIN_DISTANCE_METERS must be a positive number of meters not greater than %d", maxFieldCheckInDistanceMeters)
		}
		distance = parsed
	}
	policy := strings.ToUpper(strings.TrimSpace(env("PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY", fieldCheckInSiteMissingRequireReason)))
	switch policy {
	case fieldCheckInSiteMissingAllow, fieldCheckInSiteMissingRequireReason, fieldCheckInSiteMissingBlock:
	default:
		return fmt.Errorf("PROJECT_FIELD_CHECKIN_SITE_MISSING_POLICY must be ALLOW, REQUIRE_REASON or BLOCK")
	}
	c.FieldCheckIn = FieldCheckInConfig{SiteDistanceMeters: distance, SiteMissingPolicy: policy}
	return nil
}
