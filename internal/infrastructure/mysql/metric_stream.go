package mysql

import (
	"context"
	"database/sql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/metricstream"
	"gorm.io/gorm"
	"io"
	"strings"
	"time"
)

// StreamMetricFacts 在同一RR快照中逐行输出完整报告与资源指标。
// 参数：ctx 为请求，tenant 为验签租户，output 为有界协议流。
// 返回值：源读取、输出或提交错误；未提交绝不输出end。
func (r *Repository) StreamMetricFacts(ctx context.Context, tenant string, output io.Writer) error {
	if tenant == "" || strings.TrimSpace(tenant) != tenant {
		return application.ErrValidation
	}
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	meta := domain.MetricFactsSnapshot{SchemaVersion: 1, TenantID: tenant, SourceCode: "project_management", SnapshotAt: time.Now().UTC().Format(time.RFC3339Nano), Complete: true, Rows: []domain.MetricFactRow{}, Basis: "逻辑报告仅完整新审核首轮终决；历史首次未知。实际利用率仅明确发布容量与独立确认实施时段，按北京业务月及冻结容量组织；未登记容量不推算，每资源当月未知覆盖单列。"}
	var bounds struct{ First, Last *time.Time }
	if err := tx.Model(&metricOperationRecord{}).Select("MIN(created_at) first,MAX(created_at) last").Where("tenant_id=?", tenant).Scan(&bounds).Error; err != nil {
		return err
	}
	if bounds.First != nil {
		meta.CollectionStartedAt = bounds.First.UTC().Format(time.RFC3339Nano)
	}
	if bounds.Last != nil {
		meta.SourceUpdatedAt = bounds.Last.UTC().Format(time.RFC3339Nano)
	}
	stream, err := metricstream.New(output, meta)
	if err != nil {
		return err
	}
	if err = streamReportReviews(tx, tenant, stream); err != nil {
		return err
	}
	if err = streamReportReturns(tx, tenant, stream); err != nil {
		return err
	}
	if err = streamResourceMetrics(tx, tenant, stream); err != nil {
		return err
	}
	if err = stream.Flush(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit().Error; err != nil {
		return err
	}
	return stream.End(meta.SourceUpdatedAt, meta.CollectionStartedAt)
}

// streamReportReviews 通过单次关联游标读取首版本、首轮及缺轮次真实归属。
// 参数：tx/tenant 为一致性租户快照，stream 为有界输出。
// 返回值：父项缺失、读取或输出失败返回错误，历史首次仍保持UNKNOWN。
func streamReportReviews(tx *gorm.DB, tenant string, stream *metricstream.Writer) error {
	const query = `SELECT v.subject_type,v.subject_id,v.revision,v.review_required,
 q.id round_id,q.revision round_revision,q.round_no,q.decision,q.decided_at,
 CASE WHEN q.id IS NOT NULL THEN q.organization_id ELSE p.owner_org_id END organization_id,
 CASE WHEN q.id IS NOT NULL THEN q.identity_id ELSE i.project_manager_id END identity_id,
 CASE WHEN q.id IS NOT NULL THEN q.project_id ELSE i.project_id END project_id,
 p.id parent_id
 FROM (SELECT r.*,ROW_NUMBER() OVER(PARTITION BY subject_type,subject_id ORDER BY revision,id) rn FROM pm_report_revision r WHERE tenant_id=?) v
 LEFT JOIN (SELECT r.*,ROW_NUMBER() OVER(PARTITION BY subject_type,subject_id ORDER BY revision,round_no,id) rn FROM pm_report_review_round r WHERE tenant_id=?) q ON BINARY q.subject_type=BINARY v.subject_type AND BINARY q.subject_id=BINARY v.subject_id AND q.rn=1
 LEFT JOIN pm_service_item i ON i.tenant_id=? AND BINARY i.id=BINARY v.service_item_id
 LEFT JOIN pm_project p ON BINARY p.tenant_id=BINARY i.tenant_id AND BINARY p.id=BINARY i.project_id
 WHERE v.rn=1 ORDER BY v.subject_type,v.subject_id`
	rows, err := tx.Raw(query, tenant, tenant, tenant).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v struct {
			SubjectType, SubjectID                string
			Revision                              uint64
			ReviewRequired                        bool
			RoundID                               *string
			RoundRevision, RoundNo                uint64
			Decision                              string
			DecidedAt                             *time.Time
			OrganizationID, IdentityID, ProjectID string
			ParentID                              *string
		}
		if err := tx.ScanRows(rows, &v); err != nil {
			return err
		}
		if v.RoundID == nil && v.ParentID == nil {
			return gorm.ErrRecordNotFound
		}
		row := domain.MetricFactRow{Kind: "report_review", RecordID: v.SubjectType + ":" + v.SubjectID, Status: "UNKNOWN", OrganizationID: v.OrganizationID, IdentityID: v.IdentityID, ProjectID: v.ProjectID}
		if v.ReviewRequired && v.Revision == 0 && (v.RoundID == nil || v.RoundRevision == 0 && v.RoundNo == 1) {
			row.Status = "PENDING"
			if v.RoundID != nil && v.DecidedAt != nil && (v.Decision == "PASSED" || v.Decision == "RETURNED") {
				row.Status = v.Decision
				row.Period = metricPeriod(*v.DecidedAt)
				row.FirstDecidedAt = v.DecidedAt.UTC().Format(time.RFC3339Nano)
			}
		}
		if err := stream.Add(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// streamReportReturns 通过结果游标输出所有真实退回事件，不限制历史条数。
// 参数：tx/tenant 为一致性租户快照，stream 为有界输出。
// 返回值：查询或输出失败返回错误。
func streamReportReturns(tx *gorm.DB, tenant string, stream *metricstream.Writer) error {
	rows, err := tx.Model(&metricReviewRecord{}).Select("id,subject_type,subject_id,organization_id,identity_id,project_id,decided_at,reason_code").Where("tenant_id=? AND decision='RETURNED' AND decided_at IS NOT NULL", tenant).Order("id").Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v metricReviewRecord
		if err := tx.ScanRows(rows, &v); err != nil {
			return err
		}
		row := domain.MetricFactRow{Kind: "report_return", RecordID: v.ID, SubjectID: v.SubjectType + ":" + v.SubjectID, OrganizationID: v.OrganizationID, IdentityID: v.IdentityID, ProjectID: v.ProjectID, Period: metricPeriod(*v.DecidedAt), Status: "RETURNED", ReasonCode: v.ReasonCode}
		if err := stream.Add(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// streamResourceMetrics 在数据库分组后逐行读取资源、期间及冻结组织的完整事实。
// 参数：tx/tenant 为一致性租户快照，stream 为有界输出。
// 返回值：查询、计数溢出或输出失败返回错误；未登记容量保留未知。
func streamResourceMetrics(tx *gorm.DB, tenant string, stream *metricstream.Writer) error {
	const stable = `CASE WHEN resource_type='PERSON' AND identity_id<>'' THEN identity_id ELSE resource_id END COLLATE utf8mb4_bin`
	const capabilityStable = `CASE WHEN c.resource_type='PERSON' AND c.user_id<>'' THEN c.user_id ELSE c.resource_id END COLLATE utf8mb4_bin`
	query := `SELECT resource_type,resource_key,period,organization_id,MAX(identity_id) identity_id,
 SUM(capacity) capacity_minutes,SUM(actual) actual_minutes,SUM(pending) pending_count,SUM(auto_minutes) auto_confirmed_minutes,SUM(auto_count) auto_confirmed_count,
 CASE WHEN SUM(has_capacity)=0 THEN 1 ELSE 0 END unknown_count FROM (
 SELECT resource_type COLLATE utf8mb4_bin resource_type,` + stable + ` resource_key,SUBSTRING(business_date,1,7) COLLATE utf8mb4_bin period,organization_id COLLATE utf8mb4_bin organization_id,identity_id COLLATE utf8mb4_bin identity_id,minutes capacity,0 actual,0 pending,1 has_capacity,0 auto_minutes,0 auto_count FROM pm_resource_capacity WHERE tenant_id=? AND status='PUBLISHED'
 UNION ALL SELECT resource_type COLLATE utf8mb4_bin,` + stable + `,SUBSTRING(business_date,1,7) COLLATE utf8mb4_bin,organization_id COLLATE utf8mb4_bin,identity_id COLLATE utf8mb4_bin,0,CASE WHEN status IN ('CONFIRMED','CANCEL_PENDING') THEN minutes ELSE 0 END,CASE WHEN status='PENDING' THEN 1 ELSE 0 END,0,CASE WHEN status IN ('CONFIRMED','CANCEL_PENDING') AND auto_confirmed THEN minutes ELSE 0 END,CASE WHEN status IN ('CONFIRMED','CANCEL_PENDING') AND auto_confirmed THEN 1 ELSE 0 END FROM pm_resource_usage WHERE tenant_id=? AND status IN ('CONFIRMED','CANCEL_PENDING','PENDING')
 UNION ALL SELECT c.resource_type COLLATE utf8mb4_bin,` + capabilityStable + `,?,'',c.user_id COLLATE utf8mb4_bin,0,0,0,0,0,0 FROM pm_capability c WHERE c.tenant_id=? AND c.resource_type IN ('PERSON','EQUIPMENT') AND NOT EXISTS(SELECT 1 FROM pm_resource_capacity x WHERE BINARY x.tenant_id=BINARY c.tenant_id AND x.status='PUBLISHED' AND BINARY x.resource_type=BINARY c.resource_type AND SUBSTRING(x.business_date,1,7)=? AND CASE WHEN x.resource_type='PERSON' AND x.identity_id<>'' THEN x.identity_id ELSE x.resource_id END COLLATE utf8mb4_bin=` + capabilityStable + `)
 ) facts GROUP BY resource_type,resource_key,period,organization_id ORDER BY resource_type,resource_key,period,organization_id`
	current := metricPeriod(time.Now().UTC())
	rows, err := tx.Raw(query, tenant, tenant, current, tenant, current).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v struct {
			ResourceType, ResourceKey, Period, OrganizationID, IdentityID string
			CapacityMinutes, ActualMinutes, PendingCount, UnknownCount    int64
			AutoConfirmedMinutes, AutoConfirmedCount                      int64
		}
		if err := tx.ScanRows(rows, &v); err != nil {
			return err
		}
		key := metricResourceGroupKey(v.ResourceType, v.ResourceKey, v.IdentityID, v.Period, v.OrganizationID)
		row := metricResourceRow(key, v.ResourceType, v.IdentityID, v.Period, v.OrganizationID)
		row.CapacityMinutes, row.ActualMinutes, row.PendingCount, row.UnknownCount = v.CapacityMinutes, v.ActualMinutes, v.PendingCount, v.UnknownCount
		row.AutoConfirmedMinutes, row.AutoConfirmedCount = v.AutoConfirmedMinutes, v.AutoConfirmedCount
		if err := stream.Add(row); err != nil {
			return err
		}
	}
	return rows.Err()
}
