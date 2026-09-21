package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ensureEmbeddedPenetrationPackage(tx *gorm.DB, item *serviceItemRecord, actor, idempotencyKey string, now time.Time) error {
	if strings.EqualFold(strings.TrimSpace(item.TestMode), "PENETRATION") || !isEqualProtectionCategory(item.Category) || strings.TrimSpace(item.ProjectManagerID) == "" {
		return nil
	}
	var count int64
	if err := tx.Model(&penetrationWorkPackageRecord{}).Where("tenant_id=? AND parent_service_item_id=?", item.TenantID, item.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	record := penetrationWorkPackageRecord{
		ID: ulid.Make().String(), TenantID: item.TenantID, ProjectID: item.ProjectID, ParentServiceItemID: item.ID,
		DecisionStatus: "PENDING", EngineerIDs: jsonValue([]string{}), ExecutionStatus: "NOT_STARTED",
		ReportStatus: "NONE", Version: 1, CreatedAt: now, UpdatedAt: now, UpdatedBy: actor,
	}
	if err := tx.Create(&record).Error; err != nil {
		if isDuplicateKey(err) {
			return nil
		}
		return err
	}
	return createPenetrationEvent(tx, &record, "WORK_PACKAGE_CREATED", actor, idempotencyKey, map[string]any{"source": "PROJECT_MANAGER_ASSIGNED"}, now)
}

func isEqualProtectionCategory(category string) bool {
	category = strings.ReplaceAll(strings.TrimSpace(category), " ", "")
	return strings.Contains(category, "等保") || strings.Contains(category, "等级保护")
}

func validatePenetrationFieldCompletionGate(tx *gorm.DB, item *serviceItemRecord) error {
	if strings.EqualFold(strings.TrimSpace(item.TestMode), "PENETRATION") || !isEqualProtectionCategory(item.Category) {
		return nil
	}
	var row penetrationWorkPackageRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND parent_service_item_id=?", item.TenantID, item.ID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return penetrationFieldCompletionGate(nil)
		}
		return err
	}
	return penetrationFieldCompletionGate(&row)
}

func penetrationFieldCompletionGate(row *penetrationWorkPackageRecord) error {
	if row == nil {
		return application.PreconditionError("请先在渗透测试专项中记录与客户确认的开展结论")
	}
	if row.DecisionStatus == "NOT_REQUIRED" {
		return nil
	}
	if row.DecisionStatus != "REQUIRED" || row.ExecutionStatus != "COMPLETED" {
		return application.PreconditionError("渗透测试专项尚未形成结论或尚未完成，不能确认父服务项现场完成")
	}
	return nil
}

func validatePenetrationReportGate(tx *gorm.DB, item *serviceItemRecord, parentPhase string) error {
	if strings.EqualFold(strings.TrimSpace(item.TestMode), "PENETRATION") || !isEqualProtectionCategory(item.Category) || (parentPhase != "ISSUED" && parentPhase != "ARCHIVED") {
		return nil
	}
	var row penetrationWorkPackageRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND parent_service_item_id=?", item.TenantID, item.ID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return penetrationReportGate(nil, parentPhase)
		}
		return err
	}
	return penetrationReportGate(&row, parentPhase)
}

func penetrationReportGate(row *penetrationWorkPackageRecord, parentPhase string) error {
	if row == nil {
		return application.PreconditionError("请先处理渗透测试专项，再推进父服务项报告")
	}
	if row.DecisionStatus == "NOT_REQUIRED" {
		return nil
	}
	if parentPhase == "ISSUED" && row.ReportStatus != "ISSUED" && row.ReportStatus != "ARCHIVED" {
		return application.PreconditionError("渗透测试专项报告尚未签发，不能签发父服务项报告")
	}
	if parentPhase == "ARCHIVED" && row.ReportStatus != "ARCHIVED" {
		return application.PreconditionError("渗透测试专项报告尚未归档，不能归档父服务项")
	}
	return nil
}

