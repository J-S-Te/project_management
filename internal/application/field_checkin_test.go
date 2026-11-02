package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// checkInSiteRepository 同时提供 Repository / DeliveryRepository / SiteRepository 三个能力，
// 让签到用例能同时断言事件载荷与站点主数据比对结果。
type checkInSiteRepository struct {
	capabilityRepository
	item   domain.ServiceItem
	sites  map[string]domain.Site
	events []domain.DeliveryEvent
}

func (r *checkInSiteRepository) GetServiceItem(_ context.Context, _ platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	if r.item.ID != id {
		return domain.ServiceItem{}, ErrNotFound
	}
	return r.item, nil
}

func (r *checkInSiteRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (r *checkInSiteRepository) ListSites(context.Context, string, string) ([]domain.Site, error) {
	sites := make([]domain.Site, 0, len(r.sites))
	for _, site := range r.sites {
		sites = append(sites, site)
	}
	return sites, nil
}

func (r *checkInSiteRepository) UpsertSite(_ context.Context, item domain.Site) (domain.Site, error) {
	if r.sites == nil {
		r.sites = map[string]domain.Site{}
	}
	r.sites[item.SiteCode] = item
	return item, nil
}

func (r *checkInSiteRepository) DeleteSite(context.Context, string, string) error { return nil }

func (r *checkInSiteRepository) FindSiteByCode(_ context.Context, _ string, siteCode string) (domain.Site, error) {
	site, ok := r.sites[strings.TrimSpace(siteCode)]
	if !ok {
		return domain.Site{}, ErrNotFound
	}
	return site, nil
}

// 杭州某站点：30.2741, 120.1551。以下偏移用于构造"站内"和"超距"两类签到。
const (
	hangzhouLatitude  = 30.2741
	hangzhouLongitude = 120.1551
)

func checkInRepository() *checkInSiteRepository {
	return &checkInSiteRepository{
		item: domain.ServiceItem{ID: "SI-FIELD-1", ProjectID: "PJ-FIELD-1", Status: "实施中", SiteCode: "S-HZ", Site: "杭州机房"},
		sites: map[string]domain.Site{
			"S-HZ": {SiteCode: "S-HZ", Name: "杭州机房", Status: "ACTIVE", HasCoordinates: true, Latitude: hangzhouLatitude, Longitude: hangzhouLongitude},
		},
	}
}

func checkInInput(latitude, longitude float64, reason string) domain.FieldCheckInInput {
	return domain.FieldCheckInInput{
		Latitude: latitude, Longitude: longitude, AccuracyMeters: 12.5,
		DeviationReason:   reason,
		ClientOperationID: "checkin-01M2TEST",
		CapturedAt:        time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}
}

// 站点内签到：距离在阈值内，无需偏离原因，事件里要留下比对结论与实际距离。
func TestCheckInWithinSiteDistanceRecordsAssessment(t *testing.T) {
	repository := checkInRepository()
	service := &Service{Repo: repository}
	if err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude+0.001, hangzhouLongitude+0.001, "")); err != nil {
		t.Fatalf("站内签到不应被拒绝: %v", err)
	}
	if len(repository.events) != 1 || repository.events[0].Type != EventFieldCheckedIn {
		t.Fatalf("events = %+v", repository.events)
	}
	payload := repository.events[0].Payload
	if payload["site_check"] != CheckInSiteWithin {
		t.Fatalf("site_check = %v, want %s", payload["site_check"], CheckInSiteWithin)
	}
	if payload["site_code"] != "S-HZ" || payload["site_distance_limit_meters"] != float64(DefaultFieldCheckInDistanceMeters) {
		t.Fatalf("比对结果载荷不完整: %+v", payload)
	}
	if distance, ok := payload["site_distance_meters"].(float64); !ok || distance <= 0 {
		t.Fatalf("应记录实际距离: %+v", payload)
	}
}

