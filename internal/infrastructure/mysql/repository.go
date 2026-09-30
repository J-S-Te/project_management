package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

// derivedProjectStatusJoin 是按项目聚合服务项推进等级的 LEFT JOIN 子查询：
// total_items 为服务项总数，terminated_items 为其中已终止的数量，
// lowest_rank 取非终止服务项的最滞后推进等级（已终止计为哨兵值 9，
// 只有「全部终止」时才会胜出，而该情形由外层 CASE 的终止分支先行接住）。
// 等级分支由 domain.ServiceItemStatusStages / ServiceItemReportStages 生成，
// 与 domain.DeriveProjectStatus 共用同一套状态表，避免两处实现漂移。
// 注意：SQL 的 TRIM 只去除空格，与 Go strings.TrimSpace 在制表符/换行等
// 极端输入上存在理论差异；状态词汇表由本系统写入、不含此类字符。
func derivedProjectStatusJoin() string {
	var rankCases strings.Builder
	for _, stage := range domain.ServiceItemStatusStages() {
		rankCases.WriteString("WHEN TRIM(aggregated.status) IN (")
		rankCases.WriteString(quotedSQLList(stage.Statuses))
		rankCases.WriteString(") THEN ")
		rankCases.WriteString(strconv.Itoa(stage.Rank))
		rankCases.WriteString(" ")
	}
	var reportCases strings.Builder
	for _, stage := range domain.ServiceItemReportStages() {
		reportCases.WriteString("WHEN UPPER(TRIM(COALESCE(aggregated.report_status, ''))) IN (")
		reportCases.WriteString(quotedSQLList(stage.Statuses))
		reportCases.WriteString(") THEN ")
		reportCases.WriteString(strconv.Itoa(stage.Rank))
		reportCases.WriteString(" ")
	}
	join := `LEFT JOIN (
		SELECT aggregated.project_id AS project_id,
		       COUNT(*) AS total_items,
		       SUM(CASE WHEN TRIM(aggregated.status) = '` + domain.ProjectStatusTerminated + `' THEN 1 ELSE 0 END) AS terminated_items,
		       MIN(CASE
		           WHEN TRIM(aggregated.status) = '` + domain.ProjectStatusTerminated + `' THEN 9
		           WHEN TRIM(aggregated.status) = '` + domain.ProjectStatusFieldCompleted + `' THEN CASE
		               ` + reportCases.String() + `
		               ELSE 6
		           END
		           ` + rankCases.String() + `
		           ELSE 0
		       END) AS lowest_rank
		FROM pm_service_item AS aggregated
		WHERE aggregated.tenant_id = ?
		GROUP BY aggregated.project_id
	) AS derived_status ON derived_status.project_id = pm_project.id`
	return join
}

// derivedProjectStatusPredicate 按派生状态还原表达式过滤项目，短路顺序与
// domain.DeriveProjectStatus 完全一致：补充协议 → 无服务项回退存储状态 →
// 全部终止 → 最滞后推进等级对应的线性节点。该条件只用于 COUNT 与 LIMIT/OFFSET
// 的分页下推（AUD-2026-028）；返回行的派生状态仍由 Go 侧 applyDerivedProjectMetrics 计算。
func derivedProjectStatusPredicate(wanted string) string {
	nodes := domain.ProjectStatusNodes()
	nodeList := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeList = append(nodeList, "'"+strings.ReplaceAll(node, "'", "''")+"'")
	}
	return `CASE
		WHEN UPPER(TRIM(COALESCE(pm_project.supplement_status, ''))) = 'REQUIRED' THEN '` + domain.ProjectStatusSupplementRequired + `'
		WHEN COALESCE(derived_status.total_items, 0) = 0 THEN CASE
			WHEN TRIM(COALESCE(pm_project.status, '')) <> '' THEN TRIM(pm_project.status)
			ELSE '` + domain.ProjectStatusPendingDecomposition + `'
		END
		WHEN derived_status.terminated_items = derived_status.total_items THEN '` + domain.ProjectStatusTerminated + `'
		ELSE ELT(COALESCE(derived_status.lowest_rank, 0) + 1, ` + strings.Join(nodeList, ", ") + `)
	END = ?`
}

