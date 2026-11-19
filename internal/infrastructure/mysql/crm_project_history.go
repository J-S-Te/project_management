package mysql

import (
	"context"
	"database/sql"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// CRMCustomerProjects 从同一只读事务查询总数、当页项目及派生状态。
// 参数：ctx 控制查询；tenant 和 customer 限制数据来源；page、pageSize 为受控分页。
// 返回值：项目摘要与总数；事务或投影失败时返回错误。
func (r *Repository) CRMCustomerProjects(
	ctx context.Context, tenant, customer string, page, pageSize int,
) ([]application.CRMProjectSummary, int64, error) {
	result := []application.CRMProjectSummary{}
	var total int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := crmCustomerProjectsQuery(tx, tenant, customer)
		if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return err
		}
		var records []projectRecord
		err := base.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error
		if err != nil || len(records) == 0 {
			return err
		}
		ids := projectIDsOf(records)
		reader := NewRepository(tx)
		active, _, err := reader.snapshotActiveItems(ctx, tenant, ids, 250_000)
		if err != nil {
			return err
		}
		updated, err := reader.snapshotSourceUpdatedAt(ctx, tenant, ids)
		if err != nil {
			return err
		}
		for _, record := range records {
			p := domain.NewProjectProjection(projectFromRecord(record), "", active[record.ID], updated[record.ID])
			result = append(result, application.CRMProjectSummary{
				ProjectID: p.ID, ProjectName: p.Name, ContractNo: p.Contract, Status: p.Status,
				Progress: p.Progress, Due: record.Due, SourceUpdatedAt: p.SourceUpdatedAt,
			})
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	return result, total, err
}

// crmCustomerProjectsQuery 只选择合同来源中保存 CRM 主键的项目。
// 参数：db 为当前查询会话，tenant 为签名租户，customer 为 CRM 客户主键。
// 返回值：可独立用于计数或分页的 GORM 查询。
func crmCustomerProjectsQuery(db *gorm.DB, tenant, customer string) *gorm.DB {
	return db.Model(&projectRecord{}).
		Where("tenant_id = ? AND customer_id = ? AND source_kind = ?", tenant, customer, "CONTRACT")
}