func (r *Repository) EnsurePenetrationWorkPackage(ctx context.Context, tenantID string, item domain.ServiceItem, actor, idempotencyKey string, now time.Time) (domain.PenetrationWorkPackage, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row serviceItemRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, item.ID).Take(&row).Error; err != nil {
			return mapNotFound(err)
		}
		if item.Version != 0 && row.Version != item.Version {
			return application.ErrConflict
		}
		return ensureEmbeddedPenetrationPackage(tx, &row, actor, idempotencyKey, now)
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, item.ID)
}

func (r *Repository) GetPenetrationWorkPackage(ctx context.Context, tenantID, itemID string) (domain.PenetrationWorkPackage, error) {
	var row penetrationWorkPackageRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND parent_service_item_id=?", tenantID, itemID).Take(&row).Error; err != nil {
		return domain.PenetrationWorkPackage{}, mapNotFound(err)
	}
	return penetrationWorkPackageFromRecord(row), nil
}

func (r *Repository) SavePenetrationDecision(ctx context.Context, tenantID, itemID string, input domain.PenetrationDecisionInput, actor string, now time.Time) (domain.PenetrationWorkPackage, error) {
	err := r.mutatePenetrationPackage(ctx, tenantID, itemID, input.ExpectedVersion, input.IdempotencyKey, "DECISION_RECORDED", actor, input, now, func(tx *gorm.DB, row *penetrationWorkPackageRecord) (map[string]any, error) {
		communicatedAt, err := time.Parse(time.RFC3339, input.CommunicatedAt)
		if err != nil {
			return nil, application.ErrValidation
		}
		updates := map[string]any{
			"decision_status": input.DecisionStatus, "customer_contact": input.CustomerContact,
			"communicated_at": communicatedAt, "communication_summary": input.CommunicationSummary,
			"decision_change_reason": strings.TrimSpace(input.ChangeReason),
		}
		if input.DecisionStatus == "NOT_REQUIRED" {
			updates["execution_status"], updates["report_status"] = "CANCELLED", "NONE"
			updates["planned_start"], updates["planned_end"], updates["engineer_ids"] = nil, nil, jsonValue([]string{})
			updates["auth_doc_no"], updates["auth_start"], updates["auth_end"] = "", nil, nil
			updates["auth_scope"], updates["test_scope"], updates["test_window"] = "", "", ""
			updates["emergency_contact"], updates["rollback_plan"] = "", ""
		} else if row.DecisionStatus == "NOT_REQUIRED" {
			updates["execution_status"], updates["report_status"] = "NOT_STARTED", "NONE"
		}
		return updates, nil
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, itemID)
}

func (r *Repository) SavePenetrationPlan(ctx context.Context, tenantID, itemID string, input domain.PenetrationPlanInput, actor string, now time.Time) (domain.PenetrationWorkPackage, error) {
	err := r.mutatePenetrationPackage(ctx, tenantID, itemID, input.ExpectedVersion, input.IdempotencyKey, "PLAN_SAVED", actor, input, now, func(_ *gorm.DB, row *penetrationWorkPackageRecord) (map[string]any, error) {
		if row.DecisionStatus != "REQUIRED" || (row.ExecutionStatus != "NOT_STARTED" && row.ExecutionStatus != "CANCELLED") {
			return nil, application.PreconditionError("只有已确认开展且尚未开始的专项可以保存计划")
		}
		plannedStart, _ := time.Parse(time.RFC3339, input.PlannedStart)
		plannedEnd, _ := time.Parse(time.RFC3339, input.PlannedEnd)
		authStart, _ := time.Parse(time.RFC3339, input.AuthStart)
		authEnd, _ := time.Parse(time.RFC3339, input.AuthEnd)
		return map[string]any{
			"planned_start": plannedStart, "planned_end": plannedEnd, "engineer_ids": jsonValue(input.EngineerIDs),
			"auth_doc_no": strings.TrimSpace(input.AuthDocNo), "auth_start": authStart, "auth_end": authEnd,
			"auth_scope": strings.TrimSpace(input.AuthScope), "test_scope": strings.TrimSpace(input.TestScope),
			"test_window": strings.TrimSpace(input.TestWindow), "emergency_contact": strings.TrimSpace(input.EmergencyContact),
			"rollback_plan": strings.TrimSpace(input.RollbackPlan), "execution_status": "NOT_STARTED",
		}, nil
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, itemID)
}

func (r *Repository) AdvancePenetrationExecution(ctx context.Context, tenantID, itemID string, input domain.PenetrationExecutionInput, actor string, now time.Time) (domain.PenetrationWorkPackage, error) {
	eventType := "EXECUTION_" + input.Action
	err := r.mutatePenetrationPackage(ctx, tenantID, itemID, input.ExpectedVersion, input.IdempotencyKey, eventType, actor, input, now, func(_ *gorm.DB, row *penetrationWorkPackageRecord) (map[string]any, error) {
		switch input.Action {
		case "START":
			if row.DecisionStatus != "REQUIRED" || row.ExecutionStatus != "NOT_STARTED" || row.PlannedStart == nil || row.AuthStart == nil {
				return nil, application.PreconditionError("专项尚未完成开展确认、授权和计划，不能开始")
			}
			return map[string]any{"execution_status": "IN_PROGRESS"}, nil
		case "COMPLETE":
			if row.ExecutionStatus != "IN_PROGRESS" {
				return nil, application.PreconditionError("只有实施中的专项可以确认测试完成")
			}
			return map[string]any{"execution_status": "COMPLETED"}, nil
		case "CANCEL":
			if row.ExecutionStatus == "COMPLETED" || row.ReportStatus != "NONE" {
				return nil, application.PreconditionError("专项已完成或已进入报告流程，不能取消")
			}
			communicatedAt, err := time.Parse(time.RFC3339, input.CommunicatedAt)
			if err != nil {
				return nil, application.ErrValidation
			}
			return map[string]any{
				"decision_status": "NOT_REQUIRED", "execution_status": "CANCELLED", "customer_contact": strings.TrimSpace(input.CustomerContact),
				"communicated_at": communicatedAt, "communication_summary": strings.TrimSpace(input.CommunicationSummary),
				"decision_change_reason": strings.TrimSpace(input.Reason),
			}, nil
		default:
			return nil, application.ErrValidation
		}
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, itemID)
}

func (r *Repository) AdvancePenetrationReport(ctx context.Context, tenantID, itemID string, input domain.PenetrationReportStatusInput, actor string, now time.Time) (domain.PenetrationWorkPackage, error) {
	err := r.mutatePenetrationPackage(ctx, tenantID, itemID, input.ExpectedVersion, input.IdempotencyKey, "REPORT_STATUS_UPDATED", actor, input, now, func(tx *gorm.DB, row *penetrationWorkPackageRecord) (map[string]any, error) {
		if row.ExecutionStatus != "COMPLETED" {
			return nil, application.PreconditionError("渗透测试尚未完成，不能进入专项报告流程")
		}
		next := map[string]string{"NONE": "DRAFTING", "DRAFTING": "SUBMITTED", "SUBMITTED": "APPROVED", "APPROVED": "ISSUED", "ISSUED": "ARCHIVED"}[row.ReportStatus]
		if next == "" || input.Phase != next {
			return nil, application.ErrValidation
		}
		revision, err := lockPenetrationReportRevision(tx, row, now)
		if err != nil {
			return nil, err
		}
		updates := map[string]any{"status": input.Phase, "updated_at": now}
		switch input.Phase {
		case "DRAFTING":
			updates["prepared_by"], updates["prepared_at"] = actor, now
		case "SUBMITTED":
			if revision.PreparedBy == "" || strings.TrimSpace(revision.FileID) == "" {
				return nil, application.PreconditionError("请先编制并上传专项报告文件")
			}
		case "APPROVED":
			if revision.PreparedBy == "" || actor == revision.PreparedBy {
				return nil, application.PreconditionError("专项报告审核人与编制人必须不同")
			}
			updates["reviewed_by"], updates["reviewed_at"] = actor, now
		case "ISSUED":
			if revision.ReviewedBy == "" || actor == revision.PreparedBy || actor == revision.ReviewedBy {
				return nil, application.PreconditionError("专项报告签发人必须与编制人、审核人不同")
			}
			updates["issued_by"], updates["issued_at"] = actor, now
		case "ARCHIVED":
			if revision.IssuedBy == "" {
				return nil, application.PreconditionError("专项报告尚未签发，不能归档")
			}
			updates["archived_by"], updates["archived_at"] = actor, now
		}
		result := tx.Model(&reportRevisionRecord{}).Where("id=? AND status=?", revision.ID, revision.Status).Updates(updates)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, application.ErrConflict
		}
		return map[string]any{"report_status": input.Phase}, nil
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, itemID)
}

func (r *Repository) RegisterPenetrationReportArtifact(ctx context.Context, tenantID, itemID string, input domain.PenetrationReportArtifactInput, actor string, now time.Time) (domain.PenetrationWorkPackage, error) {
	err := r.mutatePenetrationPackage(ctx, tenantID, itemID, input.ExpectedVersion, input.IdempotencyKey, "REPORT_ARTIFACT_REGISTERED", actor, input, now, func(tx *gorm.DB, row *penetrationWorkPackageRecord) (map[string]any, error) {
		if row.ReportStatus != "DRAFTING" {
			return nil, application.PreconditionError("只有编制中的专项报告可以登记文件")
		}
		revision, err := lockPenetrationReportRevision(tx, row, now)
		if err != nil {
			return nil, err
		}
		result := tx.Model(&reportRevisionRecord{}).Where("id=? AND status='DRAFTING'", revision.ID).Updates(map[string]any{
			"file_id": input.FileID, "file_name": input.FileName, "file_mime": input.MIME,
			"file_size": input.Size, "file_sha256": input.SHA256, "updated_at": now,
		})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, application.ErrConflict
		}
		return map[string]any{}, nil
	})
	if err != nil {
		return domain.PenetrationWorkPackage{}, err
	}
	return r.GetPenetrationWorkPackage(ctx, tenantID, itemID)
}

type penetrationMutation func(*gorm.DB, *penetrationWorkPackageRecord) (map[string]any, error)

func (r *Repository) mutatePenetrationPackage(ctx context.Context, tenantID, itemID string, expectedVersion uint64, idempotencyKey, eventType, actor string, payload any, now time.Time, mutate penetrationMutation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize commands for the same work package before checking the idempotency
		// ledger. A retry that arrives while the first transaction is committing must
		// observe the committed event instead of failing on the now-stale version.
		var row penetrationWorkPackageRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND parent_service_item_id=?", tenantID, itemID).Take(&row).Error; err != nil {
			return mapNotFound(err)
		}
		var prior penetrationWorkPackageEventRecord
		duplicateErr := tx.Where("tenant_id=? AND idempotency_key=?", tenantID, idempotencyKey).Take(&prior).Error
		if duplicateErr == nil {
			if prior.EventType != eventType {
				return application.ConflictError("幂等键已用于其他专项操作")
			}
			if row.ID != prior.WorkPackageID {
				return application.ConflictError("幂等键已用于其他专项工作包")
			}
			return nil
		}
		if !errors.Is(duplicateErr, gorm.ErrRecordNotFound) {
			return duplicateErr
		}
		if expectedVersion == 0 || row.Version != expectedVersion {
			return application.ErrConflict
		}
		updates, err := mutate(tx, &row)
		if err != nil {
			return err
		}
		updates["version"], updates["updated_at"], updates["updated_by"] = gorm.Expr("version + 1"), now, actor
		result := tx.Model(&penetrationWorkPackageRecord{}).Where("tenant_id=? AND id=? AND version=?", tenantID, row.ID, row.Version).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return application.ErrConflict
		}
		return createPenetrationEvent(tx, &row, eventType, actor, idempotencyKey, payload, now)
	})
}