// quotedSQLList 把字符串列表渲染为转义后的 SQL 字面量清单（仅用于编译期常量，
// 用户输入一律走参数占位符）。
func quotedSQLList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+strings.ReplaceAll(value, "'", "''")+"'")
	}
	return strings.Join(quoted, ", ")
}

// ListProjects 把租户/范围/关键字/派生状态过滤与分页全部下推到 SQL：
// 主查询 COUNT 取 total，LIMIT/OFFSET 只取本页记录；服务项聚合补查仅覆盖本页 ID。
// 排序保持 id DESC 与全量口径一致；派生状态（status/progress/risk）仍只在
// Go 侧对本页记录计算，输出结构与历史全量口径完全一致（AUD-2026-028）。
func (r *Repository) ListProjects(ctx context.Context, filter platform.ScopeFilter, q, status string, page, pageSize int) ([]domain.Project, int, error) {
	base := applyProjectScope(r.db.WithContext(ctx).Model(&projectRecord{}), filter, "pm_project")
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + q + "%"
		base = base.Where("id LIKE ? OR name LIKE ? OR customer LIKE ? OR contract LIKE ? OR category LIKE ? OR manager LIKE ?", like, like, like, like, like, like)
	}
	wanted := strings.TrimSpace(status)
	if wanted != "" {
		base = base.Joins(derivedProjectStatusJoin(), filter.TenantID).Where(derivedProjectStatusPredicate(wanted), wanted)
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list := base.Session(&gorm.Session{}).Order("id DESC")
	if pageSize > 0 {
		list = list.Offset(max(0, (page-1)*pageSize)).Limit(pageSize)
	}
	var records []projectRecord
	if err := list.Find(&records).Error; err != nil {
		return nil, 0, err
	}
	inputs, err := r.projectStatusInputs(ctx, filter.TenantID, projectIDsOf(records))
	if err != nil {
		return nil, 0, err
	}
	items := make([]domain.Project, 0, len(records))
	for _, record := range records {
		project := projectFromRecord(record)
		applyDerivedProjectMetrics(&project, inputs[record.ID])
		// SQL 分页已按派生状态筛选；这里用 Go 侧派生函数保留同一过滤作为最终口径：
		// 若两套实现漂移，宁可少返回行（等价性集成测试可发现），也不返回状态不符的行。
		if wanted != "" && project.Status != wanted {
			continue
		}
		items = append(items, project)
	}
	return items, int(total), nil
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
	inputs, err := r.projectStatusInputs(ctx, filter.TenantID, []string{record.ID})
	if err != nil {
		return domain.Project{}, err
	}
	applyDerivedProjectMetrics(&project, inputs[record.ID])
	return project, nil
}

// applyDerivedProjectMetrics 用同一份服务项投影刷新项目的唯一状态与进度。
// 两者必须同源：只派生状态而让进度读存储列，会让确认拆解后的项目显示「待分配 · 0%」。
func applyDerivedProjectMetrics(project *domain.Project, items []domain.ProjectStatusItem) {
	project.Status = domain.DeriveProjectStatus(items, project.SupplementStatus, project.Status)
	project.Progress = domain.DeriveProjectProgress(items)
	project.Risk = domain.IsRiskProject(project.Status, items)
}

