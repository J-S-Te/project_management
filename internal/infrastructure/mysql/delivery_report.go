package mysql

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateRollbackApproval(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	requestID := stringValue(event.Payload, "request_id")
	if requestID == "" {
		return application.ErrValidation
	}
	var request deliveryEventRecord
	if err := tx.Where("tenant_id=? AND id=? AND service_item_id=? AND event_type=?", item.TenantID, requestID, item.ID, application.EventRollbackRequested).First(&request).Error; err != nil {
		return mapNotFound(err)
	}
	if request.ActorUserID == event.ActorUserID {
		return application.ErrForbidden
	}
	if stringValue(event.Payload, "kind") == "" {
		return application.ErrValidation
	}
	var count int64
	if err := tx.Model(&deliveryEventRecord{}).Where("tenant_id=? AND event_type IN ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.request_id'))=?", item.TenantID, []string{application.EventRollbackApproved, application.EventRollbackRejected, application.EventRollbackWithdrawn}, requestID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return application.ErrConflict
	}
	return nil
}

func validateRollbackWithdrawal(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	requestID := stringValue(event.Payload, "request_id")
	if requestID == "" {
		return application.ErrValidation
	}
	var request deliveryEventRecord
	if err := tx.Where("tenant_id=? AND id=? AND service_item_id=? AND event_type=?", item.TenantID, requestID, item.ID, application.EventRollbackRequested).First(&request).Error; err != nil {
		return mapNotFound(err)
	}
	if request.ActorUserID != event.ActorUserID {
		return application.ErrForbidden
	}
	var count int64
	if err := tx.Model(&deliveryEventRecord{}).Where("tenant_id=? AND event_type IN ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.request_id'))=?", item.TenantID, []string{application.EventRollbackApproved, application.EventRollbackRejected, application.EventRollbackWithdrawn}, requestID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return application.ErrConflict
	}
	return nil
}

func validateReportCorrectionApproval(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	requestID := stringValue(event.Payload, "request_id")
	if requestID == "" {
		return application.ErrValidation
	}
	var request deliveryEventRecord
	if err := tx.Where("tenant_id=? AND id=? AND service_item_id=? AND event_type=?", item.TenantID, requestID, item.ID, application.EventReportCorrectionRequested).First(&request).Error; err != nil {
		return mapNotFound(err)
	}
	if request.ActorUserID == event.ActorUserID {
		return application.ErrForbidden
	}
	var count int64
	if err := tx.Model(&deliveryEventRecord{}).Where("tenant_id=? AND event_type IN ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.request_id'))=?", item.TenantID, []string{application.EventReportCorrectionApproved, application.EventReportCorrectionRejected}, requestID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return application.ErrConflict
	}
	return nil
}

// jsonBytes 把事件载荷里的嵌套结构重新编码成可直接写入 JSON 列的字节；
// 键不存在或值为空时返回 nil，让列保持 NULL。
func jsonBytes(values map[string]any, key string) []byte {
	value, exists := values[key]
	if !exists || value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return nil
	}
	return encoded
}