// BIZ-PROJECT-05：签到坐标明显超出站点阈值且没有偏离原因 → 拒绝，且不写事件。
func TestCheckInRejectsDistanceWithoutDeviationReason(t *testing.T) {
	repository := checkInRepository()
	service := &Service{Repo: repository}
	err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude+0.05, hangzhouLongitude+0.05, ""))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if message := UserMessage(err); !strings.Contains(message, "偏离原因") || !strings.Contains(message, "米") {
		t.Fatalf("提示应给出实际距离与阈值并索取偏离原因, got %q", message)
	}
	if len(repository.events) != 0 {
		t.Fatalf("被拒绝的签到不得写事件: %+v", repository.events)
	}
}

// 超距但填写了偏离原因：允许签到，并把实际距离与结论留痕。
func TestCheckInAllowsDistanceWithDeviationReason(t *testing.T) {
	repository := checkInRepository()
	service := &Service{Repo: repository}
	if err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude+0.05, hangzhouLongitude+0.05, "客户临时改到临时办公点")); err != nil {
		t.Fatalf("填写偏离原因后应允许签到: %v", err)
	}
	payload := repository.events[0].Payload
	if payload["site_check"] != CheckInSiteDeviated || payload["deviation_reason"] != "客户临时改到临时办公点" {
		t.Fatalf("超距签到载荷 = %+v", payload)
	}
}

// 阈值可配置：默认 1000 米下判为超距的位置，把阈值放大到 10 公里后属于站内。
func TestCheckInDistanceThresholdIsConfigurable(t *testing.T) {
	repository := checkInRepository()
	service := &Service{Repo: repository, FieldCheckIn: FieldCheckInPolicy{SiteDistanceMeters: 10000}}
	if err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude+0.05, hangzhouLongitude+0.05, "")); err != nil {
		t.Fatalf("放宽阈值后应属于站内签到: %v", err)
	}
	if status := repository.events[0].Payload["site_check"]; status != CheckInSiteWithin {
		t.Fatalf("site_check = %v, want %s", status, CheckInSiteWithin)
	}
}

// 站点主数据缺失：默认策略要求偏离原因，填写后放行并标记 MISSING。
func TestCheckInSiteMissingRequiresReasonByDefault(t *testing.T) {
	repository := checkInRepository()
	repository.sites = map[string]domain.Site{}
	service := &Service{Repo: repository}
	principal := reportCorrectionPrincipal("engineer-1", "project.field.execute")

	err := service.CheckInField(context.Background(), principal, repository.item.ID, checkInInput(hangzhouLatitude, hangzhouLongitude, ""))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("站点缺失且无偏离原因时 err = %v, want ErrValidation", err)
	}
	if message := UserMessage(err); !strings.Contains(message, "无法校验签到位置") || !strings.Contains(message, "主数据不存在") {
		t.Fatalf("提示应说明站点缺失, got %q", message)
	}
	if err := service.CheckInField(context.Background(), principal, repository.item.ID, checkInInput(hangzhouLatitude, hangzhouLongitude, "新站点尚未登记")); err != nil {
		t.Fatalf("填写偏离原因后应允许签到: %v", err)
	}
	if status := repository.events[0].Payload["site_check"]; status != CheckInSiteMissing {
		t.Fatalf("site_check = %v, want %s", status, CheckInSiteMissing)
	}
}

// 站点缺失策略 BLOCK：即使写了偏离原因也不放行（项目要求签到必须可核验）。
func TestCheckInSiteMissingBlockPolicyRejects(t *testing.T) {
	repository := checkInRepository()
	repository.sites = map[string]domain.Site{}
	service := &Service{Repo: repository, FieldCheckIn: FieldCheckInPolicy{SiteMissingPolicy: SiteMissingPolicyBlock}}
	err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude, hangzhouLongitude, "新站点尚未登记"))
	if !errors.Is(err, ErrPrecondition) {
		t.Fatalf("err = %v, want ErrPrecondition", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("被拒绝的签到不得写事件: %+v", repository.events)
	}
}