// projectIDsOf 提取项目主键，供后续按项目聚合服务项状态。
func projectIDsOf(records []projectRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// projectStatusInputs 按项目聚合全部服务项状态，作为派生项目唯一状态的输入。
// 项目范围仅控制项目本身是否可见；一旦项目可见，状态、进度与风险必须基于完整
// 服务项集合派生，不能因当前角色只看到部分服务项而得到不同的项目阶段。
func (r *Repository) projectStatusInputs(ctx context.Context, tenantID string, projectIDs []string) (map[string][]domain.ProjectStatusItem, error) {
	result := map[string][]domain.ProjectStatusItem{}
	if len(projectIDs) == 0 {
		return result, nil
	}
	var rows []projectStatusRow
	if err := projectStatusInputQuery(r.db.WithContext(ctx), tenantID, projectIDs).Scan(&rows).Error; err != nil {
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

// projectStatusInputQuery 构造项目状态的完整服务项投影。禁止复用服务项可见范围：
// 否则同一项目会随登录角色不同而落入不同状态。
func projectStatusInputQuery(db *gorm.DB, tenantID string, projectIDs []string) *gorm.DB {
	return db.Model(&serviceItemRecord{}).
		Select("project_id, status, report_status").
		Where("tenant_id = ? AND project_id IN ?", tenantID, projectIDs)
}
func (r *Repository) CreateProject(ctx context.Context, item domain.Project) error {
	err := r.db.WithContext(ctx).Create(&projectRecord{ID: item.ID, TenantID: item.TenantID, OwnerOrgID: item.OwnerOrgID, Name: item.Name, Customer: item.Customer, CustomerID: item.CustomerID, Contract: item.Contract, ContractID: item.ContractID, ContractVersion: item.ContractVersion, SupplementStatus: firstValue(item.SupplementStatus, "NONE"), Services: item.Services, Category: item.Category, Team: item.Team, Manager: item.Manager, OwnerIdentityID: item.OwnerIdentityID, ManagerIdentityID: item.ManagerIdentityID, Status: item.Status, Progress: item.Progress, Due: item.Due, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}).Error
	if err != nil && isDuplicateKey(err) {
		// 无服务项的手动创建同样受唯一键约束，理由见 CreateProjectWithServiceItems。
		return application.ErrDuplicateProject
	}
	return err
}

// ListServiceItems 把租户/范围/项目过滤与分页下推到 SQL：COUNT 取 total、
// LIMIT/OFFSET 只取本页记录（AUD-2026-028）。实施计划与渗透工作包两次 IN 补查
// 原先是两次全表拉取，现只查本页 ID 集合；计划/包与状态派生都是服务项内部属性，
// 仅按本页 ID 补齐与全量口径逐行一致。排序保持 id 升序不变。
func (r *Repository) ListServiceItems(ctx context.Context, filter platform.ScopeFilter, projectID string, page, pageSize int) ([]domain.ServiceItem, int, error) {
	base := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter)
	if projectID != "" {
		base = base.Where("project_id = ?", projectID)
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list := base.Session(&gorm.Session{}).Order("id")
	if pageSize > 0 {
		list = list.Offset(max(0, (page-1)*pageSize)).Limit(pageSize)
	}
	var records []serviceItemRecord
	if err := list.Find(&records).Error; err != nil {
		return nil, 0, err
	}
	plans := map[string]domain.ImplementationPlan{}
	penetrationPackages := map[string]domain.PenetrationWorkPackage{}
	if len(records) > 0 {
		ids := make([]string, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.ID)
		}
		var rows []implPlanRecord
		if err := r.db.WithContext(ctx).Where("tenant_id=? AND service_item_id IN ?", filter.TenantID, ids).Find(&rows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range rows {
			plan := implPlanFromRecord(row)
			plans[row.ServiceItemID] = plan
		}
		var packageRows []penetrationWorkPackageRecord
		if err := r.db.WithContext(ctx).Where("tenant_id=? AND parent_service_item_id IN ?", filter.TenantID, ids).Find(&packageRows).Error; err != nil {
			return nil, 0, err
		}
		for _, row := range packageRows {
			penetrationPackages[row.ParentServiceItemID] = penetrationWorkPackageFromRecord(row)
		}
	}
	items := make([]domain.ServiceItem, 0, len(records))
	for _, record := range records {
		item := serviceFromRecord(record)
		if plan, ok := plans[record.ID]; ok {
			item.ImplementationPlan = &plan
		}
		if workPackage, ok := penetrationPackages[record.ID]; ok {
			item.PenetrationWorkPackage = &workPackage
		}
		items = append(items, item)
	}
	return items, int(total), nil
}
func (r *Repository) GetServiceItem(ctx context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	var record serviceItemRecord
	err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Where("pm_service_item.id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ServiceItem{}, application.ErrNotFound
	}
	if err != nil {
		return domain.ServiceItem{}, err
	}
	item := serviceFromRecord(record)
	var workPackage penetrationWorkPackageRecord
	packageErr := r.db.WithContext(ctx).Where("tenant_id=? AND parent_service_item_id=?", filter.TenantID, id).Take(&workPackage).Error
	if packageErr == nil {
		item.PenetrationWorkPackage = pointerTo(penetrationWorkPackageFromRecord(workPackage))
	} else if !errors.Is(packageErr, gorm.ErrRecordNotFound) {
		return domain.ServiceItem{}, packageErr
	}
	return item, nil
}

func pointerTo[T any](value T) *T { return &value }

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
			Updates(map[string]any{"status": "待分配", "status_changed_at": now, "updated_at": now, "updated_by": actor})
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
			if pending > 0 {
				continue
			}
			// 拆解全部确认即退出补充协议分支：supplement_status 是派生状态的短路条件，
			// 不回写 NONE 会让项目永久停在「补充协议处理中」，此后任何推进都不再改变状态。
			if err := tx.Model(&projectRecord{}).Where("tenant_id = ? AND id = ?", tenant, projectID).
				Updates(map[string]any{"supplement_status": "NONE", "updated_at": now}).Error; err != nil {
				return err
			}
			// 状态与进度一律走单一派生口径，不再手写目标状态（手写会让进度停在 0）。
			if err := syncProjectStatusColumn(tx, tenant, projectID); err != nil {
				return err
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

// ruleKinds 真实配置表对应的 kind 标识。列表中顺序即 ListRules 不指定 kind 时的合并顺序。
var ruleKinds = []string{"split-rules", "warning-rules", "automations", "permissions", "sla", "standards", "capability-codes"}

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
	case "capability-codes":
		return "pm_capability_code"
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
		return item, translateRuleWriteError(item.Kind, err)
	}
	// 规则主键是数据库自增生成的：必须用 Create 回填后的真实 ID 回读。
	// 此前用的是入参 ID——客户端新建时不传 ID（值为 0），GetRule 因此判定「不存在」，
	// 把已经成功写入的创建报成 404 资源不存在；用户看到失败会重复点击，每次再插一条重复规则。
	createdID := ruleRecordPrimaryKey(record)
	if createdID == 0 {
		return item, application.ErrNotFound
	}
	return r.GetRule(ctx, item.TenantID, item.Kind, createdID)
}

// ruleRecordPrimaryKey 取出创建后由数据库回填的自增主键。
// 六种规则记录各自成表，但主键都是 int64 自增，这里显式列出以免遗漏新增类型。
func ruleRecordPrimaryKey(record any) int64 {
	switch typed := record.(type) {
	case *splitRuleRecord:
		return typed.ID
	case *warningRuleRecord:
		return typed.ID
	case *automationRecord:
		return typed.ID
	case *fieldPermissionRecord:
		return typed.ID
	case *slaRecord:
		return typed.ID
	case *standardRecord:
		return typed.ID
	case *capabilityCodeRecord:
		return typed.ID
	default:
		return 0
	}
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

// DeleteRule 物理删除一条配置规则。各类配置表分别成表，因此删除必须同时限定目标表
// （由 kind 决定）、租户与主键：只按主键删除会跨类型误删，主键在各表里分别自增。
// 回读发生在删除之前，所以「不存在」只会在确实没有命中行时返回——创建接口曾因回读用
// 客户端传入的 0 主键把成功写入报成 404，删除不能重复这类错误。
func (r *Repository) DeleteRule(ctx context.Context, tenant, kind string, id int64) (domain.Rule, error) {
	table := ruleTable(kind)
	if table == "" {
		return domain.Rule{}, application.ErrValidation
	}
	var removed domain.Rule
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row ruleRow
		if err := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == 0 {
			return application.ErrNotFound
		}
		result := tx.Table(table).Where("tenant_id = ? AND id = ?", tenant, id).Delete(&ruleRow{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return application.ErrNotFound
		}
		removed = ruleFromRow(row)
		return nil
	})
	if err != nil {
		return domain.Rule{}, translateRuleWriteError(kind, err)
	}
	return removed, nil
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
		return domain.Rule{}, translateRuleWriteError(kind, err)
	}
	return r.GetRule(ctx, tenant, kind, id)
}

func translateRuleWriteError(kind string, err error) error {
	var mysqlError *drivermysql.MySQLError
	if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 {
		return err
	}
	switch kind {
	case "capability-codes":
		return application.ValidationError("同一类型的资质 / 能力编码已存在")
	case "automations", "permissions", "sla":
		return application.ConflictError("相同生效条件的启用规则已存在，请直接编辑或停用原配置")
	default:
		return err
	}
}

// CountCapabilityCodeReferences 统计目录编码在能力档案、服务项和检测类别中的引用。
// capability_codes / 服务项 required_codes 是 JSON 数组；检测类别 required_codes
// 是兼容历史的逗号分隔字符串。
// 检测类别的必检码只指向 PERSON 资质，设备编码不计入该引用。
func (r *Repository) CountCapabilityCodeReferences(ctx context.Context, tenant, resourceType, code string) (int64, error) {
	resourceType = strings.ToUpper(strings.TrimSpace(resourceType))
	code = strings.ToUpper(strings.TrimSpace(code))
	var capabilityCount int64
	if err := r.db.WithContext(ctx).Table("pm_capability").
		Where("tenant_id = ? AND UPPER(TRIM(resource_type)) = ?", tenant, resourceType).
		Where("EXISTS (SELECT 1 FROM JSON_TABLE(pm_capability.capability_codes, '$[*]' COLUMNS(code VARCHAR(512) PATH '$')) AS referenced_code WHERE UPPER(TRIM(referenced_code.code)) = ?)", code).
		Count(&capabilityCount).Error; err != nil {
		return 0, err
	}
	if resourceType != "PERSON" {
		return capabilityCount, nil
	}
	var serviceItemCount int64
	if err := r.db.WithContext(ctx).Table("pm_service_item").
		Where("tenant_id = ?", tenant).
		Where("EXISTS (SELECT 1 FROM JSON_TABLE(COALESCE(pm_service_item.required_codes, JSON_ARRAY()), '$[*]' COLUMNS(code VARCHAR(512) PATH '$')) AS referenced_code WHERE UPPER(TRIM(referenced_code.code)) = ?)", code).
		Count(&serviceItemCount).Error; err != nil {
		return 0, err
	}
	var categoryCount int64
	if err := r.db.WithContext(ctx).Table("pm_detection_category").
		Where("tenant_id = ?", tenant).
		Where("FIND_IN_SET(?, UPPER(REPLACE(REPLACE(REPLACE(required_codes, '，', ','), '；', ','), ';', ','))) > 0", code).
		Count(&categoryCount).Error; err != nil {
		return 0, err
	}
	return capabilityCount + serviceItemCount + categoryCount, nil
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
	case "capability-codes":
		// 编码和资源类型是历史能力档案的语义键，创建后不可改。
		// 如需新编码应新建目录项，旧项禁用后仍保留历史引用。
		columns = map[string]any{"name": item.Name, "enabled": item.Enabled}
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
		return domain.Rule{}, translateRuleWriteError(kind, err)
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
	case "capability-codes":
		return &capabilityCodeRecord{TenantID: item.TenantID, Kind: item.Kind, Name: item.Name, Scope: item.Scope, CheckType: item.CheckType, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now, UpdatedBy: item.UpdatedBy}, nil
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
	inputs, err := r.projectStatusInputs(ctx, filter.TenantID, projectIDsOf(projects))
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
		// 风险口径与列表/详情共用同一函数：派生状态为异常处理中/已终止，
		// 或项目内存在已终止服务项（部分终止的项目派生状态仍是「已完成」）。
		if domain.IsRiskProject(status, inputs[project.ID]) {
			result.RiskProjects++
		}
		if len(domain.UnknownServiceItemStatuses(inputs[project.ID])) > 0 {
			result.UnknownStatusItems++
		}
	}
	var count int64
	if err := applyServiceItemScope(r.db.WithContext(ctx).Model(&serviceItemRecord{}), r.db.WithContext(ctx), filter).Count(&count).Error; err != nil {
		return result, err
	}
	result.ServiceItems = int(count)
	return result, nil
}