func createPenetrationEvent(tx *gorm.DB, row *penetrationWorkPackageRecord, eventType, actor, idempotencyKey string, payload any, now time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	err = tx.Create(&penetrationWorkPackageEventRecord{
		ID: ulid.Make().String(), TenantID: row.TenantID, WorkPackageID: row.ID, EventType: eventType,
		ActorUserID: actor, IdempotencyKey: idempotencyKey, Payload: encoded, CreatedAt: now,
	}).Error
	if isDuplicateKey(err) {
		return application.ConflictError("幂等键已用于其他专项操作")
	}
	return err
}

func lockPenetrationReportRevision(tx *gorm.DB, row *penetrationWorkPackageRecord, now time.Time) (reportRevisionRecord, error) {
	var revision reportRevisionRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"tenant_id=? AND subject_type='PENETRATION_WORK_PACKAGE' AND subject_id=? AND revision=?",
		row.TenantID, row.ID, row.ReportRevision,
	).Take(&revision).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		revision = reportRevisionRecord{
			TenantID: row.TenantID, ServiceItemID: row.ParentServiceItemID, SubjectType: "PENETRATION_WORK_PACKAGE",
			SubjectID: row.ID, Revision: row.ReportRevision, Status: "NONE", ValidityStatus: "ACTIVE", CreatedAt: now, UpdatedAt: now,
		}
		err = tx.Create(&revision).Error
	}
	return revision, err
}

