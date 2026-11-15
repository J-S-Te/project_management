package mysql

import (
	"context"
	"sort"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BatchMutateMetricUsages 在一个事务内完成整批用量提交、确认或取消。
// 参数：ctx/filter 为授权范围，actor 为命令人，in 为目标版本、动作与说明。
// 返回值：全部生效后的真实用量；任一版本、范围、状态或重叠冲突均整批回滚。
func (r *Repository) BatchMutateMetricUsages(ctx context.Context, filter platform.ScopeFilter, actor string, in domain.MetricUsageBatchInput) ([]domain.ResourceUsage, error) {
	out := make([]domain.ResourceUsage, 0, len(in.Items))
	ids := make([]string, 0, len(in.Items))
	expected := make(map[string]uint64, len(in.Items))
	for _, item := range in.Items {
		ids = append(ids, item.ID)
		expected[item.ID] = item.ExpectedVersion
	}
	var rows []metricUsageRecord
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND id IN ?", filter.TenantID, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, application.ErrNotFound
	}
	seedByID := make(map[string]capabilityRecord, len(rows))
	seedKeys := make([]string, 0, len(rows))
	seeds := make(map[string]capabilityRecord, len(rows))
	for _, row := range rows {
		seed, err := r.metricResourceSeed(ctx, filter.TenantID, row.ResourceType, row.ResourceID)
		if err != nil {
			return nil, err
		}
		seedByID[row.ID] = seed
		key := seed.ResourceType + "/" + seed.ResourceID
		if _, ok := seeds[key]; !ok {
			seeds[key] = seed
			seedKeys = append(seedKeys, key)
		}
	}
	sort.Strings(seedKeys)
	sort.Strings(ids)
	rowByID := make(map[string]*metricUsageRecord, len(rows))
	for i := range rows {
		rowByID[rows[i].ID] = &rows[i]
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 统一按资源身份排序加锁，避免两个批量请求交叉锁资源形成死锁。
		for _, key := range seedKeys {
			seed := seeds[key]
			if _, err := lockMetricResourceIdentity(tx, seed, ""); err != nil {
				return err
			}
		}
		for _, id := range ids {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", filter.TenantID, id).Take(rowByID[id]).Error; err != nil {
				return mapNotFound(err)
			}
		}
		replay, hash, err := metricOperation(tx, filter.TenantID, in.IdempotencyKey, []any{"BATCH_" + in.Action, actor, in}, &out)
		if err != nil || replay {
			return err
		}
		now := time.Now().UTC()
		scoped := &Repository{db: tx}
		for _, item := range in.Items {
			row := rowByID[item.ID]
			if row == nil || row.Version != expected[item.ID] {
				return application.ErrConflict
			}
			if _, err := scoped.GetServiceItem(ctx, filter, row.ServiceItemID); err != nil {
				return err
			}
			if in.Action == "DECISION" && !metricOrgAllowed(filter, row.OrganizationID) {
				return application.ErrForbidden
			}
			decision := domain.ResourceUsageDecision{ExpectedVersion: row.Version, Decision: in.Decision, Note: in.Note}
			if err := metricUsageTransition(tx, row, actor, in.Action, decision); err != nil {
				return err
			}
			row.Version++
			row.UpdatedAt = now
			if err := tx.Save(row).Error; err != nil {
				return err
			}
			out = append(out, row.ResourceUsage)
		}
		auditID := in.IdempotencyKey
		if len(auditID) > 64 {
			auditID = auditID[:64]
		}
		return saveMetricOperation(tx, filter.TenantID, in.IdempotencyKey, hash, actor, "USAGE_BATCH_"+in.Action, auditID, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