// CountExistingContractReferences matches the stable contract ID written by
// current integrations and the (number, version) key retained by legacy/manual
// projects. This makes the KPI agree with the new-project selector while old
// rows are being backfilled with contract_id.
func (r *Repository) CountExistingContractReferences(ctx context.Context, tenantID string, references []platform.ApprovedContract) (int, error) {
	if len(references) == 0 {
		return 0, nil
	}
	records, err := r.existingContractReferenceRecords(ctx, tenantID, references)
	if err != nil {
		return 0, err
	}
	return len(matchedContractReferenceKeys(records, references)), nil
}

// FilterUnreferencedApprovedContracts is the authoritative source for the new
// project picker. It intentionally ignores the current user's project scope:
// contract-to-project uniqueness is tenant-wide, so projects hidden from the
// caller must still remove their contracts from the selectable list.
func (r *Repository) FilterUnreferencedApprovedContracts(ctx context.Context, tenantID string, references []platform.ApprovedContract) ([]platform.ApprovedContract, error) {
	if len(references) == 0 {
		return []platform.ApprovedContract{}, nil
	}
	records, err := r.existingContractReferenceRecords(ctx, tenantID, references)
	if err != nil {
		return nil, err
	}
	matched := matchedContractReferenceKeys(records, references)
	available := make([]platform.ApprovedContract, 0, len(references)-len(matched))
	for _, reference := range references {
		if !matched[approvedContractReferenceKey(reference)] {
			available = append(available, reference)
		}
	}
	return available, nil
}