func (r *Repository) ListPenetrationReportRevisions(ctx context.Context, tenantID, itemID string) ([]domain.ReportRevision, error) {
	var pkg penetrationWorkPackageRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND parent_service_item_id=?", tenantID, itemID).Take(&pkg).Error; err != nil {
		return nil, mapNotFound(err)
	}
	var rows []reportRevisionRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND subject_type='PENETRATION_WORK_PACKAGE' AND subject_id=?", tenantID, pkg.ID).Order("revision DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.ReportRevision, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.ReportRevision{
			ID: row.ID, ServiceItemID: row.ServiceItemID, SubjectType: row.SubjectType, SubjectID: row.SubjectID,
			Revision: row.Revision, Status: row.Status, ValidityStatus: row.ValidityStatus,
			FileID: row.FileID, FileName: row.FileName, FileMIME: row.FileMIME, FileSize: row.FileSize, FileSHA256: row.FileSHA256,
			PreparedBy: row.PreparedBy, PreparedAt: formatOptionalTime(row.PreparedAt), ReviewedBy: row.ReviewedBy,
			ReviewedAt: formatOptionalTime(row.ReviewedAt), IssuedBy: row.IssuedBy, IssuedAt: formatOptionalTime(row.IssuedAt),
			ArchivedBy: row.ArchivedBy, ArchivedAt: formatOptionalTime(row.ArchivedAt),
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (r *Repository) ListPenetrationPersonnelConflicts(ctx context.Context, tenantID, itemID string, engineerIDs []string, start, end time.Time) ([]string, error) {
	if len(engineerIDs) == 0 {
		return nil, nil
	}
	conflicts := make([]string, 0)
	for _, engineerID := range engineerIDs {
		var serviceRows []struct{ ID string }
		err := r.db.WithContext(ctx).Table("pm_service_item").Select("id").Where(
			"tenant_id=? AND id<>? AND archived_at IS NULL AND planned_start IS NOT NULL AND planned_end IS NOT NULL AND planned_start < ? AND planned_end > ? AND JSON_CONTAINS(engineer_ids, JSON_QUOTE(?))",
			tenantID, itemID, end, start, engineerID,
		).Find(&serviceRows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range serviceRows {
			conflicts = append(conflicts, fmt.Sprintf("%s 与服务项 %s 排期重叠", engineerID, row.ID))
		}
		var packageRows []struct{ ParentServiceItemID string }
		err = r.db.WithContext(ctx).Table("pm_penetration_work_package").Select("parent_service_item_id").Where(
			"tenant_id=? AND parent_service_item_id<>? AND decision_status='REQUIRED' AND execution_status IN ('NOT_STARTED','IN_PROGRESS') AND planned_start < ? AND planned_end > ? AND JSON_CONTAINS(engineer_ids, JSON_QUOTE(?))",
			tenantID, itemID, end, start, engineerID,
		).Find(&packageRows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range packageRows {
			conflicts = append(conflicts, fmt.Sprintf("%s 与等保服务项 %s 的渗透专项排期重叠", engineerID, row.ParentServiceItemID))
		}
	}
	return conflicts, nil
}

func (r *Repository) PenetrationWorkPackageStats(ctx context.Context, filter platform.ScopeFilter) (domain.PenetrationWorkPackageStats, error) {
	query := r.db.WithContext(ctx).Table("pm_penetration_work_package package").Joins("JOIN pm_project project ON project.tenant_id=package.tenant_id AND project.id=package.project_id")
	query = applyProjectScope(query, filter, "project")
	var rows []struct {
		DecisionStatus  string
		ExecutionStatus string
		Count           int64
	}
	if err := query.Select("package.decision_status, package.execution_status, COUNT(*) AS count").Group("package.decision_status, package.execution_status").Scan(&rows).Error; err != nil {
		return domain.PenetrationWorkPackageStats{}, err
	}
	var stats domain.PenetrationWorkPackageStats
	for _, row := range rows {
		stats.Total += row.Count
		if row.DecisionStatus == "PENDING" {
			stats.PendingDecision += row.Count
		}
		if row.DecisionStatus == "REQUIRED" {
			stats.Required += row.Count
		}
		if row.ExecutionStatus == "IN_PROGRESS" {
			stats.InProgress += row.Count
		}
		if row.ExecutionStatus == "COMPLETED" {
			stats.Completed += row.Count
		}
	}
	return stats, nil
}

func penetrationWorkPackageFromRecord(row penetrationWorkPackageRecord) domain.PenetrationWorkPackage {
	engineers := []string{}
	_ = json.Unmarshal(row.EngineerIDs, &engineers)
	return domain.PenetrationWorkPackage{
		ID: row.ID, ProjectID: row.ProjectID, ParentServiceItemID: row.ParentServiceItemID,
		DecisionStatus: row.DecisionStatus, CustomerContact: row.CustomerContact,
		CommunicatedAt: formatOptionalTime(row.CommunicatedAt), CommunicationSummary: row.CommunicationSummary,
		DecisionChangeReason: row.DecisionChangeReason, PlannedStart: formatOptionalTime(row.PlannedStart), PlannedEnd: formatOptionalTime(row.PlannedEnd),
		EngineerIDs: engineers, AuthDocNo: row.AuthDocNo, AuthStart: formatOptionalTime(row.AuthStart), AuthEnd: formatOptionalTime(row.AuthEnd),
		AuthScope: row.AuthScope, TestScope: row.TestScope, TestWindow: row.TestWindow, EmergencyContact: row.EmergencyContact,
		RollbackPlan: row.RollbackPlan, ExecutionStatus: row.ExecutionStatus, ReportStatus: row.ReportStatus,
		ReportRevision: row.ReportRevision, Version: row.Version, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339), UpdatedBy: row.UpdatedBy,
	}
}
