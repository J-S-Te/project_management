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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListProjects(ctx context.Context, filter platform.ScopeFilter, q, status string) ([]domain.Project, error) {
	query := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project")
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + q + "%"
		query = query.Where("id LIKE ? OR name LIKE ? OR customer LIKE ? OR contract LIKE ? OR category LIKE ? OR manager LIKE ?", like, like, like, like, like, like)
	}
	var records []projectRecord
	if err := query.Order("id DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	inputs, err := r.projectStatusInputs(ctx, filter, projectIDsOf(records))
	if err != nil {
		return nil, err
	}
	wanted := strings.TrimSpace(status)
	items := make([]domain.Project, 0, len(records))
	for _, record := range records {
		project := projectFromRecord(record)
		project.Status = domain.DeriveProjectStatus(inputs[record.ID], record.SupplementStatus, record.Status)
		// 状态过滤必须作用于唯一的派生状态，而不是可能滞后的存储列。
		if wanted != "" && project.Status != wanted {
			continue
		}
		items = append(items, project)
	}
	return items, nil
}
func (r *Repository) GetProject(ctx context.Context, filter platform.ScopeFilter, id string) (domain.Project, error) {
	var record projectRecord
	err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Where("pm_project.id = ?", id).First(&record).Error
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

// projectIDsOf 提取项目主键，供后续按项目聚合服务项状态。
func projectIDsOf(records []projectRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// projectStatusInputs 按项目聚合服务项状态，作为派生项目唯一状态的输入。
func (r *Repository) projectStatusInputs(ctx context.Context, filter platform.ScopeFilter, projectIDs []string) (map[string][]domain.ProjectStatusItem, error) {
	result := map[string][]domain.ProjectStatusItem{}
	if len(projectIDs) == 0 {
		return result, nil
	}
	var rows []projectStatusRow
	if err := projectStatusInputQuery(r.db.WithContext(ctx), filter, projectIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ProjectID] = append(result[row.ProjectID], domain.ProjectStatusItem{Status: row.Status, ReportStatus: row.ReportStatus})
	}
	return result, nil
}

// projectStatusRow 是派生唯一状态所需的最小服务项投影。
type projectStatusRow struct {
	ProjectID    string
	Status       string
	ReportStatus string
}

// projectStatusInputQuery 构造服务项状态投影查询，复用服务项数据范围，
// 保证派生结果与列表查询处于同一可见边界。
func projectStatusInputQuery(db *gorm.DB, filter platform.ScopeFilter, projectIDs []string) *gorm.DB {
	return applyServiceItemScope(db.Model(&serviceItemRecord{}), db, filter).
		Select("project_id, status, report_status").
		Where("project_id IN ?", projectIDs)
}
func (r *Repository) CreateProject(ctx context.Context, item domain.Project) error {
	return r.db.WithContext(ctx).Create(&projectRecord{ID: item.ID, TenantID: item.TenantID, OwnerOrgID: item.OwnerOrgID, Name: item.Name, Customer: item.Customer, CustomerID: item.CustomerID, Contract: item.Contract, ContractVersion: item.ContractVersion, SupplementStatus: firstValue(item.SupplementStatus, "NONE"), Services: item.Services, Category: item.Category, Team: item.Team, Manager: item.Manager, OwnerIdentityID: item.OwnerIdentityID, ManagerIdentityID: item.ManagerIdentityID, Status: item.Status, Progress: item.Progress, Due: item.Due, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}).Error
}
func (r *Repository) ListServiceItems(ctx context.Context, filter platform.ScopeFilter, projectID string) ([]domain.ServiceItem, error) {
	query := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter)
	if projectID != "" {
		query = query.Where("project_id = ?", projectID)
	}
	var records []serviceItemRecord
	if err := query.Order("id").Find(&records).Error; err != nil {
		return nil, err
	}
	plans := map[string]domain.ImplementationPlan{}
	if len(records) > 0 {
		ids := make([]string, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.ID)
		}
		var rows []implPlanRecord
		if err := r.db.WithContext(ctx).Where("tenant_id=? AND service_item_id IN ?", filter.TenantID, ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			plan := implPlanFromRecord(row)
			plans[row.ServiceItemID] = plan
		}
	}
	items := make([]domain.ServiceItem, 0, len(records))
	for _, record := range records {
		item := serviceFromRecord(record)
		if plan, ok := plans[record.ID]; ok {
			item.ImplementationPlan = &plan
		}
		items = append(items, item)
	}
	return items, nil
}
func (r *Repository) GetServiceItem(ctx context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	var record serviceItemRecord
	err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Where("pm_service_item.id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ServiceItem{}, application.ErrNotFound
	}
	return serviceFromRecord(record), err
}