func (r *Repository) existingContractReferenceRecords(ctx context.Context, tenantID string, references []platform.ApprovedContract) ([]projectRecord, error) {
	ids := make([]string, 0, len(references))
	numbers := make([]string, 0, len(references))
	for _, reference := range references {
		if id := strings.TrimSpace(reference.ID); id != "" {
			ids = append(ids, id)
		}
		if number := strings.TrimSpace(reference.Number); number != "" {
			numbers = append(numbers, number)
		}
	}
	var records []projectRecord
	err := r.db.WithContext(ctx).Model(&projectRecord{}).
		Select("id", "contract", "contract_id", "contract_version").
		Where("tenant_id = ?", tenantID).
		Where("contract_id IN ? OR contract IN ?", ids, numbers).
		Find(&records).Error
	return records, err
}

func countMatchedContractReferences(records []projectRecord, references []platform.ApprovedContract) int {
	return len(matchedContractReferenceKeys(records, references))
}

func approvedContractReferenceKey(reference platform.ApprovedContract) string {
	return strings.TrimSpace(reference.ID) + "\x1f" + strconv.FormatUint(reference.Version, 10)
}

func matchedContractReferenceKeys(records []projectRecord, references []platform.ApprovedContract) map[string]bool {
	referenceByID := make(map[string]string, len(references))
	referenceByLegacyKey := make(map[string]string, len(references))
	for _, reference := range references {
		id := strings.TrimSpace(reference.ID)
		if id == "" {
			continue
		}
		key := approvedContractReferenceKey(reference)
		referenceByID[id+"\x1f"+strconv.FormatUint(reference.Version, 10)] = key
		referenceByLegacyKey[strings.TrimSpace(reference.Number)+"\x1f"+strconv.FormatUint(reference.Version, 10)] = key
	}
	matched := make(map[string]bool, len(references))
	for _, record := range records {
		if key := referenceByID[strings.TrimSpace(record.ContractID)+"\x1f"+strings.TrimSpace(record.ContractVersion)]; key != "" {
			matched[key] = true
			continue
		}
		if key := referenceByLegacyKey[strings.TrimSpace(record.Contract)+"\x1f"+strings.TrimSpace(record.ContractVersion)]; key != "" {
			matched[key] = true
		}
	}
	return matched
}

