package application

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
)

// BIZ-PROJECT-05：现场签到坐标必须与站点主数据做一致性校验，偏离原因不是可选备注。
//
// 修复前 CheckInField 只校验经纬度的数值范围：工程师在任何地方（甚至根本没有到达
// 现场）都能提交签到，偏离原因字段也可能为空，现场证据链因此无法证明"人确实到了
// 站点"。
//
// 现在的判定顺序：
//  1. 服务项关联了站点编码（site_code）→ 回读站点主数据坐标，用球面距离比对；
//  2. 距离超过阈值 → 必须填写偏离原因，否则拒绝（提示实际距离与阈值）；
//  3. 站点主数据缺失/未采集坐标/已停用 → 由 SiteMissingPolicy 决定放行、要求原因或拒绝；
//  4. 服务项没有站点编码（历史数据/自由文本场所）→ 记录 site_check=UNLINKED 且不做距离
//     判定，让历史数据继续可签到，同时留下"未关联主数据、无法核验"的可见痕迹。

const (
	// DefaultFieldCheckInDistanceMeters 是签到坐标与站点主数据坐标之间默认允许的距离（米）。
	// 现场定位精度受建筑遮挡影响，阈值过小会把正常签到判成偏离。
	DefaultFieldCheckInDistanceMeters = 1000

	// SiteMissingPolicyAllow：站点主数据不可用时放行（只记录比对结论）。
	SiteMissingPolicyAllow = "ALLOW"
	// SiteMissingPolicyRequireReason：站点主数据不可用时必须填写偏离原因（默认）。
	SiteMissingPolicyRequireReason = "REQUIRE_REASON"
	// SiteMissingPolicyBlock：站点主数据不可用时直接拒绝签到。
	SiteMissingPolicyBlock = "BLOCK"

	// 签到与站点主数据的比对结论，写入事件载荷供审计与前端提示。
	CheckInSiteWithin        = "WITHIN"
	CheckInSiteDeviated      = "DEVIATED"
	CheckInSiteUnlinked      = "UNLINKED"
	CheckInSiteMissing       = "MISSING"
	CheckInSiteNoCoordinates = "NO_COORDINATES"
	CheckInSiteInactive      = "INACTIVE"
)

// FieldCheckInPolicy 是现场签到与站点主数据一致性校验的策略。
type FieldCheckInPolicy struct {
	// SiteDistanceMeters 是允许的签到距离（米）。<=0 时使用
	// DefaultFieldCheckInDistanceMeters。
	SiteDistanceMeters float64
	// SiteMissingPolicy 是站点主数据缺失时的处理策略：ALLOW / REQUIRE_REASON（默认）/
	// BLOCK。未知取值收敛到 REQUIRE_REASON：无法识别的配置不能当成"不校验"。
	SiteMissingPolicy string
}

// normalize 补齐默认值并收敛未知策略。
func (policy FieldCheckInPolicy) normalize() FieldCheckInPolicy {
	if math.IsNaN(policy.SiteDistanceMeters) || math.IsInf(policy.SiteDistanceMeters, 0) || policy.SiteDistanceMeters <= 0 {
		policy.SiteDistanceMeters = DefaultFieldCheckInDistanceMeters
	}
	switch strings.ToUpper(strings.TrimSpace(policy.SiteMissingPolicy)) {
	case SiteMissingPolicyAllow:
		policy.SiteMissingPolicy = SiteMissingPolicyAllow
	case SiteMissingPolicyBlock:
		policy.SiteMissingPolicy = SiteMissingPolicyBlock
	default:
		policy.SiteMissingPolicy = SiteMissingPolicyRequireReason
	}
	return policy
}

// fieldCheckInAssessment 是一次签到坐标与站点主数据比对的结论。
type fieldCheckInAssessment struct {
	Status         string
	SiteCode       string
	SiteName       string
	DistanceMeters float64
	LimitMeters    float64
}

// hasDistance 判断本次比对是否算出了可比较的距离。
func (a fieldCheckInAssessment) hasDistance() bool {
	return a.Status == CheckInSiteWithin || a.Status == CheckInSiteDeviated
}

