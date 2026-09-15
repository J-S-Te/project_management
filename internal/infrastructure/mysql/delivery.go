package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isDuplicateKey 识别 MySQL 唯一键冲突（错误码 1062），把驱动的底层错误翻译成业务哨兵。
func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func (r *Repository) FindProjectByContractVersion(ctx context.Context, filter platform.ScopeFilter, contractID, version string) (domain.Project, error) {
	var record projectRecord
	err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Where("contract = ? AND contract_version = ?", contractID, version).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	project := projectFromRecord(record)
	inputs, err := r.projectStatusInputs(ctx, filter.TenantID, []string{record.ID})
	if err != nil {
		return domain.Project{}, err
	}
	applyDerivedProjectMetrics(&project, inputs[record.ID])
	return project, nil
}

func (r *Repository) ActivateContract(ctx context.Context, project domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractID: project.ContractID, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
			// 同一 (tenant_id, contract_id, contract_version) 并发激活时，唯一键
			// uq_pm_project_contract_version 会让后到的事务失败；这里翻译成语义哨兵，
			// 由应用层回读已存在项目并按幂等成功返回。
			if isDuplicateKey(err) {
				return application.ErrDuplicateContract
			}
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &project.CreatedAt, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func createNotificationOutbox(tx *gorm.DB, event domain.DeliveryEvent) error {
	if event.Notification == nil || len(event.Notification.Recipients) == 0 {
		return nil
	}
	payload, err := json.Marshal(event.Notification)
	if err != nil {
		return err
	}
	idempotencyKey := strings.TrimSpace(event.Notification.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = event.ID
	}
	return tx.Create(&notificationOutboxRecord{
		EventID: event.ID, TenantID: event.TenantID, EventType: event.Type,
		IdempotencyKey: idempotencyKey,
		AggregateType:  "service_item", AggregateID: event.ServiceItemID,
		Payload: payload, Status: "PENDING", CreatedAt: event.CreatedAt,
	}).Error
}

func (r *Repository) EnqueueNotification(ctx context.Context, tenantID string, message domain.NotificationMessage) (bool, error) {
	payload, err := json.Marshal(message)
	if err != nil {
		return false, err
	}
	idempotencyKey := strings.TrimSpace(message.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = message.EventID
	}
	record := notificationOutboxRecord{EventID: message.EventID, TenantID: tenantID, IdempotencyKey: idempotencyKey, EventType: message.EventType, AggregateType: message.ReferenceType, AggregateID: message.ReferenceID, Payload: payload, Status: "PENDING", CreatedAt: message.OccurredAt}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) ClaimNotificationOutbox(ctx context.Context, workerID string, limit int, now time.Time) ([]domain.NotificationOutboxItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var claimed []notificationOutboxRecord
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status IN ? AND (next_retry_at IS NULL OR next_retry_at <= ?) AND (locked_until IS NULL OR locked_until < ?)", []string{"PENDING", "RETRY_WAIT", "PROCESSING"}, now, now).
			Order("created_at ASC").Limit(limit).Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]uint64, 0, len(claimed))
		for _, row := range claimed {
			ids = append(ids, row.ID)
		}
		return tx.Model(&notificationOutboxRecord{}).Where("id IN ?", ids).Updates(map[string]any{
			"status": "PROCESSING", "locked_by": workerID, "locked_until": now.Add(2 * time.Minute),
		}).Error
	})
	if err != nil {
		return nil, err
	}
	items := make([]domain.NotificationOutboxItem, 0, len(claimed))
	for _, row := range claimed {
		var message domain.NotificationMessage
		if err := json.Unmarshal(row.Payload, &message); err != nil {
			return nil, err
		}
		items = append(items, domain.NotificationOutboxItem{ID: row.ID, Message: message, RetryCount: row.RetryCount})
	}
	return items, nil
}