func unique(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func projectFromRecord(r projectRecord) domain.Project {
	return domain.Project{TenantID: r.TenantID, OwnerOrgID: r.OwnerOrgID, ID: r.ID, Name: r.Name, Customer: r.Customer, CustomerID: r.CustomerID, Contract: r.Contract, ContractID: r.ContractID, ContractVersion: r.ContractVersion, SupplementStatus: r.SupplementStatus, Services: r.Services, Category: r.Category, Team: r.Team, Manager: r.Manager, OwnerIdentityID: r.OwnerIdentityID, ManagerIdentityID: r.ManagerIdentityID, Status: r.Status, Progress: r.Progress, Due: r.Due, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Version: r.Version}
}

func applyProjectScope(query *gorm.DB, filter platform.ScopeFilter, alias string) *gorm.DB {
	query = query.Where(alias+".tenant_id = ?", filter.TenantID)
	if filter.AssignedItemsOnly {
		return query.Where("EXISTS (SELECT 1 FROM pm_service_item scope_item WHERE scope_item.tenant_id = "+alias+".tenant_id AND scope_item.project_id = "+alias+".id AND scope_item.archived_at IS NULL AND (scope_item.team_lead_id = ? OR scope_item.project_manager_id = ? OR JSON_CONTAINS(scope_item.engineer_ids, JSON_QUOTE(?))))", filter.UserID, filter.UserID, filter.UserID)
	}
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
		conditions = append(conditions, "("+alias+".owner_identity_id = ? OR "+alias+".manager_identity_id = ? OR EXISTS (SELECT 1 FROM pm_service_item scope_item WHERE scope_item.tenant_id = "+alias+".tenant_id AND scope_item.project_id = "+alias+".id AND scope_item.archived_at IS NULL AND (scope_item.team_lead_id = ? OR scope_item.project_manager_id = ? OR JSON_CONTAINS(scope_item.engineer_ids, JSON_QUOTE(?)))))")
		args = append(args, filter.IdentityID, filter.IdentityID, filter.UserID, filter.UserID, filter.UserID)
	}
	if len(conditions) == 0 {
		return query.Where("1 = 0")
	}
	return query.Where("("+strings.Join(conditions, " OR ")+")", args...)
}