func (r *Repository) ConfirmServiceItems(ctx context.Context, filter platform.ScopeFilter, ids []string, actor string) ([]domain.ServiceItem, error) {
	tenant := filter.TenantID
	var result []domain.ServiceItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var records []serviceItemRecord
		// 对待确认服务项加排他锁，使状态校验、批量确认和项目状态推进处于同一串行化临界区。
		// 锁查询也套用数据范围过滤：即便范围过滤在事务提交前发生变化，事务内也只能看到
		// 授权范围内的行，从根上杜绝"读时已校验、写时越界"的 TOCTOU 窗口。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Scopes(func(db *gorm.DB) *gorm.DB { return applyServiceItemScope(db, tx, filter) }).
			Where("pm_service_item.id IN ?", ids).Find(&records).Error; err != nil {
			return err
		}
		if len(records) != len(unique(ids)) {
			return application.ErrNotFound
		}
		for _, record := range records {
			if record.Status != "待确认" && record.Status != "待复核" && record.Status != "待分配" {
				return application.ErrValidation
			}
		}
		now := time.Now().UTC()
		update := tx.Model(&serviceItemRecord{}).
			Where("tenant_id = ? AND id IN ? AND status IN ?", tenant, ids, []string{"待确认", "待复核", "待分配"}).
			Updates(map[string]any{"status": "待分配", "updated_at": now, "updated_by": actor})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != int64(len(records)) {
			// 条件更新行数不匹配表示锁等待期间状态已变化，不用旧快照覆盖并发操作。
			return application.ErrConflict
		}
		// 特殊方法服务项确认后进入技术总监复核窗口，复核通过前不能发布实施计划。
		pending := make([]string, 0)
		for _, record := range records {
			if record.Special == "是" && record.TechReviewStatus != "APPROVED" {
				pending = append(pending, record.ID)
			}
		}
		if len(pending) > 0 {
			if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND id IN ?", tenant, pending).
				Updates(map[string]any{"tech_review_status": "PENDING", "updated_at": now, "updated_by": actor}).Error; err != nil {
				return err
			}
		}
		projectIDs := make([]string, 0)
		seenProjects := map[string]bool{}
		for _, record := range records {
			if !seenProjects[record.ProjectID] {
				seenProjects[record.ProjectID] = true
				projectIDs = append(projectIDs, record.ProjectID)
			}
		}
		for _, projectID := range projectIDs {
			var pending int64
			if err := tx.Model(&serviceItemRecord{}).Where("tenant_id = ? AND project_id = ? AND status IN ?", tenant, projectID, []string{"待确认", "待复核"}).Count(&pending).Error; err != nil {
				return err
			}
			if pending == 0 {
				if err := tx.Model(&projectRecord{}).Where("tenant_id = ? AND id = ? AND status = ?", tenant, projectID, "待拆解确认").Updates(map[string]any{"status": "待分配", "updated_at": now}).Error; err != nil {
					return err
				}
			}
		}
		result = make([]domain.ServiceItem, 0, len(records))
		for _, record := range records {
			record.Status = "待分配"
			if record.Special == "是" && record.TechReviewStatus != "APPROVED" {
				record.TechReviewStatus = "PENDING"
			}
			result = append(result, serviceFromRecord(record))
		}
		return nil
	})
	return result, err
}