func (r *Repository) MarkNotificationOutboxSent(ctx context.Context, id uint64, workerID string, now time.Time) error {
	result := r.db.WithContext(ctx).Model(&notificationOutboxRecord{}).
		Where("id=? AND status='PROCESSING' AND locked_by=?", id, workerID).
		Updates(map[string]any{"status": "SENT", "sent_at": now, "locked_by": "", "locked_until": nil, "last_error_summary": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func (r *Repository) MarkNotificationOutboxRetry(ctx context.Context, id uint64, workerID string, next time.Time, dead bool) error {
	status := "RETRY_WAIT"
	if dead {
		status = "DEAD_LETTER"
	}
	result := r.db.WithContext(ctx).Model(&notificationOutboxRecord{}).
		Where("id=? AND status='PROCESSING' AND locked_by=?", id, workerID).
		Updates(map[string]any{"status": status, "retry_count": gorm.Expr("retry_count + 1"), "next_retry_at": next, "locked_by": "", "locked_until": nil})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func (r *Repository) CreateProjectWithServiceItems(ctx context.Context, project domain.Project, items []domain.ServiceItem) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractID: project.ContractID, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
			// 手动创建没有合同激活那样的幂等回读：撞唯一键 uq_pm_project_contract_version
			// 说明同合同同版本已有项目，翻译成语义哨兵交由应用层给出可执行提示，
			// 而不是让 MySQL 1062 一路兜底成 500「服务暂不可用」。
			if isDuplicateKey(err) {
				return application.ErrDuplicateProject
			}
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &project.CreatedAt, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) SyncContractStampStatus(ctx context.Context, project domain.Project, uploaded bool, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest deliveryEventRecord
		err := tx.Where("tenant_id = ? AND project_id = ? AND event_type = ?", project.TenantID, project.ID, application.EventContractStampStatus).Order("created_at DESC").Take(&latest).Error
		if err == nil {
			payload := map[string]any{}
			if json.Unmarshal(latest.Payload, &payload) == nil && payload["stamped_contract_uploaded"] == uploaded {
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func (r *Repository) ApplyDeliveryEvent(ctx context.Context, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if event.Type == application.EventDeviationReviewed {
			deviationID := stringValue(event.Payload, "deviation_id")
			var reported deliveryEventRecord
			if err := tx.Where("tenant_id=? AND event_type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deviation_id'))=?", event.TenantID, application.EventDeviationReported, deviationID).First(&reported).Error; err != nil {
				return mapNotFound(err)
			}
			event.ProjectID, event.ServiceItemID = reported.ProjectID, reported.ServiceItemID
		}
		if event.ServiceItemID != "" {
			var item serviceItemRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", event.TenantID, event.ServiceItemID).First(&item).Error; err != nil {
				return mapNotFound(err)
			}
			event.ProjectID = item.ProjectID
			if err := applyItemEvent(tx, &item, event); err != nil {
				return err
			}
		}
		if event.ProjectID != "" {
			var project projectRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", event.TenantID, event.ProjectID).First(&project).Error; err != nil {
				return mapNotFound(err)
			}
			if err := applyProjectEvent(tx, &project, event); err != nil {
				return err
			}
		}
		// L4：事件落库后把派生项目状态回写 pm_project.status 缓存，与读侧派生口径保持一致。
		// 服务项状态在事务内推进（含直接落库的终止/偏离/现场完成），不再让存储列悄然陈旧。
		if event.ProjectID != "" {
			if err := syncProjectStatusColumn(tx, event.TenantID, event.ProjectID); err != nil {
				return err
			}
		}
		if err := createEvent(tx, event); err != nil {
			return err
		}
		return createNotificationOutbox(tx, event)
	})
}

func applyItemEvent(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	// 先前置读取、再事务行锁之间可能已有其他命令完成。撤销命令把客户端看到的版本
	// 带入事件，在锁内二次比较，避免后到的撤销覆盖最新责任链。
	if expected := expectedVersionValue(event.Payload); expected != 0 && item.Version != expected {
		return application.ErrConflict
	}
	updates := map[string]any{"updated_at": event.CreatedAt, "updated_by": event.ActorUserID}
	switch event.Type {
	case application.EventTeamAssigned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = stringValue(event.Payload, "team_lead_id")
	case application.EventTeamAssignmentRevoked:
		if item.Status != "待分配" || item.TeamLeadID == "" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = ""
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		updates["required_codes"] = jsonValue([]string{})
		updates["conflict_status"] = "UNCHECKED"
	case application.EventDecompositionReturned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		updates["status"] = "待确认"
		updates["team_lead_id"] = ""
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		updates["conflict_status"] = "UNCHECKED"
		updates["tech_review_status"] = "NONE"
		updates["tech_reviewed_at"] = nil
		updates["tech_reviewed_by"] = ""
		updates["tech_review_comment"] = ""
	case application.EventExecutionTeamAssigned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		if item.TeamLeadID == "" {
			return application.ErrValidation
		}
		updates["project_manager_id"] = stringValue(event.Payload, "project_manager_id")
		updates["engineer_ids"] = jsonValue(event.Payload["engineer_ids"])
		updates["equipment_ids"] = jsonValue(event.Payload["equipment_ids"])
		updates["required_codes"] = jsonValue(event.Payload["required_codes"])
		updates["conflict_status"] = stringValue(event.Payload, "conflict_status")
	case application.EventExecutionAssignmentRevoked:
		if item.Status != "待分配" || item.TeamLeadID == "" || item.ProjectManagerID == "" {
			return application.ErrValidation
		}
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		updates["required_codes"] = jsonValue([]string{})
		updates["conflict_status"] = "UNCHECKED"
	case application.EventImplementationPlanned:
		// 与 PlanImplementation 共用同一套前置规则：行锁内复查可覆盖读后状态变化的竞态，
		// 并且仍然返回可执行的原因而不是笼统的参数错误。
		if err := application.CheckImplementationPlanPrecondition(application.PlanPrecondition{
			Status: item.Status, ProjectManagerID: item.ProjectManagerID,
			ConflictStatus: item.ConflictStatus, Special: item.Special, TechReviewStatus: item.TechReviewStatus,
		}); err != nil {
			return err
		}
		if item.TestMode == "PENETRATION" && stringValue(event.Payload, "penetration_test_plan") == "" {
			return application.ValidationError("请填写渗透测试专项计划")
		}
		updates["planned_start"] = rfc3339Value(event.Payload, "planned_start")
		updates["planned_end"] = rfc3339Value(event.Payload, "planned_end")
		updates["status"] = "待实施"
		if err := upsertImplPlan(tx, item, event); err != nil {
			return err
		}
	case application.EventImplementationPlanRevoked:
		if item.Status != "待实施" {
			return application.ErrValidation
		}
		if err := clearImplPlan(tx, item, event, true); err != nil {
			return err
		}
		updates["planned_start"] = nil
		updates["planned_end"] = nil
		updates["status"] = "待分配"
	case application.EventSpecialMethodReviewed:
		if item.Special != "是" {
			return application.ErrValidation
		}
		decision := strings.ToUpper(stringValue(event.Payload, "decision"))
		if decision != "APPROVED" && decision != "REJECTED" {
			return application.ErrValidation
		}
		if item.TechReviewStatus != "PENDING" && item.TechReviewStatus != "REJECTED" {
			return application.ErrValidation
		}
		updates["tech_review_status"] = decision
		updates["tech_reviewed_at"] = event.CreatedAt
		updates["tech_reviewed_by"] = event.ActorUserID
		updates["tech_review_comment"] = stringValue(event.Payload, "comment")
	case application.EventReportStatusUpdated:
		if item.Status != "现场实施完成" {
			return application.ErrValidation
		}
		phase := strings.ToUpper(stringValue(event.Payload, "phase"))
		if !allowedReportPhase(phase) {
			return application.ErrValidation
		}
		current := reportPhaseRank(item.ReportStatus)
		next := reportPhaseRank(phase)
		if next != current+1 {
			return application.ErrValidation
		}
		revision, err := lockReportRevision(tx, item)
		if err != nil {
			return err
		}
		if revision.ValidityStatus != "ACTIVE" || reportPhaseRank(revision.Status) != current {
			return application.ErrConflict
		}
		if err := advanceReportRevision(tx, &revision, phase, event); err != nil {
			return err
		}
		updates["report_status"] = phase
		updates["report_updated_at"] = event.CreatedAt
		updates["report_updated_by"] = event.ActorUserID
		switch phase {
		case "COMPILING":
			updates["report_prepared_by"] = event.ActorUserID
		case "REVIEWED":
			updates["report_reviewed_by"] = event.ActorUserID
		case "ISSUED":
			updates["report_issued_by"] = event.ActorUserID
		}
	case application.EventEquipmentReturned:
		// 设备归还只能作用于已进入交付的服务项：待确认/待复核/待分配还没有设备清单可还，
		// 已终止是终态，允许事后归还等于让终态数据可被改写。
		if item.Status == "待确认" || item.Status == "待复核" || item.Status == "待分配" || item.Status == domain.ProjectStatusTerminated {
			return application.ErrValidation
		}
		if err := markEquipmentReturned(tx, item, event); err != nil {
			return err
		}
	case application.EventPreparationStarted:
		// 首次准备从“待实施”进入；现场回退会回到“实施准备中”，允许负责人重新核验
		// 设备与行程后再次提交准备。新的 PREPARATION_STARTED 事件是重新进入现场节点
		// 的明确边界，避免刚回退的项目继续残留在现场实施列表。
		if !canStartPreparation(item.Status) {
			return application.ErrValidation
		}
		if err := updateImplPlanEquipment(tx, item, event); err != nil {
			return err
		}
		updates["status"] = "实施准备中"
	case application.EventPreparationRevoked:
		if item.Status != "实施准备中" {
			return application.ErrValidation
		}
		if err := clearImplPlan(tx, item, event, false); err != nil {
			return err
		}
		updates["status"] = "待实施"
	case application.EventRollbackApproved:
		if err := validateRollbackApproval(tx, item, event); err != nil {
			return err
		}
		switch stringValue(event.Payload, "kind") {
		case "FIELD_TO_PREPARATION":
			if item.Status != "实施中" {
				return application.ErrValidation
			}
			updates["status"] = "实施准备中"
		case "REPORT_TO_FIELD":
			if item.Status != "现场实施完成" || (item.ReportStatus != "COMPILING" && item.ReportStatus != "REVIEWED") {
				return application.ErrValidation
			}
			updates["status"] = "实施中"
			updates["report_status"] = "NONE"
			updates["report_updated_at"] = event.CreatedAt
			updates["report_updated_by"] = event.ActorUserID
		default:
			return application.ErrValidation
		}
	case application.EventRollbackWithdrawn:
		if err := validateRollbackWithdrawal(tx, item, event); err != nil {
			return err
		}
	case application.EventReportCorrectionApproved:
		if err := validateReportCorrectionApproval(tx, item, event); err != nil {
			return err
		}
		if item.Status != "现场实施完成" || (item.ReportStatus != "ISSUED" && item.ReportStatus != "ARCHIVED") {
			return application.ErrValidation
		}
		if err := invalidateAndCreateReportRevision(tx, item, event); err != nil {
			return err
		}
		updates["report_status"] = "NONE"
		updates["report_revision"] = item.ReportRevision + 1
		updates["report_prepared_by"] = ""
		updates["report_reviewed_by"] = ""
		updates["report_issued_by"] = ""
		updates["report_updated_at"] = event.CreatedAt
		updates["report_updated_by"] = event.ActorUserID
	case application.EventFieldRecordSubmitted:
		// 现场记录（原始数据 / 环境条件）是进入"实施中"的真实动作。
		// 原先由坐标签到承担这个状态推进，但那份坐标没有任何证明力，已删除；
		// 这里沿用同一转移，避免服务项停在"实施准备中"再也走不动。
		if item.Status != "待实施" && item.Status != "实施准备中" && item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "实施中"
		if err := persistEvidenceFiles(tx, item, event, "FIELD"); err != nil {
			return err
		}
	case application.EventFieldCompleted:
		// 按服务项确认现场完成：先做完的项不必等项目里最后一个动作"顺带"完成。
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "现场实施完成"
		updates["report_status"] = "NONE"
		if err := ensureReportRevision(tx, item, event.CreatedAt); err != nil {
			return err
		}
	case application.EventDeviationReported:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "异常处理中"
		if err := persistEvidenceFiles(tx, item, event, "DEVIATION"); err != nil {
			return err
		}
	case application.EventDeviationReviewed:
		if item.Status != "异常处理中" {
			return application.ErrValidation
		}
		switch stringValue(event.Payload, "decision") {
		case "RELEASE":
			updates["status"] = "实施中"
		case "RETEST":
			updates["status"] = "待实施"
		case "TERMINATE":
			updates["status"] = "已终止"
		default:
			return application.ErrValidation
		}
	default:
		// 审计类事件只留痕、不改服务项状态。其余未知类型必须显式拒绝：
		// 静默成功会让未接线的新事件在接口层返回成功、审计流显示「已发生」，
		// 而业务状态停在原地，排查时无从判断。
		if isAuditOnlyEvent(event.Type) {
			return nil
		}
		return application.ErrValidation
	}
	// 状态真正变化时记录「进入当前状态的时刻」，作为 SLA 停留时长的唯一基准。
	if _, changed := updates["status"]; changed {
		updates["status_changed_at"] = event.CreatedAt
	}
	// 条件更新 + 版本自增：把"写入必须基于事务内读到的那一版"写成显式不变量。
	// 当前调用方已在事务内加了 FOR UPDATE，这是防御性的第二道闸；
	// 将来若有旁路写路径忘记加锁，会以冲突而不是静默覆盖收场。
	updates["version"] = gorm.Expr("version + 1")
	result := tx.Model(&serviceItemRecord{}).
		Where("tenant_id=? AND id=? AND version=?", item.TenantID, item.ID, item.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func canStartPreparation(status string) bool {
	return status == "待实施" || status == "实施准备中"
}

// isAuditOnlyEvent 列出不改变服务项状态、仅用于留痕的事件类型。
func isAuditOnlyEvent(eventType string) bool {
	switch eventType {
	case application.EventContractActivated, application.EventContractStampStatus,
		application.EventWarningTriggered, application.EventAutomationTriggered,
		application.EventRollbackRequested, application.EventRollbackRejected,
		application.EventReportCorrectionRequested, application.EventReportCorrectionRejected:
		return true
	}
	return false
}

func applyProjectEvent(tx *gorm.DB, project *projectRecord, event domain.DeliveryEvent) error {
	updates := map[string]any{"updated_at": event.CreatedAt}
	switch event.Type {
	case application.EventScopeChangeDetected:
		// 范围变更检测命中：项目进入补充协议处理中，等待合同回写后重新确认拆解。
		updates["supplement_status"] = "REQUIRED"
		updates["status"] = "补充协议处理中"
	case application.EventDecompositionAdjusted:
		var existing int64
		if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=? AND status NOT IN ?", project.TenantID, project.ID, []string{"待确认", "待复核"}).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return application.ErrValidation
		}
		encoded, _ := json.Marshal(event.Payload["service_items"])
		var items []domain.ServiceItem
		if err := json.Unmarshal(encoded, &items); err != nil || len(items) == 0 {
			return application.ErrValidation
		}
		var oldItems []serviceItemRecord
		if err := tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Find(&oldItems).Error; err != nil {
			return err
		}
		// 实施计划和交付事件属于旧服务项的历史证据，随服务项归档保留，不做物理删除。
		if err := tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Delete(&serviceItemRecord{}).Error; err != nil {
			return err
		}
		// 拆解调整的服务项由应用层只带业务字段，编号必须在这里补齐：
		// 主键为空会让多行互相冲突而整单回滚，单行则会落成 id='' 的孤儿行——
		// 事件分发以 service_item_id != "" 判定，该服务项之后再也无法被确认或实施。
		maxSequence := 0
		for _, old := range oldItems {
			if sequence := serviceItemSequence(old.ID); sequence > maxSequence {
				maxSequence = sequence
			}
		}
		for index, item := range items {
			if strings.TrimSpace(item.ID) == "" {
				item.ID = serviceItemIDFor(project.ID, maxSequence+index+1)
			}
			rec := serviceItemRecord{ID: item.ID, TenantID: project.TenantID, ProjectID: project.ID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &event.CreatedAt, CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt, UpdatedBy: event.ActorUserID}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		updates["services"] = len(items)
		updates["supplement_status"] = "REQUIRED"
		updates["status"] = "补充协议处理中"
	}
	if len(updates) == 1 {
		return nil
	}
	// 项目业务版本：仅在项目自身内容变更时自增（派生状态缓存同步不算业务变更）。
	// 与条件更新配对，让"我基于的是旧版本"这类并发冲突能被显式发现而不是静默覆盖。
	updates["version"] = gorm.Expr("version + 1")
	result := tx.Model(&projectRecord{}).
		Where("tenant_id=? AND id=? AND version=?", project.TenantID, project.ID, project.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

// serviceItemIDFor 生成服务项编号，与创建、合同激活路径保持同一语义：SI-<项目后缀>-NNN。
func serviceItemIDFor(projectID string, sequence int) string {
	return fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(projectID, "PJ-"), sequence)
}

// serviceItemSequence 解析既有服务项编号末段的序号；无法解析时返回 0，
// 使拆解调整总能从 1 开始顺延而不是与归档行重号。
func serviceItemSequence(itemID string) int {
	parts := strings.Split(strings.TrimSpace(itemID), "-")
	if len(parts) == 0 {
		return 0
	}
	sequence, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || sequence < 0 {
		return 0
	}
	return sequence
}

// syncProjectStatusColumn 在事件事务提交前按派生规则重算并回写 pm_project.status。
// 与读侧（ListProjects/GetProject/Dashboard/FindProjectByContractVersion）共用
// domain.DeriveProjectStatus 单一口径，保证存储列永不分叉：服务项状态推进到哪，
// 项目状态缓存就立刻对齐到哪，不再依赖某条事件手工设置项目状态。
func syncProjectStatusColumn(tx *gorm.DB, tenantID, projectID string) error {
	var project projectRecord
	// 与事件路径同一加锁顺序（先服务项、后项目行）：项目行锁把所有针对同一项目的
	// 写入串行化，随后的服务项投影读才是稳定快照，不会把落后一档的状态写回存储列。
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, projectID).First(&project).Error; err != nil {
		return err
	}
	var items []domain.ProjectStatusItem
	if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=?", tenantID, projectID).Select("status, report_status").Scan(&items).Error; err != nil {
		return err
	}
	derived := domain.DeriveProjectStatus(items, project.SupplementStatus, project.Status)
	// 进度与状态同源：两者都由同一次服务项投影派生，避免进度退化成没人写的装饰字段。
	progress := domain.DeriveProjectProgress(items)
	return tx.Model(&projectRecord{}).Where("tenant_id=? AND id=?", tenantID, projectID).
		Updates(map[string]any{"status": derived, "progress": progress}).Error
}

// ListSlaOverdue 返回超期服务项：计划完成时间早于当前 UTC 且尚未进入终态。
// 与列表/仪表盘一致地套用服务项数据范围，超期时长由数据库按小时取整。
func (r *Repository) ListSlaOverdue(ctx context.Context, filter platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	var rows []struct {
		ID              string
		ProjectID       string
		Site            string
		Category        string
		Status          string
		PlannedEnd      *time.Time
		StatusChangedAt *time.Time
		OverdueHours    int64
	}
	query := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).
		Select("id, project_id, site, category, status, planned_end, status_changed_at, COALESCE(TIMESTAMPDIFF(HOUR, planned_end, UTC_TIMESTAMP()), 0) AS overdue_hours").
		// 候选集必须同时覆盖两类口径：①计划完成时间已过（固定口径）②按规则的状态停留超时/临近。
		// 后者恰恰包含「计划时间未到但已临近超时」的项，因此不能在此按 planned_end 过滤，
		// 否则提前提醒永远不会触发；计划完成口径的状态过滤下沉到 computeSlaItems。
		// 唯一在此排除的是终态「已终止」：它没有后续动作，不属于任何 SLA 口径。
		Where("status <> ?", domain.ProjectStatusTerminated)
	if err := query.Order("planned_end").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domain.SlaOverdueItem, 0, len(rows))
	for _, row := range rows {
		plannedEnd := ""
		if row.PlannedEnd != nil {
			plannedEnd = row.PlannedEnd.Format(time.RFC3339)
		}
		item := domain.SlaOverdueItem{ID: row.ID, ProjectID: row.ProjectID, Site: row.Site, Category: row.Category, Status: row.Status, PlannedEnd: plannedEnd, OverdueHours: row.OverdueHours}
		if row.StatusChangedAt != nil {
			item.StatusChangedAt = *row.StatusChangedAt
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListDeliveryEvents(ctx context.Context, filter platform.ScopeFilter, projectID string) ([]domain.DeliveryEvent, error) {
	projects := applyProjectScope(r.db.WithContext(ctx).Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND project_id IN (?)", filter.TenantID, projects)
	if projectID != "" {
		query = query.Where("project_id=?", projectID)
	}
	var records []deliveryEventRecord
	if err := query.Order("created_at DESC").Limit(500).Find(&records).Error; err != nil {
		return nil, err
	}
	out := make([]domain.DeliveryEvent, 0, len(records))
	for _, v := range records {
		payload := map[string]any{}
		_ = json.Unmarshal(v.Payload, &payload)
		out = append(out, domain.DeliveryEvent{ID: v.ID, ProjectID: v.ProjectID, ServiceItemID: v.ServiceItemID, Type: v.EventType, ActorUserID: v.ActorUserID, Payload: payload, CreatedAt: v.CreatedAt})
	}
	return out, nil
}

func (r *Repository) FindProjectForDeviation(ctx context.Context, filter platform.ScopeFilter, deviationID string) (string, string, error) {
	projects := applyProjectScope(r.db.WithContext(ctx).Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	var record deliveryEventRecord
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND project_id IN (?) AND event_type = ? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deviation_id')) = ?", filter.TenantID, projects, application.EventDeviationReported, deviationID).
		Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", application.ErrNotFound
	}
	return record.ProjectID, record.ServiceItemID, err
}

func (r *Repository) UpsertCapability(ctx context.Context, item domain.Capability, actor string) (domain.Capability, error) {
	if item.ID == "" {
		item.ID = ulid.Make().String()
	}
	item.UpdatedAt = time.Now().UTC()
	codes := jsonValue(item.Codes)
	// 人员档案的身份复核状态不能由导入/编辑覆盖：新档案默认 UNLINKED（未关联平台账号）
	// 或 ACTIVE（已关联但尚未复核），真实状态只能由回基础平台的复核写入。
	identityStatus := domain.IdentityStatusUnlinked
	if strings.TrimSpace(item.UserID) != "" {
		identityStatus = firstValue(item.IdentityStatus, domain.IdentityStatusActive)
	}
	rec := capabilityRecord{ID: item.ID, TenantID: item.TenantID, ResourceType: item.ResourceType, ResourceID: item.ResourceID, ResourceName: item.ResourceName, UserID: strings.TrimSpace(item.UserID), CapabilityCodes: codes, ValidFrom: timePtr(item.ValidFrom), ValidUntil: timePtr(item.ValidUntil), Status: item.Status, UsageScope: firstValue(item.UsageScope, domain.EquipmentUsageAny), IdentityStatus: identityStatus, UpdatedAt: item.UpdatedAt, UpdatedBy: actor}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "resource_type"}, {Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"resource_name", "user_id", "capability_codes", "valid_from", "valid_until", "status", "usage_scope", "updated_at", "updated_by"})}).Create(&rec).Error
	return item, err
}

// DeleteEquipment 物理删除设备主数据。业务历史使用的是实施计划和交付事件中的设备快照，
// 因而不会随着目录记录删除而丢失；活动占用的保护由应用层在同一租户边界内先行校验。
func (r *Repository) DeleteEquipment(ctx context.Context, tenantID, resourceID string) error {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND resource_type = ? AND resource_id = ?", tenantID, "EQUIPMENT", strings.TrimSpace(resourceID)).
		Delete(&capabilityRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return application.ErrNotFound
	}
	return nil
}

func (r *Repository) ListCapabilities(ctx context.Context, tenant, typ string) ([]domain.Capability, error) {
	q := r.db.WithContext(ctx).Where("tenant_id=?", tenant)
	if typ != "" {
		q = q.Where("resource_type=?", typ)
	}
	var records []capabilityRecord
	if err := q.Order("resource_type, resource_name").Find(&records).Error; err != nil {
		return nil, err
	}
	return capabilitiesFromRecords(records), nil
}
func (r *Repository) FindCapabilities(ctx context.Context, tenant, at string, ids []string) ([]domain.Capability, error) {
	if len(ids) == 0 {
		return []domain.Capability{}, nil
	}
	var records []capabilityRecord
	// 有效期按「日期」口径判定：业务填写的是日期（有效期至 X 日），应含当日。
	// 若直接比较 valid_until >= now，到期日当天 00:00 之后就会被判为过期，
	// 等于把"有效期至今天"变成"昨天就失效"，与录入人的理解差一天。
	err := r.db.WithContext(ctx).Where(
		"tenant_id=? AND resource_id IN ? AND status='ACTIVE' AND (valid_from IS NULL OR DATE(valid_from)<=DATE(?)) AND (valid_until IS NULL OR DATE(valid_until)>=DATE(?))",
		tenant, ids, at, at).Find(&records).Error
	return capabilitiesFromRecords(records), err
}
func capabilitiesFromRecords(records []capabilityRecord) []domain.Capability {
	out := make([]domain.Capability, 0, len(records))
	for _, v := range records {
		codes := []string{}
		_ = json.Unmarshal(v.CapabilityCodes, &codes)
		item := domain.Capability{ID: v.ID, ResourceType: v.ResourceType, ResourceID: v.ResourceID, ResourceName: v.ResourceName, UserID: v.UserID, Codes: codes, Status: v.Status, UsageScope: firstValue(v.UsageScope, domain.EquipmentUsageAny), IdentityStatus: firstValue(v.IdentityStatus, domain.IdentityStatusUnlinked), UpdatedAt: v.UpdatedAt}
		if v.IdentityCheckedAt != nil {
			item.IdentityCheckedAt = *v.IdentityCheckedAt
		}
		if v.ValidFrom != nil {
			item.ValidFrom = *v.ValidFrom
		}
		if v.ValidUntil != nil {
			item.ValidUntil = *v.ValidUntil
		}
		out = append(out, item)
	}
	return out
}
func createEvent(tx *gorm.DB, event domain.DeliveryEvent) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	return tx.Create(&deliveryEventRecord{ID: event.ID, TenantID: event.TenantID, ProjectID: event.ProjectID, ServiceItemID: event.ServiceItemID, EventType: event.Type, ActorUserID: event.ActorUserID, Payload: payload, CreatedAt: event.CreatedAt}).Error
}