func applyServiceItemScope(query, subqueryDB *gorm.DB, filter platform.ScopeFilter) *gorm.DB {
	if filter.AssignedItemsOnly {
		return query.Where("pm_service_item.tenant_id = ? AND pm_service_item.archived_at IS NULL AND (pm_service_item.team_lead_id = ? OR pm_service_item.project_manager_id = ? OR JSON_CONTAINS(pm_service_item.engineer_ids, JSON_QUOTE(?)))", filter.TenantID, filter.UserID, filter.UserID, filter.UserID)
	}
	projects := applyProjectScope(subqueryDB.Table("pm_project AS scope_project").Select("scope_project.id"), filter, "scope_project")
	return query.Where("pm_service_item.tenant_id = ? AND pm_service_item.project_id IN (?)", filter.TenantID, projects)
}
func serviceFromRecord(r serviceItemRecord) domain.ServiceItem {
	item := domain.ServiceItem{TenantID: r.TenantID, ID: r.ID, ProjectID: r.ProjectID, SourceServiceID: r.SourceServiceID, Batch: r.Batch, Site: r.Site, SiteCode: r.SiteCode, Category: r.Category, Requirement: r.Requirement, System: r.System, SystemLevel: r.SystemLevel, SystemStandard: r.SystemStandard, Special: r.Special, TestMode: r.TestMode, TeamLeadID: r.TeamLeadID, ProjectManagerID: r.ProjectManagerID, ConflictStatus: r.ConflictStatus, TechReviewStatus: r.TechReviewStatus, TechReviewedBy: r.TechReviewedBy, TechReviewComment: r.TechReviewComment, ReportStatus: r.ReportStatus, ReportUpdatedBy: r.ReportUpdatedBy, ReportRevision: r.ReportRevision, ReportPreparedBy: r.ReportPreparedBy, ReportReviewedBy: r.ReportReviewedBy, ReportIssuedBy: r.ReportIssuedBy, Status: r.Status, Version: r.Version}
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
