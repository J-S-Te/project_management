package mysql

import (
	"encoding/json"
	"errors"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// advanceReportRevisionWithMode 在锁内区分经理阶段维护和真实审核推进。
// 参数：tx/item/revision 为锁内事务与对象，phase 为目标阶段，event 为可信交付事件。
// 返回值：手动主体、版本或写入失败时返回错误；严格模式沿用原审核校验。
func advanceReportRevisionWithMode(
	tx *gorm.DB, item *serviceItemRecord, revision *reportRevisionRecord,
	phase string, event domain.DeliveryEvent,
) error {
	if stringValue(event.Payload, "transition_mode") != application.ManualProjectManagerReportMode {
		if err := guardManualReportWorkflow(tx, revision); err != nil {
			return err
		}
		return advanceReportRevision(tx, revision, phase, event)
	}
	if event.ActorUserID == "" || item.ProjectManagerID != event.ActorUserID {
		return application.ErrForbidden
	}
	if expectedVersionValue(event.Payload) == 0 {
		return application.ErrValidation
	}
	// 手动维护只更新阶段，不填写真实编制、独立审核或签发证据。
	result := tx.Model(&reportRevisionRecord{}).
		Where("tenant_id=? AND id=? AND status=? AND validity_status='ACTIVE'",
			item.TenantID, revision.ID, revision.Status).
		Updates(map[string]any{"status": phase, "updated_at": event.CreatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

// guardManualReportWorkflow 阻止真实审核或旧阶段命令接管已采用手动流转的修订。
// 参数：tx 为持有服务项锁的事务，revision 为当前有效报告修订。
// 返回值：该主报告修订已手动流转时返回前置条件错误，查询或解码失败原样返回。
func guardManualReportWorkflow(tx *gorm.DB, revision *reportRevisionRecord) error {
	if revision.SubjectType != "SERVICE_ITEM" {
		return nil
	}
	var latest deliveryEventRecord
	err := tx.Where("tenant_id=? AND service_item_id=? AND event_type=?",
		revision.TenantID, revision.SubjectID, application.EventReportStatusUpdated).
		Order("created_at DESC, id DESC").Take(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var payload struct {
		Mode     string `json:"transition_mode"`
		Revision uint64 `json:"report_revision"`
	}
	if err = json.Unmarshal(latest.Payload, &payload); err != nil {
		return err
	}
	if payload.Mode == application.ManualProjectManagerReportMode && payload.Revision == revision.Revision {
		return application.PreconditionError("当前主报告版本由项目经理手动流转，不能再通过真实审核或旧阶段操作推进")
	}
	return nil
}