// upsertImplPlan 把实施计划（含渗透测试专项合规要素）落到 pm_impl_plan，与服务项 1:1。
// 同名唯一键冲突时原地刷新，保证重复发布实施计划不会产生脏数据。
func upsertImplPlan(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	record := implPlanRecord{
		ID:                  ulid.Make().String(),
		TenantID:            item.TenantID,
		ServiceItemID:       item.ID,
		PlannedStart:        rfc3339Time(event.Payload, "planned_start"),
		PlannedEnd:          rfc3339Time(event.Payload, "planned_end"),
		SitePlan:            stringValue(event.Payload, "site_plan"),
		PenetrationTestPlan: stringValue(event.Payload, "penetration_test_plan"),
		AuthDocNo:           stringValue(event.Payload, "auth_doc_no"),
		AuthStart:           rfc3339Time(event.Payload, "auth_start"),
		AuthEnd:             rfc3339Time(event.Payload, "auth_end"),
		AuthScope:           stringValue(event.Payload, "auth_scope"),
		TestScope:           stringValue(event.Payload, "test_scope"),
		TestWindow:          stringValue(event.Payload, "test_window"),
		EmergencyContact:    stringValue(event.Payload, "emergency_contact"),
		RollbackPlan:        stringValue(event.Payload, "rollback_plan"),
		Personnel:           jsonBytes(event.Payload, "personnel"),
		UpdatedAt:           event.CreatedAt,
		UpdatedBy:           event.ActorUserID,
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "service_item_id"}}, DoUpdates: clause.AssignmentColumns([]string{"planned_start", "planned_end", "site_plan", "penetration_test_plan", "auth_doc_no", "auth_start", "auth_end", "auth_scope", "test_scope", "test_window", "emergency_contact", "rollback_plan", "personnel", "updated_at", "updated_by"})}).Create(&record).Error
}