// siteLabel 返回提示里可读的站点标识。
func (a fieldCheckInAssessment) siteLabel() string {
	return firstNonEmpty(a.SiteName, a.SiteCode)
}

func (s *Service) fieldCheckInPolicy() FieldCheckInPolicy { return s.FieldCheckIn.normalize() }

// assessCheckInSite 把签到坐标与站点主数据比对。
// 站点台账本身不可达（数据库故障）时返回错误而不是"通过"：无法核验就不放行。
func (s *Service) assessCheckInSite(ctx context.Context, tenantID string, item domain.ServiceItem, latitude, longitude float64) (fieldCheckInAssessment, error) {
	policy := s.fieldCheckInPolicy()
	assessment := fieldCheckInAssessment{
		SiteCode: strings.TrimSpace(item.SiteCode), SiteName: strings.TrimSpace(item.Site),
		LimitMeters: policy.SiteDistanceMeters,
	}
	if assessment.SiteCode == "" {
		assessment.Status = CheckInSiteUnlinked
		return assessment, nil
	}
	repo, ok := s.Repo.(SiteRepository)
	if !ok {
		// 仓储没有站点台账能力（历史实现/测试桩）：按"主数据缺失"处理，交给策略决定。
		assessment.Status = CheckInSiteMissing
		return assessment, nil
	}
	site, err := repo.FindSiteByCode(ctx, tenantID, assessment.SiteCode)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			assessment.Status = CheckInSiteMissing
			return assessment, nil
		}
		return assessment, err
	}
	assessment.SiteName = firstNonEmpty(site.Name, assessment.SiteName)
	if !strings.EqualFold(strings.TrimSpace(site.Status), "ACTIVE") {
		assessment.Status = CheckInSiteInactive
		return assessment, nil
	}
	if !site.HasCoordinates {
		assessment.Status = CheckInSiteNoCoordinates
		return assessment, nil
	}
	assessment.DistanceMeters = haversineMeters(latitude, longitude, site.Latitude, site.Longitude)
	if assessment.DistanceMeters > assessment.LimitMeters {
		assessment.Status = CheckInSiteDeviated
	} else {
		assessment.Status = CheckInSiteWithin
	}
	return assessment, nil
}

// requireCheckInDeviationReason 在偏离或站点无法核验时要求填写偏离原因。
func requireCheckInDeviationReason(assessment fieldCheckInAssessment, reason string, policy FieldCheckInPolicy) error {
	reason = strings.TrimSpace(reason)
	switch assessment.Status {
	case CheckInSiteWithin, CheckInSiteUnlinked:
		return nil
	case CheckInSiteDeviated:
		if reason != "" {
			return nil
		}
		return ValidationError(fmt.Sprintf("签到位置距站点「%s」约 %.0f 米，超出 %.0f 米阈值；请填写偏离原因后再提交",
			assessment.siteLabel(), assessment.DistanceMeters, assessment.LimitMeters))
	case CheckInSiteMissing, CheckInSiteNoCoordinates, CheckInSiteInactive:
		label := missingSiteLabel(assessment.Status)
		switch policy.SiteMissingPolicy {
		case SiteMissingPolicyAllow:
			return nil
		case SiteMissingPolicyBlock:
			return PreconditionError(fmt.Sprintf("无法校验签到位置：站点「%s」%s；请先补全站点主数据与坐标后再签到",
				assessment.siteLabel(), label))
		default:
			if reason != "" {
				return nil
			}
			return ValidationError(fmt.Sprintf("无法校验签到位置：站点「%s」%s；请填写偏离原因后再提交",
				assessment.siteLabel(), label))
		}
	}
	return nil
}

// missingSiteLabel 把站点不可用状态翻译成用户可读原因。
func missingSiteLabel(status string) string {
	switch status {
	case CheckInSiteMissing:
		return "主数据不存在"
	case CheckInSiteNoCoordinates:
		return "尚未采集坐标"
	case CheckInSiteInactive:
		return "已停用"
	default:
		return "无法用于签到校验"
	}
}

// haversineMeters 计算两个经纬度之间的球面距离（米）。
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusMeters = 6371000.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusMeters * math.Asin(math.Sqrt(math.Min(1, math.Max(0, a))))
}
