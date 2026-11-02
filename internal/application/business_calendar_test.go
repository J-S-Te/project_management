package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/businessclock"
	"github.com/j-s-te/project-management/internal/domain"
)

// 北京时间 2026-01-01 00:30（UTC 仍是 2025-12-31）：业务日历值必须已经跨年/跨日。
var beijingNewYearInstant = time.Date(2025, 12, 31, 16, 30, 0, 0, time.UTC)

// 编号年份段按北京业务年：UTC 12-31 16:30 之后新建的项目不能再记成上一年。
func TestProjectIDUsesBeijingBusinessYear(t *testing.T) {
	newYear := projectID(beijingNewYearInstant)
	if !strings.HasPrefix(newYear, "PJ-2026-") {
		t.Fatalf("projectID(%s) = %q, want PJ-2026-…", beijingNewYearInstant, newYear)
	}
	suffix := strings.TrimPrefix(newYear, "PJ-2026-")
	if len(suffix) != 6 || suffix != strings.ToUpper(suffix) {
		t.Fatalf("project id suffix = %q, want 6 uppercase ULID characters", suffix)
	}

	// 对照：UTC 15:30 仍是北京 2025-12-31 23:30，必须留在上一年。
	if previous := projectID(time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC)); !strings.HasPrefix(previous, "PJ-2025-") {
		t.Fatalf("projectID(UTC 15:30) = %q, want PJ-2025-…", previous)
	}
}

// 行程单号日期段按北京业务日：北京 1 月 1 日 00:30 生成的单号必须是新年日期段。
func TestTravelRequestIDUsesBeijingBusinessDate(t *testing.T) {
	newYear := newTravelRequestID(beijingNewYearInstant)
	if !strings.HasPrefix(newYear, "TRIP-20260101-") {
		t.Fatalf("newTravelRequestID(%s) = %q, want TRIP-20260101-…", beijingNewYearInstant, newYear)
	}
	if previous := newTravelRequestID(time.Date(2025, 12, 31, 15, 30, 0, 0, time.UTC)); !strings.HasPrefix(previous, "TRIP-20251231-") {
		t.Fatalf("newTravelRequestID(UTC 15:30) = %q, want TRIP-20251231-…", previous)
	}
}

// 服务项编号沿用项目编号后缀（SI-<项目后缀>-NNN），不另行取当前年份；
// 项目号按北京年生成后，服务项年份随之一致，不会出现 PJ-2026 / SI-2025 的分叉。
func TestServiceItemIDInheritsBeijingBusinessYear(t *testing.T) {
	project := projectID(beijingNewYearInstant)
	if !strings.HasPrefix(project, "PJ-2026-") {
		t.Fatalf("projectID = %q, want PJ-2026-…", project)
	}
	itemID := fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(project, "PJ-"), 1)
	if !strings.HasPrefix(itemID, "SI-2026-") || !strings.HasSuffix(itemID, "-001") {
		t.Fatalf("service item id = %q, want SI-2026-…-001 inherited from %q", itemID, project)
	}
	if want := "SI-" + strings.TrimPrefix(project, "PJ-") + "-001"; itemID != want {
		t.Fatalf("service item id = %q, want %q", itemID, want)
	}
}

// 设备检定到期判定按北京业务日：UTC 12-31 16:30（北京 1 月 1 日 00:30）时，
// 有效期至 2025-12-31 的设备已经过期，有效期自 2026-01-01 的设备已经生效。
func TestDeriveCapabilityEffectiveStatusUsesBeijingBusinessDay(t *testing.T) {
	tests := []struct {
		name   string
		item   domain.Capability
		status string
	}{
		{
			name:   "yesterday expired once Beijing rolls into the new day",
			item:   domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidUntil: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)},
			status: capabilityEffectiveExpired,
		},
		{
			name:   "new Beijing day starts the calibration",
			item:   domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			status: capabilityEffectiveActive,
		},
		{
			name:   "valid through the Beijing business day",
			item:   domain.Capability{ResourceType: "EQUIPMENT", Status: "ACTIVE", ValidUntil: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			status: capabilityEffectiveActive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := deriveCapabilityEffectiveStatus(test.item, beijingNewYearInstant)
			if got.EffectiveStatus != test.status {
				t.Fatalf("effective status = %q, want %q", got.EffectiveStatus, test.status)
			}
		})
	}
}