// markEquipmentReturned 在计划行的设备清单里给对应设备写上归还时间。找不到该设备时按幂等
// 处理：可能是重复归还，或设备已被重新保存的清单替换，都不应报错。
func markEquipmentReturned(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	resourceID := strings.TrimSpace(stringValue(event.Payload, "resource_id"))
	if resourceID == "" {
		return application.ErrValidation
	}
	var record implPlanRecord
	if err := tx.Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).Take(&record).Error; err != nil {
		return mapNotFound(err)
	}
	var resources []domain.PlanResource
	if len(record.Equipment) > 0 {
		if err := json.Unmarshal(record.Equipment, &resources); err != nil {
			return err
		}
	}
	updated := make([]domain.PlanResource, 0, len(resources))
	changed := false
	for _, resource := range resources {
		if resource.ResourceID == resourceID && strings.TrimSpace(resource.ReturnedAt) == "" {
			resource.ReturnedAt = event.CreatedAt.Format(time.RFC3339)
			changed = true
		}
		updated = append(updated, resource)
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	return tx.Model(&implPlanRecord{}).Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).
		Updates(map[string]any{"equipment": encoded, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}).Error
}

// updateImplPlanEquipment 把实施准备阶段确定的设备清单写回计划行。设备清单与人员清单
// 分列保存，因此重发计划不会覆盖设备，反之亦然。
func updateImplPlanEquipment(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	equipment := jsonBytes(event.Payload, "equipment")
	if len(equipment) == 0 {
		return nil
	}
	return tx.Model(&implPlanRecord{}).
		Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).
		Updates(map[string]any{"equipment": equipment, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}).Error
}