func rfc3339Time(values map[string]any, key string) *time.Time {
	value := stringValue(values, key)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

// allowedReportPhase 报告从编制到归档的推进序列；每个阶段只允许向后推进。
func allowedReportPhase(phase string) bool {
	return reportPhaseRank(phase) > 0
}
func reportPhaseRank(phase string) int {
	switch strings.ToUpper(strings.TrimSpace(phase)) {
	case "COMPILING":
		return 1
	case "REVIEWED":
		return 2
	case "ISSUED":
		return 3
	case "ARCHIVED":
		return 4
	default:
		return 0
	}
}

func ensureReportRevision(tx *gorm.DB, item *serviceItemRecord, now time.Time) error {
	var count int64
	if err := tx.Model(&reportRevisionRecord{}).
		Where("tenant_id=? AND subject_type='SERVICE_ITEM' AND subject_id=? AND revision=?", item.TenantID, item.ID, item.ReportRevision).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	status := strings.ToUpper(strings.TrimSpace(item.ReportStatus))
	if reportPhaseRank(status) == 0 {
		status = "NONE"
	}
	return tx.Create(&reportRevisionRecord{
		TenantID: item.TenantID, ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID, Revision: item.ReportRevision,
		Status: status, ValidityStatus: "ACTIVE", CreatedAt: now, UpdatedAt: now,
	}).Error
}

func (r *Repository) ListReportRevisions(ctx context.Context, tenantID, itemID string) ([]domain.ReportRevision, error) {
	var rows []reportRevisionRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND subject_type='SERVICE_ITEM' AND subject_id=?", tenantID, itemID).
		Order("revision DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domain.ReportRevision, 0, len(rows))
	for _, row := range rows {
		items = append(items, domain.ReportRevision{
			ID: row.ID, ServiceItemID: row.ServiceItemID, SubjectType: row.SubjectType, SubjectID: row.SubjectID, Revision: row.Revision,
			Status: row.Status, ValidityStatus: row.ValidityStatus,
			CorrectionRequestID: row.CorrectionRequestID, CorrectionReason: row.CorrectionReason,
			FileID: row.FileID, FileName: row.FileName, FileMIME: row.FileMIME,
			FileSize: row.FileSize, FileSHA256: row.FileSHA256,
			PreparedBy: row.PreparedBy, PreparedAt: formatOptionalTime(row.PreparedAt),
			ReviewedBy: row.ReviewedBy, ReviewedAt: formatOptionalTime(row.ReviewedAt),
			IssuedBy: row.IssuedBy, IssuedAt: formatOptionalTime(row.IssuedAt),
			ArchivedBy: row.ArchivedBy, ArchivedAt: formatOptionalTime(row.ArchivedAt),
			InvalidatedBy: row.InvalidatedBy, InvalidatedAt: formatOptionalTime(row.InvalidatedAt),
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return items, nil
}

func (r *Repository) RegisterReportArtifact(ctx context.Context, tenantID, itemID string, revision uint64, input domain.ReportArtifactInput, actor string, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item serviceItemRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, itemID).Take(&item).Error; err != nil {
			return mapNotFound(err)
		}
		if item.ReportRevision != revision || (item.ReportStatus != "COMPILING" && item.ReportStatus != "NONE") {
			return application.PreconditionError("只能为当前处于编制阶段的报告版本登记文件")
		}
		if err := ensureReportRevision(tx, &item, now); err != nil {
			return err
		}
		result := tx.Model(&reportRevisionRecord{}).Where("tenant_id=? AND subject_type='SERVICE_ITEM' AND subject_id=? AND revision=? AND validity_status='ACTIVE'", tenantID, itemID, revision).
			Updates(map[string]any{"file_id": input.FileID, "file_name": input.FileName, "file_mime": input.MIME, "file_size": input.Size, "file_sha256": input.SHA256, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return application.ErrConflict
		}
		return nil
	})
}

func persistEvidenceFiles(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent, kind string) error {
	raw, ok := event.Payload["evidence_files"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var files []domain.ReportArtifactInput
	if err = json.Unmarshal(encoded, &files); err != nil {
		return application.ErrValidation
	}
	for _, file := range files {
		record := evidenceFileRecord{
			TenantID: item.TenantID, ServiceItemID: item.ID, EvidenceKind: kind,
			FileID: file.FileID, FileName: file.FileName, FileMIME: file.MIME,
			FileSize: file.Size, FileSHA256: file.SHA256,
			CreatedBy: event.ActorUserID, CreatedAt: event.CreatedAt,
		}
		if err = tx.Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func lockReportRevision(tx *gorm.DB, item *serviceItemRecord) (reportRevisionRecord, error) {
	if err := ensureReportRevision(tx, item, time.Now().UTC()); err != nil {
		return reportRevisionRecord{}, err
	}
	var revision reportRevisionRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND subject_type='SERVICE_ITEM' AND subject_id=? AND revision=?", item.TenantID, item.ID, item.ReportRevision).
		Take(&revision).Error
	return revision, err
}

func advanceReportRevision(tx *gorm.DB, revision *reportRevisionRecord, phase string, event domain.DeliveryEvent) error {
	actor := strings.TrimSpace(event.ActorUserID)
	if actor == "" {
		return application.ErrValidation
	}
	updates := map[string]any{"status": phase, "updated_at": event.CreatedAt}
	switch phase {
	case "COMPILING":
		updates["prepared_by"], updates["prepared_at"] = actor, event.CreatedAt
	case "REVIEWED":
		if revision.PreparedBy == "" || actor == revision.PreparedBy {
			return application.PreconditionError("报告审核人与编制人必须是不同人员")
		}
		if strings.TrimSpace(revision.FileID) == "" || strings.TrimSpace(revision.FileSHA256) == "" {
			return application.PreconditionError("请先上传并登记当前报告版本文件，再提交审核")
		}
		updates["reviewed_by"], updates["reviewed_at"] = actor, event.CreatedAt
	case "ISSUED":
		if revision.ReviewedBy == "" || actor == revision.PreparedBy || actor == revision.ReviewedBy {
			return application.PreconditionError("报告签发人必须与编制人、审核人不同")
		}
		updates["issued_by"], updates["issued_at"] = actor, event.CreatedAt
	case "ARCHIVED":
		if revision.IssuedBy == "" {
			return application.PreconditionError("报告尚未完成签发，不能归档")
		}
		updates["archived_by"], updates["archived_at"] = actor, event.CreatedAt
	default:
		return application.ErrValidation
	}
	result := tx.Model(&reportRevisionRecord{}).
		Where("id=? AND status=? AND validity_status='ACTIVE'", revision.ID, revision.Status).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func invalidateAndCreateReportRevision(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	current, err := lockReportRevision(tx, item)
	if err != nil {
		return err
	}
	if current.ValidityStatus != "ACTIVE" {
		return application.ErrConflict
	}
	result := tx.Model(&reportRevisionRecord{}).Where("id=? AND validity_status='ACTIVE'", current.ID).
		Updates(map[string]any{"validity_status": "VOID", "invalidated_by": event.ActorUserID, "invalidated_at": event.CreatedAt, "updated_at": event.CreatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return tx.Create(&reportRevisionRecord{
		TenantID: item.TenantID, ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID, Revision: item.ReportRevision + 1,
		Status: "NONE", ValidityStatus: "ACTIVE", CorrectionRequestID: stringValue(event.Payload, "request_id"),
		CorrectionReason: stringValue(event.Payload, "reason"), CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt,
	}).Error
}
