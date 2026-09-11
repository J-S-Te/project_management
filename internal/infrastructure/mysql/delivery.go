package mysql

import (
	"context"
	"encoding/json"
	"errors"
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
	inputs, err := r.projectStatusInputs(ctx, filter, []string{record.ID})
	if err != nil {
		return domain.Project{}, err
	}
	project.Status = domain.DeriveProjectStatus(inputs[record.ID], record.SupplementStatus, record.Status)
	return project, nil
}

func (r *Repository) ActivateContract(ctx context.Context, project domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, CustomerID: project.CustomerID, Contract: project.Contract, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
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
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, Special: item.Special, TestMode: item.TestMode, Status: item.Status, ConflictStatus: item.ConflictStatus, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		return createEvent(tx, event)
	})
}

func (r *Repository) CreateProjectWithServiceItems(ctx context.Context, project domain.Project, items []domain.ServiceItem) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, Contract: project.Contract, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: item.TenantID, ProjectID: item.ProjectID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, Special: item.Special, TestMode: item.TestMode, Status: item.Status, ConflictStatus: item.ConflictStatus, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
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
		return createEvent(tx, event)
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
		return createEvent(tx, event)
	})
}

func applyItemEvent(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	updates := map[string]any{"updated_at": event.CreatedAt, "updated_by": event.ActorUserID}
	switch event.Type {
	case application.EventTeamAssigned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = stringValue(event.Payload, "team_lead_id")
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
		if next <= current {
			return application.ErrValidation
		}
		updates["report_status"] = phase
		updates["report_updated_at"] = event.CreatedAt
		updates["report_updated_by"] = event.ActorUserID
	case application.EventEquipmentReturned:
		if err := markEquipmentReturned(tx, item, event); err != nil {
			return err
		}
	case application.EventPreparationStarted:
		if item.Status != "待实施" {
			return application.ErrValidation
		}
		if err := updateImplPlanEquipment(tx, item, event); err != nil {
			return err
		}
		updates["status"] = "实施准备中"
	case application.EventFieldRecordSubmitted:
		// 现场记录（原始数据 / 环境条件）是进入"实施中"的真实动作。
		// 原先由坐标签到承担这个状态推进，但那份坐标没有任何证明力，已删除；
		// 这里沿用同一转移，避免服务项停在"实施准备中"再也走不动。
		if item.Status != "待实施" && item.Status != "实施准备中" && item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "实施中"
	case application.EventFieldCompleted:
		// 按服务项确认现场完成：先做完的项不必等项目里最后一个动作"顺带"完成。
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "现场实施完成"
		updates["report_status"] = "COMPILING"
	case application.EventDeviationReported:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "异常处理中"
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
		return nil
	}
	return tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND id=?", item.TenantID, item.ID).Updates(updates).Error
}

func applyProjectEvent(tx *gorm.DB, project *projectRecord, event domain.DeliveryEvent) error {
	updates := map[string]any{"updated_at": event.CreatedAt}
	switch event.Type {
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
		oldIDs := make([]string, 0, len(oldItems))
		for _, old := range oldItems {
			oldIDs = append(oldIDs, old.ID)
		}
		if len(oldIDs) > 0 {
			if err := tx.Where("tenant_id=? AND service_item_id IN ?", project.TenantID, oldIDs).Delete(&implPlanRecord{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Delete(&serviceItemRecord{}).Error; err != nil {
			return err
		}
		for _, item := range items {
			rec := serviceItemRecord{ID: item.ID, TenantID: project.TenantID, ProjectID: project.ID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, Special: item.Special, TestMode: item.TestMode, Status: item.Status, ConflictStatus: item.ConflictStatus, CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt, UpdatedBy: event.ActorUserID}
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
	return tx.Model(&projectRecord{}).Where("tenant_id=? AND id=?", project.TenantID, project.ID).Updates(updates).Error
}

// syncProjectStatusColumn 在事件事务提交前按派生规则重算并回写 pm_project.status。
// 与读侧（ListProjects/GetProject/Dashboard/FindProjectByContractVersion）共用
// domain.DeriveProjectStatus 单一口径，保证存储列永不分叉：服务项状态推进到哪，
// 项目状态缓存就立刻对齐到哪，不再依赖某条事件手工设置项目状态。
func syncProjectStatusColumn(tx *gorm.DB, tenantID, projectID string) error {
	var project projectRecord
	if err := tx.Where("tenant_id=? AND id=?", tenantID, projectID).First(&project).Error; err != nil {
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
		ID           string
		ProjectID    string
		Site         string
		Category     string
		Status       string
		PlannedEnd   *time.Time
		OverdueHours int64
	}
	query := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).
		Select("id, project_id, site, category, status, planned_end, TIMESTAMPDIFF(HOUR, planned_end, UTC_TIMESTAMP()) AS overdue_hours").
		Where("planned_end IS NOT NULL AND planned_end < UTC_TIMESTAMP() AND status NOT IN ?", []string{domain.ProjectStatusCompleted, domain.ProjectStatusTerminated})
	if err := query.Order("planned_end").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domain.SlaOverdueItem, 0, len(rows))
	for _, row := range rows {
		plannedEnd := ""
		if row.PlannedEnd != nil {
			plannedEnd = row.PlannedEnd.Format(time.RFC3339)
		}
		items = append(items, domain.SlaOverdueItem{ID: row.ID, ProjectID: row.ProjectID, Site: row.Site, Category: row.Category, Status: row.Status, PlannedEnd: plannedEnd, OverdueHours: row.OverdueHours})
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
	err := r.db.WithContext(ctx).Where("tenant_id=? AND resource_id IN ? AND status='ACTIVE' AND (valid_from IS NULL OR valid_from<=?) AND (valid_until IS NULL OR valid_until>=?)", tenant, ids, at, at).Find(&records).Error
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
func jsonValue(v any) []byte { b, _ := json.Marshal(v); return b }
func stringValue(values map[string]any, key string) string {
	v, _ := values[key].(string)
	return strings.TrimSpace(v)
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
