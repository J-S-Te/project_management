package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
)

// ReadMetricFacts 在同一只读RR事务中生成有界完整的指标事实。
// 参数：ctx 为取消上下文，tenant 为签名机器身份绑定租户。
// 返回值：事务提交后的完整快照；超限、查询或提交失败时不发布可用结果。
func (r *Repository) ReadMetricFacts(ctx context.Context, tenant string) (domain.MetricFactsSnapshot, error) {
	empty := domain.MetricFactsSnapshot{}
	if tenant == "" || strings.TrimSpace(tenant) != tenant {
		return empty, application.ErrValidation
	}
	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if tx.Error != nil {
		return empty, tx.Error
	}
	defer tx.Rollback()
	out := domain.MetricFactsSnapshot{SchemaVersion: 1, TenantID: tenant, SourceCode: "project_management", SnapshotAt: time.Now().UTC().Format(time.RFC3339Nano), Complete: true, Rows: []domain.MetricFactRow{}, Basis: "逻辑报告仅完整新审核首轮终决；历史首次未知。实际利用率仅明确发布容量与独立确认实施时段，按北京业务月及冻结容量组织；未登记容量不推算，每资源当月未知覆盖单列。"}
	if err := metricReportRows(tx, tenant, &out); err != nil {
		return empty, err
	}
	if err := metricResourceRows(tx, tenant, &out); err != nil {
		return empty, err
	}
	var bounds struct{ First, Last *time.Time }
	if err := tx.Model(&metricOperationRecord{}).Select("MIN(created_at) first,MAX(created_at) last").Where("tenant_id=?", tenant).Scan(&bounds).Error; err != nil {
		return empty, err
	}
	if bounds.First != nil {
		out.CollectionStartedAt = bounds.First.UTC().Format(time.RFC3339Nano)
	}
	if bounds.Last != nil {
		out.SourceUpdatedAt = bounds.Last.UTC().Format(time.RFC3339Nano)
	}
	if len(out.Rows) > 10000 {
		return empty, application.ErrProjectSnapshotLimit
	}
	body, err := json.Marshal(out)
	if err != nil {
		return empty, err
	}
	if len(body) > (8<<20)-64 {
		return empty, application.ErrProjectSnapshotLimit
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if err = tx.Commit().Error; err != nil {
		return empty, err
	}
	return out, nil
}

// metricPeriod 计算UTC事实时间对应的北京业务月份。
// 参数：at 为真实事实时间。
// 返回值：北京时间YYYY-MM。
func metricPeriod(at time.Time) string {
	return at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01")
}

// metricReportRows 逐逻辑报告保留首次未知、待审和可证明首轮终决。
// 参数：tx 为只读事务，tenant 为租户，out 接收全部报告和真实退回事件。
// 返回值：来源超过有界完整读取上限或数据库失败时返回错误。
func metricReportRows(tx *gorm.DB, tenant string, out *domain.MetricFactsSnapshot) error {
	var revisions []reportRevisionRecord
	if err := tx.Where("tenant_id=?", tenant).Order("subject_type,subject_id,revision,id").Limit(10001).Find(&revisions).Error; err != nil {
		return err
	}
	var rounds []metricReviewRecord
	if err := tx.Where("tenant_id=?", tenant).Order("revision,round_no,id").Limit(10001).Find(&rounds).Error; err != nil {
		return err
	}
	if len(revisions) > 10000 || len(rounds) > 10000 {
		return application.ErrProjectSnapshotLimit
	}
	first := map[string]metricReviewRecord{}
	for _, round := range rounds {
		subject := round.SubjectType + ":" + round.SubjectID
		if _, ok := first[subject]; !ok {
			first[subject] = round
		}
		if round.Decision == "RETURNED" && round.DecidedAt != nil {
			out.Rows = append(out.Rows, domain.MetricFactRow{Kind: "report_return", RecordID: round.ID, SubjectID: subject, OrganizationID: round.OrganizationID, IdentityID: round.IdentityID, ProjectID: round.ProjectID, Period: metricPeriod(*round.DecidedAt), Status: "RETURNED", ReasonCode: round.ReasonCode})
		}
	}
	seen := map[string]bool{}
	for _, rev := range revisions {
		key := rev.SubjectType + ":" + rev.SubjectID
		if seen[key] {
			continue
		}
		seen[key] = true
		row := domain.MetricFactRow{Kind: "report_review", RecordID: key, Status: "UNKNOWN"}
		round, ok := first[key]
		if ok {
			row.OrganizationID = round.OrganizationID
			row.IdentityID = round.IdentityID
			row.ProjectID = round.ProjectID
		}
		if !ok {
			var item serviceItemRecord
			if err := tx.Unscoped().Where("tenant_id=? AND id=?", tenant, rev.ServiceItemID).Take(&item).Error; err != nil {
				return err
			}
			var project projectRecord
			if err := tx.Where("tenant_id=? AND id=?", tenant, item.ProjectID).Take(&project).Error; err != nil {
				return err
			}
			row.OrganizationID = project.OwnerOrgID
			row.IdentityID = item.ProjectManagerID
			row.ProjectID = item.ProjectID
		}
		if rev.ReviewRequired && rev.Revision == 0 && (!ok || round.Revision == 0 && round.RoundNo == 1) {
			row.Status = "PENDING"
			if ok && round.DecidedAt != nil && (round.Decision == "PASSED" || round.Decision == "RETURNED") {
				row.Status = round.Decision
				row.Period = metricPeriod(*round.DecidedAt)
				row.FirstDecidedAt = round.DecidedAt.UTC().Format(time.RFC3339Nano)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return nil
}

// metricResourceRows 按资源、北京月及冻结容量组织汇总真实时长与覆盖。
// 参数：tx 为同一快照事务，tenant 为租户，out 接收人员和设备行。
// 返回值：来源读取超限或失败时不输出不完整指标。
func metricResourceRows(tx *gorm.DB, tenant string, out *domain.MetricFactsSnapshot) error {
	var capacities []metricCapacityRecord
	var usages []metricUsageRecord
	var resources []capabilityRecord
	if err := tx.Where("tenant_id=? AND status='PUBLISHED'", tenant).Limit(10001).Find(&capacities).Error; err != nil {
		return err
	}
	if err := tx.Where("tenant_id=?", tenant).Limit(10001).Find(&usages).Error; err != nil {
		return err
	}
	if err := tx.Where("tenant_id=?", tenant).Limit(10001).Find(&resources).Error; err != nil {
		return err
	}
	if len(capacities) > 10000 || len(usages) > 10000 || len(resources) > 10000 {
		return application.ErrProjectSnapshotLimit
	}
	groups := map[string]*domain.MetricFactRow{}
	known := map[string]bool{}
	for _, capacity := range capacities {
		period := capacity.BusinessDate[:7]
		key := metricResourceGroupKey(capacity.ResourceType, capacity.ResourceID, capacity.IdentityID, period, capacity.OrganizationID)
		row := groups[key]
		if row == nil {
			row = metricResourceRow(key, capacity.ResourceType, capacity.IdentityID, period, capacity.OrganizationID)
			groups[key] = row
		}
		row.CapacityMinutes += capacity.Minutes
		known[metricResourceGroupKey(capacity.ResourceType, capacity.ResourceID, capacity.IdentityID, period, "")] = true
	}
	for _, usage := range usages {
		if usage.Status != "CONFIRMED" && usage.Status != "CANCEL_PENDING" && usage.Status != "PENDING" {
			continue
		}
		period := usage.BusinessDate[:7]
		key := metricResourceGroupKey(usage.ResourceType, usage.ResourceID, usage.IdentityID, period, usage.OrganizationID)
		row := groups[key]
		if row == nil {
			row = metricResourceRow(key, usage.ResourceType, usage.IdentityID, period, usage.OrganizationID)
			groups[key] = row
			row.UnknownCount = 1
		}
		switch usage.Status {
		case "CONFIRMED", "CANCEL_PENDING":
			row.ActualMinutes += usage.Minutes
		case "PENDING":
			row.PendingCount++
		}
	}
	current := metricPeriod(time.Now().UTC())
	for _, resource := range resources {
		prefix := metricResourceGroupKey(resource.ResourceType, resource.ResourceID, resource.UserID, current, "")
		if known[prefix] || resource.ResourceType != "PERSON" && resource.ResourceType != "EQUIPMENT" {
			continue
		}
		key := prefix
		if groups[key] == nil {
			row := metricResourceRow(key, resource.ResourceType, resource.UserID, current, "")
			row.UnknownCount = 1
			groups[key] = row
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.Rows = append(out.Rows, *groups[key])
	}
	return nil
}

// metricResourceRow 生成稳定资源指标行，未知容量保持零分母与明确未知计数。
// 参数：key/kind/identity/period/org 为真实资源身份、业务月与冻结归属。
// 返回值：不推算容量和实际时长的初始行。
func metricResourceRow(key, kind, identity, period, org string) *domain.MetricFactRow {
	metricKind := "person_utilization"
	if kind == "EQUIPMENT" {
		metricKind = "equipment_utilization"
		identity = ""
	}
	return &domain.MetricFactRow{Kind: metricKind, RecordID: key, IdentityID: identity, Period: period, OrganizationID: org, Status: "CONFIRMED"}
}

// metricResourceGroupKey 按人员稳定身份合并多档案容量与实际时长，设备保留原资源键。
// 参数：kind/id/identity 为档案证据及稳定人员身份，period/org 为业务月和冻结组织。
// 返回值：跨档案稳定且保留组织维度的统计行身份。
func metricResourceGroupKey(kind, id, identity, period, org string) string {
	key := id
	if kind == "PERSON" && identity != "" {
		key = identity
	}
	return kind + ":" + key + ":" + period + ":" + org
}