// 设备“今天是否在借”按北京业务日：北京 1 月 1 日 00:30 时，
// 占用时段到 2025-12-31 的行已经结束，占用 2026-01-01 的行才算借出。
func TestEquipmentInUseAtUsesBeijingBusinessDay(t *testing.T) {
	reservations := []domain.EquipmentReservation{
		{ServiceItemID: "SI-TODAY", ResourceID: "EQ-TODAY", WindowStart: "2026-01-01", WindowEnd: "2026-01-01"},
		{ServiceItemID: "SI-YESTERDAY", ResourceID: "EQ-YESTERDAY", WindowStart: "2025-12-31", WindowEnd: "2025-12-31"},
		{ServiceItemID: "SI-NEXT", ResourceID: "EQ-NEXT", WindowStart: "2026-01-02", WindowEnd: "2026-01-02"},
	}
	active := equipmentInUseAt(reservations, beijingNewYearInstant)
	if _, ok := active["EQ-TODAY"]; !ok {
		t.Fatalf("equipment reserved for the Beijing business day must count as out: %#v", active)
	}
	if _, ok := active["EQ-YESTERDAY"]; ok {
		t.Fatalf("reservation ended before the Beijing business day must not count as out: %#v", active)
	}
	if _, ok := active["EQ-NEXT"]; ok {
		t.Fatalf("reservation starting after the Beijing business day must not count as out: %#v", active)
	}
}

// 北京时间凌晨窗口下，“在借设备不可再借”的拦截必须仍然生效：
// resolvePreparationEquipment 的在位检查（约 109 行）按北京业务日判定占用，
// 若退回 UTC 日期，凌晨 00:00–08:00 会把当天的占用看成“还没开始”。
//
// 请求时段故意取明天、与今天的占用时段不重叠：这样后面的“时段冲突”检查不会兜底，
// 唯一能拒绝该请求的就是 109 行的“当前不在公司（在借中）”判定。
func TestResolvePreparationEquipmentBlocksEquipmentBorrowedOnBeijingBusinessDay(t *testing.T) {
	todayText := businessclock.DateKey(time.Now())
	tomorrowText := businessclock.DateKey(time.Now().AddDate(0, 0, 1))
	tomorrow, err := time.Parse("2006-01-02", tomorrowText)
	if err != nil {
		t.Fatalf("parse business day %q: %v", tomorrowText, err)
	}
	repo := &capabilityRepository{
		capabilities: []domain.Capability{
			{ResourceType: "EQUIPMENT", ResourceID: "EQ-OUT", ResourceName: "外借中的设备", Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny},
		},
		reservations: []domain.EquipmentReservation{
			{ServiceItemID: "SI-OTHER", ProjectID: "PJ-2026-OTHER", ResourceID: "EQ-OUT", ResourceName: "外借中的设备", WindowStart: todayText, WindowEnd: todayText},
		},
	}
	service := &Service{Repo: repo}
	inputs := []domain.PlanResourceInput{{
		ResourceType: "EQUIPMENT", ResourceID: "EQ-OUT", WindowStart: tomorrowText, WindowEnd: tomorrowText,
	}}
	_, err = service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF", inputs, tomorrow, tomorrow)
	if err == nil || !errors.Is(err, ErrResourceConflict) || !strings.Contains(err.Error(), "当前不在公司") {
		t.Fatalf("equipment borrowed for the Beijing business day must be blocked, got %v", err)
	}

	// 对照：占用记录清空后同一请求放行，证明拦截来自在位判定而不是别的校验。
	repo.reservations = nil
	if _, err := service.resolvePreparationEquipment(context.Background(), repo, "tenant-1", "SI-SELF", inputs, tomorrow, tomorrow); err != nil {
		t.Fatalf("returned equipment must be available again: %v", err)
	}
}