// clearImplPlan 只清除当前有效计划行，完整旧计划/设备快照仍保留在产生它的交付事件中。
// 计划撤销清空人员与合规要素；准备撤销仅释放设备预约，允许在同一计划上重新准备。
func clearImplPlan(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent, clearPlan bool) error {
	updates := map[string]any{"equipment": nil, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}
	if clearPlan {
		updates["planned_start"] = nil
		updates["planned_end"] = nil
		updates["site_plan"] = ""
		updates["penetration_test_plan"] = ""
		updates["auth_doc_no"] = ""
		updates["auth_start"] = nil
		updates["auth_end"] = nil
		updates["auth_scope"] = ""
		updates["test_scope"] = ""
		updates["test_window"] = ""
		updates["emergency_contact"] = ""
		updates["rollback_plan"] = ""
		updates["personnel"] = nil
	}
	return tx.Model(&implPlanRecord{}).Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).Updates(updates).Error
}

// validateRollbackApproval 在服务项行锁保护下确认请求存在、属于当前项且从未被批准，防止双击
// 或两个审批人并发把同一补偿申请重复执行。
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
		Where("tenant_id=? AND service_item_id=? AND revision=?", item.TenantID, item.ID, item.ReportRevision).
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
		TenantID: item.TenantID, ServiceItemID: item.ID, Revision: item.ReportRevision,
		Status: status, ValidityStatus: "ACTIVE", CreatedAt: now, UpdatedAt: now,
	}).Error
}

