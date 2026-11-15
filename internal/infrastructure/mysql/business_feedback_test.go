package mysql

import (
	"errors"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// TestReportFeedbackPreservesPrecondition 用途：验证报告状态拒绝说明当前阶段且不误报输入错误。
// 参数：t 为隔离内存数据库测试上下文。
// 返回值：无；状态规则或公开提示不符时失败。
func TestReportFeedbackPreservesPrecondition(t *testing.T) {
	for _, tc := range []struct{ status, phase, want string }{
		{"实施中", "COMPILING", "现场实施完成"},
		{"现场实施完成", "ISSUED", "编制"},
	} {
		t.Run(tc.status+tc.phase, func(t *testing.T) {
			_, db := metricTestDB(t)
			_, item := seedMetricProject(t, db, "SERVICE_ITEM")
			item.Status = tc.status
			err := applyItemEvent(db, &item, domain.DeliveryEvent{
				Type:    application.EventReportStatusUpdated,
				Payload: map[string]any{"phase": tc.phase},
			})
			if !errors.Is(err, application.ErrPrecondition) ||
				!strings.Contains(application.UserMessage(err), tc.want) {
				t.Fatalf("状态拒绝未保留前置原因: %v", err)
			}
		})
	}
}

// TestPreparationFeedbackPreservesEquipmentCause 用途：验证锁内设备失效和占用原因可安全展示。
// 参数：t 为隔离内存数据库测试上下文。
// 返回值：无；设备原因丢失或预留索引被误改时失败。
func TestPreparationFeedbackPreservesEquipmentCause(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "设备失效", true: "并发占用"}[occupied], func(t *testing.T) {
			db := equipmentAtomicityDB(t)
			device := atomicityEquipment(t, db, "tenant", "EQ-1")
			item := atomicityPlan(t, db, "tenant", "待实施", nil)
			want := "重新选择设备"
			if occupied {
				want = "2026-10-20"
				row := equipmentReservationRecord{TenantID: "tenant", ResourceID: "EQ-1",
					ServiceItemID: "other-item", UseDate: *item.PlannedStart,
					WindowStart: *item.PlannedStart, WindowEnd: *item.PlannedEnd, CreatedAt: time.Now()}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Create().Before("gorm:create").Register("equipment-busy", func(tx *gorm.DB) {
					if tx.Statement.Table == "pm_equipment_reservation" {
						tx.AddError(&mysqldriver.MySQLError{Number: 1062})
					}
				}); err != nil {
					t.Fatal(err)
				}
			} else if err := db.Model(&device).Update("status", "DISABLED").Error; err != nil {
				t.Fatal(err)
			}
			event := domain.DeliveryEvent{CreatedAt: time.Now(), Payload: map[string]any{
				"equipment": []domain.PlanResource{{ResourceType: "EQUIPMENT", ResourceID: "EQ-1"}},
			}}
			err := syncEquipmentReservations(db, &item, event)
			if !errors.Is(err, application.ErrResourceConflict) ||
				!strings.Contains(application.UserMessage(err), want) {
				t.Fatalf("设备业务原因不可展示: %v", err)
			}
		})
	}
}
