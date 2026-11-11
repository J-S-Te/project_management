package mysql

import (
	"context"
	"database/sql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"strings"
	"time"
)

// ReadReportDashboard 在只读可重复读事务中聚合全部报告主体类型。
// 参数：ctx为取消上下文，tenant为签名身份绑定的唯一租户。
// 返回值：提交后的权威快照；查询、边界或提交失败时不返回可用快照。
func (r *Repository) ReadReportDashboard(ctx context.Context, tenant string) (domain.ReportDashboard, error) {
	empty := domain.ReportDashboard{}
	if tenant == "" || strings.TrimSpace(tenant) != tenant {
		return empty, application.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		return empty, tx.Error
	}
	defer tx.Rollback()
	value, err := readReportDashboard(tx, tenant)
	if err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if err = tx.Commit().Error; err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	return value, nil
}

// readReportDashboard 以有界分组结果和最多两个中位样本生成聚合。
// 参数：tx为同一只读事务，tenant为目标租户。
// 返回值：报告快照；异常状态基数或任一SQL失败时返回错误。
func readReportDashboard(tx *gorm.DB, tenant string) (domain.ReportDashboard, error) {
	value := domain.ReportDashboard{TenantID: tenant, SourceCode: "project_management",
		SnapshotAt:     time.Now().UTC().Format(time.RFC3339Nano),
		MissingMetrics: []string{"first_pass_rate", "open_complaints"},
		Basis:          "报告版本台账：ACTIVE版本按状态计数，VOID单列，SUPERSEDED不算作废；周期为完整且非负的prepared_at至issued_at，不推算首次通过率与投诉。"}
	value.Metrics.StatusCounts = map[string]int64{}
	var rows []struct {
		Status, ValidityStatus string
		Count                  int64
	}
	q := tx.Table("pm_report_revision").Where("tenant_id = ?", tenant)
	if err := q.Select("status, validity_status, COUNT(*) count").Group("status, validity_status").Limit(129).Scan(&rows).Error; err != nil {
		return value, err
	}
	if len(rows) > 128 {
		return value, application.ErrProjectSnapshotLimit
	}
	for _, row := range rows {
		if row.ValidityStatus == "ACTIVE" {
			value.Metrics.ActiveReports += row.Count
			value.Metrics.StatusCounts[row.Status] += row.Count
		}
		if row.ValidityStatus == "VOID" {
			value.Metrics.VoidReports += row.Count
		}
	}
	for _, status := range []string{"REVIEWED", "APPROVED", "ISSUED", "ARCHIVED"} {
		value.Metrics.ReviewedReports += value.Metrics.StatusCounts[status]
	}
	if err := reportDashboardMedian(tx, tenant, &value.Metrics); err != nil {
		return value, err
	}
	var latest any
	if err := tx.Table("pm_report_revision").Select("MAX(updated_at)").Where("tenant_id = ?", tenant).Row().Scan(&latest); err != nil {
		return value, err
	}
	updated, err := snapshotSQLTime(latest)
	if err != nil {
		return value, err
	}
	if !updated.IsZero() {
		value.SourceUpdatedAt = updated.Format(time.RFC3339Nano)
	}
	value.Available = true
	return value, nil
}

// reportDashboardMedian 只读取中间一至两个真实周期，避免全量加载版本。
// 参数：tx为快照事务，tenant为租户，metrics接收周期样本数与P50。
// 返回值：SQL或快照不一致错误；无样本时P50保持空值。
func reportDashboardMedian(tx *gorm.DB, tenant string, metrics *domain.ReportDashboardMetrics) error {
	cycles := func() *gorm.DB {
		return tx.Table("pm_report_revision").Where("tenant_id = ? AND validity_status = 'ACTIVE' AND prepared_at IS NOT NULL AND issued_at IS NOT NULL AND issued_at >= prepared_at", tenant)
	}
	if err := cycles().Count(&metrics.CycleSamples).Error; err != nil {
		return err
	}
	if metrics.CycleSamples == 0 {
		return nil
	}
	expression := "TIMESTAMPDIFF(MICROSECOND, prepared_at, issued_at) / 86400000000.0"
	if tx.Dialector.Name() == "sqlite" {
		expression = "julianday(issued_at) - julianday(prepared_at)"
	}
	limit := 1
	if metrics.CycleSamples%2 == 0 {
		limit = 2
	}
	var samples []struct{ Days float64 }
	if err := cycles().Select(expression + " days").Order("days ASC, id ASC").Offset(int((metrics.CycleSamples - 1) / 2)).Limit(limit).Scan(&samples).Error; err != nil {
		return err
	}
	if len(samples) != limit {
		return application.ErrProjectSnapshotProtocol
	}
	median := samples[0].Days
	if limit == 2 {
		median = (median + samples[1].Days) / 2
	}
	metrics.CycleP50Days = &median
	return nil
}