// 站点缺失策略 ALLOW：不拦截，但仍记录"无法核验"的结论。
func TestCheckInSiteMissingAllowPolicyRecordsUnverified(t *testing.T) {
	repository := checkInRepository()
	repository.sites = map[string]domain.Site{}
	service := &Service{Repo: repository, FieldCheckIn: FieldCheckInPolicy{SiteMissingPolicy: SiteMissingPolicyAllow}}
	if err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude, hangzhouLongitude, "")); err != nil {
		t.Fatalf("ALLOW 策略下应允许签到: %v", err)
	}
	if status := repository.events[0].Payload["site_check"]; status != CheckInSiteMissing {
		t.Fatalf("site_check = %v, want %s", status, CheckInSiteMissing)
	}
}

// 站点未采集坐标：默认策略要求偏离原因，填写后放行并标记 NO_COORDINATES。
func TestCheckInSiteWithoutCoordinatesRequiresReason(t *testing.T) {
	repository := checkInRepository()
	repository.sites["S-HZ"] = domain.Site{SiteCode: "S-HZ", Name: "杭州机房", Status: "ACTIVE"}
	service := &Service{Repo: repository}
	principal := reportCorrectionPrincipal("engineer-1", "project.field.execute")
	if err := service.CheckInField(context.Background(), principal, repository.item.ID, checkInInput(hangzhouLatitude, hangzhouLongitude, "")); !errors.Is(err, ErrValidation) {
		t.Fatalf("未采集坐标且无偏离原因时 err = %v, want ErrValidation", err)
	}
	if err := service.CheckInField(context.Background(), principal, repository.item.ID, checkInInput(hangzhouLatitude, hangzhouLongitude, "站点坐标待补采")); err != nil {
		t.Fatalf("填写偏离原因后应允许签到: %v", err)
	}
	if status := repository.events[0].Payload["site_check"]; status != CheckInSiteNoCoordinates {
		t.Fatalf("site_check = %v, want %s", status, CheckInSiteNoCoordinates)
	}
}

// 历史数据兼容：服务项只带自由文本场所、没有站点编码时继续可签到，并标记 UNLINKED。
func TestCheckInWithoutSiteCodeStaysCompatible(t *testing.T) {
	repository := checkInRepository()
	repository.item.SiteCode = ""
	repository.item.Site = "客户自由文本场所"
	service := &Service{Repo: repository}
	if err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude, hangzhouLongitude, "")); err != nil {
		t.Fatalf("未关联站点主数据的历史服务项应可继续签到: %v", err)
	}
	payload := repository.events[0].Payload
	if payload["site_check"] != CheckInSiteUnlinked {
		t.Fatalf("site_check = %v, want %s", payload["site_check"], CheckInSiteUnlinked)
	}
	if _, ok := payload["site_distance_meters"]; ok {
		t.Fatalf("未关联站点时不应伪造距离: %+v", payload)
	}
}

// 停用站点按"主数据不可用"处理：默认要求偏离原因。
func TestCheckInInactiveSiteRequiresReason(t *testing.T) {
	repository := checkInRepository()
	repository.sites["S-HZ"] = domain.Site{SiteCode: "S-HZ", Name: "杭州机房", Status: "DISABLED", HasCoordinates: true, Latitude: hangzhouLatitude, Longitude: hangzhouLongitude}
	service := &Service{Repo: repository}
	err := service.CheckInField(context.Background(), reportCorrectionPrincipal("engineer-1", "project.field.execute"), repository.item.ID,
		checkInInput(hangzhouLatitude, hangzhouLongitude, ""))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if message := UserMessage(err); !strings.Contains(message, "已停用") {
		t.Fatalf("提示应说明站点已停用, got %q", message)
	}
}

// 距离计算口径自检：赤道上一度经度约 111 公里，避免阈值比较被单位错误带偏。
func TestHaversineMetersGraceful(t *testing.T) {
	if got := haversineMeters(0, 0, 0, 1); got < 111000 || got > 112000 {
		t.Fatalf("一度经度在赤道约 111km, got %.0f", got)
	}
	if got := haversineMeters(hangzhouLatitude, hangzhouLongitude, hangzhouLatitude, hangzhouLongitude); got != 0 {
		t.Fatalf("同一个点距离应为 0, got %.3f", got)
	}
}