// ruleKinds 六套真实配置表对应的 kind 标识。列表中顺序即 ListRules 不指定 kind 时的合并顺序。
var ruleKinds = []string{"split-rules", "warning-rules", "automations", "permissions", "sla", "standards"}

func ruleTable(kind string) string {
	switch kind {
	case "standards":
		return "pm_standard"
	case "split-rules":
		return "pm_split_rule"
	case "warning-rules":
		return "pm_warning_rule"
	case "automations":
		return "pm_automation"
	case "permissions":
		return "pm_field_permission"
	case "sla":
		return "pm_sla"
	default:
		return ""
	}
}

func (r *Repository) ListRules(ctx context.Context, tenant, kind string) ([]domain.Rule, error) {
	kinds := ruleKinds
	if kind != "" {
		kinds = []string{kind}
	}
	items := make([]domain.Rule, 0, 8)
	for _, current := range kinds {
		table := ruleTable(current)
		if table == "" {
			if kind != "" {
				return nil, application.ErrValidation
			}
			continue
		}
		var rows []ruleRow
		if err := r.db.WithContext(ctx).Table(table).Where("tenant_id = ?", tenant).Order("id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			items = append(items, ruleFromRow(row))
		}
	}
	return items, nil
}

func (r *Repository) CreateRule(ctx context.Context, item domain.Rule) (domain.Rule, error) {
	if ruleTable(item.Kind) == "" {
		return item, application.ErrValidation
	}
	now := time.Now().UTC()
	record, err := ruleRecordFor(item, now)
	if err != nil {
		return item, err
	}
	if err := r.db.WithContext(ctx).Create(record).Error; err != nil {
		return item, err
	}
	return r.GetRule(ctx, item.TenantID, item.Kind, item.ID)
}

// GetRule 按表/租户/ID 重新读取配置行，供创建、编辑、启停后回显。
func (r *Repository) GetRule(ctx context.Context, tenant, kind string, id int64) (domain.Rule, error) {
	table := ruleTable(kind)
	if table == "" {
		return domain.Rule{}, application.ErrValidation
	}
	var row ruleRow
	if err := r.db.WithContext(ctx).Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Scan(&row).Error; err != nil {
		return domain.Rule{}, err
	}
	if row.ID == 0 {
		return domain.Rule{}, application.ErrNotFound
	}
	return ruleFromRow(row), nil
}

func (r *Repository) SetRuleEnabled(ctx context.Context, tenant, kind string, id int64, enabled bool, actor string) (domain.Rule, error) {
	table := ruleTable(kind)
	if table == "" {
		return domain.Rule{}, application.ErrValidation
	}
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).
			Updates(map[string]any{"enabled": enabled, "updated_at": now, "updated_by": actor})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return application.ErrNotFound
		}
		var row ruleRow
		if err := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == 0 {
			return application.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return domain.Rule{}, err
	}
	return r.GetRule(ctx, tenant, kind, id)
}

// UpdateRule 整行更新配置：名称、启停开关和该 kind 专属字段。
func (r *Repository) UpdateRule(ctx context.Context, tenant, kind string, id int64, item domain.Rule) (domain.Rule, error) {
	now := time.Now().UTC()
	columns := map[string]any{}
	switch kind {
	case "split-rules":
		columns = map[string]any{"name": item.Name, "scope": item.Scope, "enabled": item.Enabled}
	case "warning-rules":
		columns = map[string]any{"name": item.Name, "check_type": item.CheckType, "threshold": item.Threshold, "enabled": item.Enabled}
	case "automations":
		columns = map[string]any{"name": item.Name, "trigger": item.Trigger, "target": item.Target, "enabled": item.Enabled}
	case "permissions":
		columns = map[string]any{"name": item.Name, "role_code": item.RoleCode, "field_name": item.FieldName, "access_level": item.AccessLevel, "enabled": item.Enabled}
	case "sla":
		columns = map[string]any{"name": item.Name, "status": item.Status, "deadline_hours": item.DeadlineHours, "remind_hours": item.RemindHours, "enabled": item.Enabled}
	case "standards":
		columns = map[string]any{"name": item.Name, "scope": item.Scope, "enabled": item.Enabled}
	default:
		return domain.Rule{}, application.ErrValidation
	}
	columns["updated_at"] = now
	columns["updated_by"] = item.UpdatedBy
	table := ruleTable(kind)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Updates(columns)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return application.ErrNotFound
		}
		var row ruleRow
		if err := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == 0 {
			return application.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return domain.Rule{}, err
	}
	return r.GetRule(ctx, tenant, kind, id)
}

