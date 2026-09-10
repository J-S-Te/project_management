package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repository) FindProjectByContractVersion(ctx context.Context, filter platform.ScopeFilter, contractID, version string) (domain.Project, error) {
	var record projectRecord
	err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Where("contract = ? AND contract_version = ?", contractID, version).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Project{}, application.ErrNotFound
	}
	return projectFromRecord(record), err
}

func (r *Repository) ActivateContract(ctx context.Context, project domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, Contract: project.Contract, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Health: project.Health, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
		if err := tx.Create(&pr).Error; err != nil {
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
		pr := projectRecord{ID: project.ID, TenantID: project.TenantID, OwnerOrgID: project.OwnerOrgID, Name: project.Name, Customer: project.Customer, Contract: project.Contract, ContractVersion: project.ContractVersion, SupplementStatus: project.SupplementStatus, Services: project.Services, Category: project.Category, Team: project.Team, Manager: project.Manager, OwnerIdentityID: project.OwnerIdentityID, ManagerIdentityID: project.ManagerIdentityID, Health: project.Health, Status: project.Status, Progress: project.Progress, Due: project.Due, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}
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
		health := "关注"
		if uploaded {
			health = "正常"
		}
		if err := tx.Model(&projectRecord{}).Where("tenant_id = ? AND id = ?", project.TenantID, project.ID).Updates(map[string]any{"health": health, "updated_at": event.CreatedAt}).Error; err != nil {
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
	case application.EventAssignmentPublished:
		if item.Status != "待分配" && item.Status != "待实施" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = stringValue(event.Payload, "team_lead_id")
		updates["project_manager_id"] = stringValue(event.Payload, "project_manager_id")
		updates["engineer_ids"] = jsonValue(event.Payload["engineer_ids"])
		updates["equipment_ids"] = jsonValue(event.Payload["equipment_ids"])
		updates["required_codes"] = jsonValue(event.Payload["required_codes"])
		updates["planned_start"] = rfc3339Value(event.Payload, "planned_start")
		updates["planned_end"] = rfc3339Value(event.Payload, "planned_end")
		updates["conflict_status"] = stringValue(event.Payload, "conflict_status")
		if updates["conflict_status"] == "PASSED" {
			updates["status"] = "待制定计划"
		}
	case application.EventImplementationPlanned:
		if item.Status != "待分配" && item.Status != "待制定计划" {
			return application.ErrValidation
		}
		if item.ProjectManagerID == "" || item.ConflictStatus != "PASSED" {
			return application.ErrValidation
		}
		if item.Special == "是" && item.TechReviewStatus != "APPROVED" {
			return application.ErrValidation
		}
		if item.TestMode == "PENETRATION" && stringValue(event.Payload, "penetration_test_plan") == "" {
			return application.ErrValidation
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
	case application.EventPreparationStarted:
		if item.Status != "待实施" {
			return application.ErrValidation
		}
		updates["status"] = "实施准备中"
	case application.EventFieldCheckIn:
		if item.Status != "待实施" && item.Status != "实施准备中" && item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "实施中"
	case application.EventFieldRecordSubmitted:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
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
		updates["health"] = "关注"
	case application.EventFieldImplementationDone:
		var count int64
		if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=? AND status NOT IN ?", project.TenantID, project.ID, []string{"实施中", "现场实施完成", "已终止"}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return application.ErrValidation
		}
		updates["status"] = "现场实施完成"
		updates["progress"] = 80
		if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=? AND status=?", project.TenantID, project.ID, "实施中").Updates(map[string]any{"status": "现场实施完成", "report_status": "COMPILING", "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}).Error; err != nil {
			return err
		}
	}
	if len(updates) == 1 {
		return nil
	}
	return tx.Model(&projectRecord{}).Where("tenant_id=? AND id=?", project.TenantID, project.ID).Updates(updates).Error
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
	rec := capabilityRecord{ID: item.ID, TenantID: item.TenantID, ResourceType: item.ResourceType, ResourceID: item.ResourceID, ResourceName: item.ResourceName, CapabilityCodes: codes, ValidFrom: timePtr(item.ValidFrom), ValidUntil: timePtr(item.ValidUntil), Status: item.Status, UpdatedAt: item.UpdatedAt, UpdatedBy: actor}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "resource_type"}, {Name: "resource_id"}}, DoUpdates: clause.AssignmentColumns([]string{"resource_name", "capability_codes", "valid_from", "valid_until", "status", "updated_at", "updated_by"})}).Create(&rec).Error
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
		item := domain.Capability{ID: v.ID, ResourceType: v.ResourceType, ResourceID: v.ResourceID, ResourceName: v.ResourceName, Codes: codes, Status: v.Status, UpdatedAt: v.UpdatedAt}
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
		UpdatedAt:           event.CreatedAt,
		UpdatedBy:           event.ActorUserID,
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "service_item_id"}}, DoUpdates: clause.AssignmentColumns([]string{"planned_start", "planned_end", "site_plan", "penetration_test_plan", "auth_doc_no", "auth_start", "auth_end", "auth_scope", "test_scope", "test_window", "emergency_contact", "rollback_plan", "updated_at", "updated_by"})}).Create(&record).Error
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
