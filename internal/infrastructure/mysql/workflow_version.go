package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type workflowRevisionRecord struct {
	TenantID      string `gorm:"primaryKey;size:64"`
	ProjectID     string `gorm:"primaryKey;size:32"`
	ServiceItemID string `gorm:"primaryKey;size:32"`
	Revision      uint64 `gorm:"not null"`
}

// TableName 返回流程修订记录使用的持久化表名。
// 参数：无。
// 返回值：正式迁移中使用的表名。
func (workflowRevisionRecord) TableName() string { return "pm_workflow_revision" }

type workflowVersionRow struct {
	Kind           string
	ID             string
	Project        string
	Version        uint64
	Revision       uint64
	RelatedVersion uint64
	Stage          string
	Substage       string
	UpdatedAt      string
}

// WorkflowVersion 用一条 MySQL 快照查询计算受权的项目、服务项及交付事件修订签名。
// 参数：ctx 为查询上下文，filter 为会话的数据范围，projectID 为可选项目编号。
// 返回值：确定性签名，查询或消费结果失败时返回错误。
func (r *Repository) WorkflowVersion(ctx context.Context, filter platform.ScopeFilter, projectID string) (string, error) {
	db := r.db.WithContext(ctx)
	projects := workflowVersionProjects(db, filter)
	items := applyServiceItemScope(db.Table("pm_service_item"), db, filter).
		Where("pm_service_item.archived_at IS NULL").
		Joins(`LEFT JOIN pm_workflow_revision AS rev ON rev.tenant_id=pm_service_item.tenant_id
 AND rev.project_id=pm_service_item.project_id AND rev.service_item_id=pm_service_item.id`).
		Joins(`LEFT JOIN pm_penetration_work_package AS pkg
 ON pkg.tenant_id=pm_service_item.tenant_id COLLATE utf8mb4_0900_ai_ci
 AND pkg.parent_service_item_id=pm_service_item.id COLLATE utf8mb4_0900_ai_ci`).
		Select(`'item' AS kind, pm_service_item.id, pm_service_item.project_id, pm_service_item.version,
 COALESCE(rev.revision, 0), COALESCE(pkg.version, 0), CAST(pm_service_item.status AS BINARY), CAST(pm_service_item.report_status AS BINARY),
 COALESCE(CAST(pm_service_item.updated_at AS BINARY), CAST('' AS BINARY))`)
	if projectID != "" {
		projects = projects.Where("pm_project.id = ?", projectID)
		items = items.Where("pm_service_item.project_id = ?", projectID)
	}
	rows, err := db.Raw("? UNION ALL ? ORDER BY kind, id", projects, items).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for rows.Next() {
		var row workflowVersionRow
		if err := rows.Scan(&row.Kind, &row.ID, &row.Project, &row.Version, &row.Revision,
			&row.RelatedVersion, &row.Stage, &row.Substage, &row.UpdatedAt); err != nil {
			return "", err
		}
		if err := encoder.Encode(row); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), rows.Err()
}

// workflowVersionProjects 读取可见项目汇总，执行角色不观察项目级事件的修订。
// 参数：db 为当前查询会话，filter 为可信租户及范围。
// 返回值：参与快照版本的项目投影查询。
func workflowVersionProjects(db *gorm.DB, filter platform.ScopeFilter) *gorm.DB {
	projects := applyProjectScope(db.Table("pm_project"), filter, "pm_project")
	revision := "0"
	stage := "pm_project.status"
	summary := "pm_project.progress"
	if filter.AssignedItemsOnly {
		// 项目汇总对当前读者可见，其他服务项修订仍只参与已有的汇总口径。
		projects = projects.Joins(derivedProjectStatusJoin(), filter.TenantID)
		stage = derivedProjectStatusExpression()
		summary = `CONCAT_WS(':', pm_project.progress, COALESCE(derived_status.lowest_report_phase, -1),
 COALESCE(derived_status.terminated_items > 0, 0))`
	} else {
		revision = "COALESCE(rev.revision, 0)"
		projects = projects.Joins(`LEFT JOIN pm_workflow_revision AS rev ON rev.tenant_id=pm_project.tenant_id
 AND rev.project_id=pm_project.id AND rev.service_item_id=''`)
	}
	return projects.Select(`'project' AS kind, pm_project.id, pm_project.id AS project_key, pm_project.version,
 ` + revision + `, 0, CAST(` + stage + ` AS BINARY), CAST(` + summary + ` AS BINARY), CAST('' AS BINARY)`)
}

// advanceWorkflowRevision 与交付事件同事务推进单个业务坐标的修订。
// 参数：tx 为业务事务，tenantID/projectID/itemID 为已解析的交付坐标，空 itemID 表示项目。
// 返回值：保存失败时返回错误，由调用事务回滚。
func advanceWorkflowRevision(tx *gorm.DB, tenantID, projectID, itemID string) error {
	if projectID == "" {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "project_id"}, {Name: "service_item_id"}},
		DoUpdates: clause.Assignments(map[string]any{"revision": gorm.Expr("revision + 1")}),
	}).Create(&workflowRevisionRecord{TenantID: tenantID, ProjectID: projectID, ServiceItemID: itemID, Revision: 1}).Error
}