func (r *Repository) ListReportRevisions(ctx context.Context, tenantID, itemID string) ([]domain.ReportRevision, error) {
	var rows []reportRevisionRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND service_item_id=?", tenantID, itemID).
		Order("revision DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domain.ReportRevision, 0, len(rows))
	for _, row := range rows {
		items = append(items, domain.ReportRevision{
			ID: row.ID, ServiceItemID: row.ServiceItemID, Revision: row.Revision,
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
		result := tx.Model(&reportRevisionRecord{}).Where("tenant_id=? AND service_item_id=? AND revision=? AND validity_status='ACTIVE'", tenantID, itemID, revision).
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
		Where("tenant_id=? AND service_item_id=? AND revision=?", item.TenantID, item.ID, item.ReportRevision).
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
		TenantID: item.TenantID, ServiceItemID: item.ID, Revision: item.ReportRevision + 1,
		Status: "NONE", ValidityStatus: "ACTIVE", CorrectionRequestID: stringValue(event.Payload, "request_id"),
		CorrectionReason: stringValue(event.Payload, "reason"), CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt,
	}).Error
}
func jsonValue(v any) []byte { b, _ := json.Marshal(v); return b }
func stringValue(values map[string]any, key string) string {
	v, _ := values[key].(string)
	return strings.TrimSpace(v)
}
func expectedVersionValue(values map[string]any) uint64 {
	switch value := values["expected_version"].(type) {
	case uint64:
		return value
	case uint:
		return uint64(value)
	case int:
		if value > 0 {
			return uint64(value)
		}
	case int64:
		if value > 0 {
			return uint64(value)
		}
	case float64:
		if value > 0 && value == float64(uint64(value)) {
			return uint64(value)
		}
	}
	return 0
}
func rfc3339Value(values map[string]any, key string) any {
	value := stringValue(values, key)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return parsed
}
func timePtr(v time.Time) *time.Time {
	if v.IsZero() {
		return nil
	}
	return &v
}
func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return application.ErrNotFound
	}
	return err
}

// UpdateCapabilityIdentities 批量回写人员档案的身份复核结果。
// 只更新身份相关列，避免复核动作覆盖档案的业务字段。
func (r *Repository) UpdateCapabilityIdentities(ctx context.Context, tenantID string, statuses map[string]string, checkedAt time.Time) error {
	if len(statuses) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for userID, status := range statuses {
			if err := tx.Model(&capabilityRecord{}).
				Where("tenant_id = ? AND resource_type = ? AND user_id = ?", tenantID, "PERSON", userID).
				Updates(map[string]any{"identity_status": status, "identity_checked_at": checkedAt, "updated_at": checkedAt}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