// ruleRecordFor 按 kind 构造对应真实表的插入记录，避免把无关列写入目标表。
func ruleRecordFor(item domain.Rule, now time.Time) (any, error) {
	switch item.Kind {
	case "split-rules":
		return &splitRuleRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Scope: item.Scope, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	case "warning-rules":
		return &warningRuleRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, CheckType: item.CheckType, Threshold: item.Threshold, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	case "automations":
		return &automationRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Trigger: item.Trigger, Target: item.Target, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	case "permissions":
		return &fieldPermissionRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, RoleCode: item.RoleCode, FieldName: item.FieldName, AccessLevel: item.AccessLevel, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	case "sla":
		return &slaRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Status: item.Status, DeadlineHours: item.DeadlineHours, RemindHours: item.RemindHours, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	case "standards":
		return &standardRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Scope: item.Scope, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
	default:
		return nil, application.ErrValidation
	}
}
func (r *Repository) Dashboard(ctx context.Context, filter platform.ScopeFilter) (domain.Dashboard, error) {
	result := domain.Dashboard{StatusCounts: map[string]int{}}
	// 统计口径必须与列表/详情一致：先派生唯一状态，再计数，避免仪表盘与项目列表对不上。
	var projects []projectRecord
	if err := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project").Select("id, status, supplement_status").Find(&projects).Error; err != nil {
		return result, err
	}
	inputs, err := r.projectStatusInputs(ctx, filter, projectIDsOf(projects))
	if err != nil {
		return result, err
	}
	for _, project := range projects {
		status := domain.DeriveProjectStatus(inputs[project.ID], project.SupplementStatus, project.Status)
		result.StatusCounts[status]++
		result.ProjectCount++
		if status != domain.ProjectStatusCompleted {
			result.InFlightProjects++
		}
		// 风险口径改为派生状态：异常处理中或已终止的项目。
		if domain.IsRiskProjectStatus(status) {
			result.RiskProjects++
		}
	}
	var count int64
	if err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Count(&count).Error; err != nil {
		return result, err
	}
	result.ServiceItems = int(count)
	return result, nil
}

func unique(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func projectFromRecord(r projectRecord) domain.Project {
	return domain.Project{TenantID: r.TenantID, OwnerOrgID: r.OwnerOrgID, ID: r.ID, Name: r.Name, Customer: r.Customer, CustomerID: r.CustomerID, Contract: r.Contract, ContractVersion: r.ContractVersion, SupplementStatus: r.SupplementStatus, Services: r.Services, Category: r.Category, Team: r.Team, Manager: r.Manager, OwnerIdentityID: r.OwnerIdentityID, ManagerIdentityID: r.ManagerIdentityID, Status: r.Status, Progress: r.Progress, Due: r.Due, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func applyProjectScope(query *gorm.DB, filter platform.ScopeFilter, alias string) *gorm.DB {
	query = query.Where(alias+".tenant_id = ?", filter.TenantID)
	if filter.AllowAll {
		return query
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 8)
	if len(filter.OrganizationIDs) > 0 {
		conditions = append(conditions, alias+".owner_org_id IN ?")
		args = append(args, filter.OrganizationIDs)
	}
	if len(filter.ProjectIDs) > 0 {
		conditions = append(conditions, alias+".id IN ?")
		args = append(args, filter.ProjectIDs)
	}
	if filter.AllowSelf {
		conditions = append(conditions, "("+alias+".owner_identity_id = ? OR "+alias+".manager_identity_id = ? OR EXISTS (SELECT 1 FROM pm_service_item scope_item WHERE scope_item.tenant_id = "+alias+".tenant_id AND scope_item.project_id = "+alias+".id AND (scope_item.team_lead_id = ? OR scope_item.project_manager_id = ? OR JSON_CONTAINS(scope_item.engineer_ids, JSON_QUOTE(?)))))")
		args = append(args, filter.IdentityID, filter.IdentityID, filter.IdentityID, filter.IdentityID, filter.IdentityID)
	}
	if len(conditions) == 0 {
		return query.Where("1 = 0")
	}
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func applyServiceItemScope(query, subqueryDB *gorm.DB, filter platform.ScopeFilter) *gorm.DB {
	projects := applyProjectScope(subqueryDB.Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	return query.Where("pm_service_item.tenant_id = ? AND pm_service_item.project_id IN (?)", filter.TenantID, projects)
}
func serviceFromRecord(r serviceItemRecord) domain.ServiceItem {
	item := domain.ServiceItem{TenantID: r.TenantID, ID: r.ID, ProjectID: r.ProjectID, SourceServiceID: r.SourceServiceID, Batch: r.Batch, Site: r.Site, Category: r.Category, Requirement: r.Requirement, System: r.System, SystemLevel: r.SystemLevel, Special: r.Special, TestMode: r.TestMode, TeamLeadID: r.TeamLeadID, ProjectManagerID: r.ProjectManagerID, ConflictStatus: r.ConflictStatus, TechReviewStatus: r.TechReviewStatus, TechReviewedBy: r.TechReviewedBy, TechReviewComment: r.TechReviewComment, ReportStatus: r.ReportStatus, ReportUpdatedBy: r.ReportUpdatedBy, Status: r.Status}
	_ = json.Unmarshal(r.EngineerIDs, &item.EngineerIDs)
	_ = json.Unmarshal(r.EquipmentIDs, &item.EquipmentIDs)
	_ = json.Unmarshal(r.RequiredCodes, &item.RequiredCodes)
	if r.PlannedStart != nil {
		item.PlannedStart = r.PlannedStart.Format(time.RFC3339)
	}
	if r.PlannedEnd != nil {
		item.PlannedEnd = r.PlannedEnd.Format(time.RFC3339)
	}
	if r.TechReviewedAt != nil {
		item.TechReviewedAt = r.TechReviewedAt.Format(time.RFC3339)
	}
	if r.ReportUpdatedAt != nil {
		item.ReportUpdatedAt = r.ReportUpdatedAt.Format(time.RFC3339)
	}
	return item
}
func implPlanFromRecord(r implPlanRecord) domain.ImplementationPlan {
	plan := domain.ImplementationPlan{SitePlan: r.SitePlan, PenetrationTestPlan: r.PenetrationTestPlan, AuthDocNo: r.AuthDocNo, AuthScope: r.AuthScope, TestScope: r.TestScope, TestWindow: r.TestWindow, EmergencyContact: r.EmergencyContact, RollbackPlan: r.RollbackPlan, Personnel: []domain.PlanResource{}, Equipment: []domain.PlanResource{}}
	// 清单解析失败的历史数据不应让整个计划读不出来：降级为空清单，其余字段照常返回。
	plan.Personnel = decodePlanResources(r.Personnel, plan.Personnel)
	plan.Equipment = decodePlanResources(r.Equipment, plan.Equipment)
	if r.PlannedStart != nil {
		plan.PlannedStart = r.PlannedStart.Format(time.RFC3339)
	}
	if r.PlannedEnd != nil {
		plan.PlannedEnd = r.PlannedEnd.Format(time.RFC3339)
	}
	if r.AuthStart != nil {
		plan.AuthStart = r.AuthStart.Format(time.RFC3339)
	}
	if r.AuthEnd != nil {
		plan.AuthEnd = r.AuthEnd.Format(time.RFC3339)
	}
	return plan
}

// decodePlanResources 解析实施计划/准备阶段保存的资源快照；解析失败时返回传入的兜底空列表，
// 不让历史脏数据把整个计划读不出来。
func decodePlanResources(raw []byte, fallback []domain.PlanResource) []domain.PlanResource {
	if len(raw) == 0 {
		return fallback
	}
	var resources []domain.PlanResource
	if err := json.Unmarshal(raw, &resources); err != nil || resources == nil {
		return fallback
	}
	return resources
}

// ListEquipmentReservations 读取同租户其他服务项已登记的设备占用（实施准备阶段的设备清单），
// 供占用冲突校验使用。占用区间取自行级使用时段，留空表示全程（计划起止）。
func (r *Repository) ListEquipmentReservations(ctx context.Context, tenantID, excludeServiceItemID string) ([]domain.EquipmentReservation, error) {
	var rows []struct {
		ServiceItemID string
		ProjectID     string
		Customer      string
		Equipment     []byte
		PlannedStart  *time.Time
		PlannedEnd    *time.Time
	}
	query := r.db.WithContext(ctx).Table("pm_impl_plan AS plan").
		Select("plan.service_item_id, item.project_id, project.customer, plan.equipment, plan.planned_start, plan.planned_end").
		Joins("JOIN pm_service_item AS item ON item.tenant_id = plan.tenant_id AND item.id = plan.service_item_id").
		Joins("LEFT JOIN pm_project AS project ON project.tenant_id = item.tenant_id AND project.id = item.project_id").
		Where("plan.tenant_id = ? AND plan.equipment IS NOT NULL", tenantID).
		// 只有真正持有设备的服务项才算占用：被偏离评审打回「待实施」或已终止的项，
		// 计划里虽然还留着设备清单，但设备已经不该继续被它锁住。
		Where("item.status IN ?", []string{
			domain.ProjectStatusPreparing, domain.ProjectStatusInProgress,
			domain.ProjectStatusException, domain.ProjectStatusFieldCompleted,
		})
	if excludeServiceItemID != "" {
		query = query.Where("plan.service_item_id <> ?", excludeServiceItemID)
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	reservations := []domain.EquipmentReservation{}
	for _, row := range rows {
		var resources []domain.PlanResource
		if err := json.Unmarshal(row.Equipment, &resources); err != nil {
			continue
		}
		planStart, planEnd := "", ""
		if row.PlannedStart != nil {
			planStart = row.PlannedStart.Format("2006-01-02")
		}
		if row.PlannedEnd != nil {
			planEnd = row.PlannedEnd.Format("2006-01-02")
		}
		for _, resource := range resources {
			// 已归还的设备行保留历史，但不再占用设备，也不再算「不在公司」。
			if strings.TrimSpace(resource.ReturnedAt) != "" {
				continue
			}
			reservation := domain.EquipmentReservation{
				ServiceItemID: row.ServiceItemID, ProjectID: row.ProjectID, Customer: row.Customer,
				ResourceID: resource.ResourceID, ResourceName: resource.ResourceName,
				WindowStart: firstValue(resource.WindowStart, planStart), WindowEnd: firstValue(resource.WindowEnd, planEnd),
			}
			if reservation.WindowStart == "" || reservation.WindowEnd == "" {
				continue
			}
			reservations = append(reservations, reservation)
		}
	}
	return reservations, nil
}

func ruleFromRow(r ruleRow) domain.Rule {
	return domain.Rule{TenantID: r.TenantID, ID: r.ID, Kind: r.Kind, Name: r.Name, Scope: r.Scope, Trigger: r.Trigger, CheckType: r.CheckType, Threshold: r.Threshold, Target: r.Target, RoleCode: r.RoleCode, FieldName: r.FieldName, AccessLevel: r.AccessLevel, Status: r.Status, DeadlineHours: r.DeadlineHours, RemindHours: r.RemindHours, Enabled: r.Enabled, Updated: r.UpdatedAt.Format("2006-01-02 15:04")}
}
func firstValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
