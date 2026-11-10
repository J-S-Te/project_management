package mysql

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/j-s-te/project-management/internal/application"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReconcileExpenseSpend 分页重算实际投影和效果分摊以发现只读游标无法暴露的损坏。
// 参数：ctx 控制读取，tenant/env 为可信来源，after 为更新时间及单据标识组成的不透明游标，limit 为已限制的批量大小。
// 返回值：每单当前版本、净额、摘要与实际一致性；数据库失败返回错误。
func (r *Repository) ReconcileExpenseSpend(ctx context.Context, tenant, env, after string, limit int) ([]domain.SpendReconciliation, error) {
	result := []domain.SpendReconciliation{}
	var afterKey struct {
		UpdatedAt time.Time `json:"updated_at"`
		ClaimID   string    `json:"claim_id"`
	}
	if after != "" {
		raw, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || json.Unmarshal(raw, &afterKey) != nil || afterKey.UpdatedAt.IsZero() || afterKey.ClaimID == "" {
			return result, application.ErrValidation
		}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cursors []expenseCursorRow
		if err := tx.Table("pm_expense_cursor").Where("tenant_id=? AND source_environment=? AND (updated_at>? OR (updated_at=? AND claim_id>?))", tenant, env, afterKey.UpdatedAt, afterKey.UpdatedAt, afterKey.ClaimID).Order("updated_at,claim_id").Limit(limit).Find(&cursors).Error; err != nil {
			return err
		}
		for _, cursor := range cursors {
			snapshot, err := readExpenseSnapshot(tx, cursor)
			if err != nil {
				return err
			}
			consistent, err := checkExpenseMaterialization(tx, snapshot)
			if err != nil {
				return err
			}
			sum := new(big.Int)
			for _, c := range snapshot.Contributions {
				v, _ := new(big.Int).SetString(c.PaidNetMinor, 10)
				if v != nil {
					sum.Add(sum, v)
				}
			}
			hash, _ := domain.SpendSnapshotHash(snapshot)
			result = append(result, domain.SpendReconciliation{ClaimID: cursor.ClaimID, ProjectionRevision: cursor.ProjectionRevision, SnapshotHash: cursor.SnapshotHash, Currency: snapshot.Currency, PaidNetMinor: sum.String(), UpdatedAt: cursor.UpdatedAt, Consistent: consistent && hash == cursor.SnapshotHash && domain.ValidateSpendSnapshot(snapshot) == nil})
		}
		return nil
	})
	return result, err
}

// readExpenseSnapshot 读取与游标精确匹配的不可变版本并复核内容摘要。
// 参数：tx 为事务，cursor 为当前锁定或一致性读取的游标。
// 返回值：完整历史快照；缺失、篡改或解析失败返回错误。
func readExpenseSnapshot(tx *gorm.DB, cursor expenseCursorRow) (domain.SpendSnapshot, error) {
	var row expenseSnapshotRow
	s := domain.SpendSnapshot{TenantID: cursor.TenantID, SourceEnvironment: cursor.SourceEnvironment, ClaimID: cursor.ClaimID}
	if err := expenseClaimQuery(tx, "pm_expense_snapshot", s).Where("projection_revision=?", cursor.ProjectionRevision).Take(&row).Error; err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(row.Payload), &s); err != nil {
		return s, err
	}
	if s.TenantID != cursor.TenantID || s.SourceEnvironment != cursor.SourceEnvironment || s.ClaimID != cursor.ClaimID || s.ProjectionRevision != cursor.ProjectionRevision || s.SnapshotHash != row.SnapshotHash {
		return s, domain.ErrSpendProtocol
	}
	return s, nil
}

// checkExpenseMaterialization 将实际贡献、效果和分摊逐项与完整快照比较。
// 参数：tx 为读取事务，s 为不可变快照。
// 返回值：三类实际记录均一致返回 true；数据库失败返回错误。
func checkExpenseMaterialization(tx *gorm.DB, s domain.SpendSnapshot) (bool, error) {
	var projections []expenseProjectionRow
	if err := expenseClaimQuery(tx, "pm_expense_projection", s).Find(&projections).Error; err != nil {
		return false, err
	}
	if len(projections) != len(s.Contributions) {
		return false, nil
	}
	expected := map[string]string{}
	for _, c := range s.Contributions {
		expected[domain.SpendKey(c.ProjectID, c.ServiceItemID, c.FeeType)] = c.PaidNetMinor
	}
	for _, p := range projections {
		var item *string
		if p.ServiceItemID != "" {
			item = &p.ServiceItemID
		}
		if expected[domain.SpendKey(p.ProjectID, item, p.FeeType)] != strconv.FormatInt(p.PaidNetMinor, 10) {
			return false, nil
		}
	}
	var effects []expenseEffectRow
	if err := expenseClaimQuery(tx, "pm_expense_effect", s).Find(&effects).Error; err != nil {
		return false, err
	}
	if len(effects) != len(s.Effects) {
		return false, nil
	}
	known := map[string]expenseEffectRow{}
	for _, e := range effects {
		known[e.EffectID] = e
	}
	for _, e := range s.Effects {
		row := known[e.EffectID]
		var decoded domain.SpendEffect
		if row.EffectHash != domain.SpendEffectHash(e) || json.Unmarshal([]byte(row.Payload), &decoded) != nil || domain.SpendEffectHash(decoded) != row.EffectHash {
			return false, nil
		}
		var allocations []expenseAllocationRow
		if err := tx.Table("pm_expense_allocation").Where("tenant_id=? AND source_environment=? AND effect_id=?", s.TenantID, s.SourceEnvironment, e.EffectID).Find(&allocations).Error; err != nil {
			return false, err
		}
		if len(allocations) != len(e.Allocations) {
			return false, nil
		}
		amounts := map[string]string{}
		for _, a := range e.Allocations {
			amounts[domain.SpendKey(a.ProjectID, a.ServiceItemID, a.FeeType)] = a.AmountMinor
		}
		for _, a := range allocations {
			var item *string
			if a.ServiceItemID != "" {
				item = &a.ServiceItemID
			}
			if amounts[domain.SpendKey(a.ProjectID, item, a.FeeType)] != strconv.FormatInt(a.AmountMinor, 10) {
				return false, nil
			}
		}
	}
	return true, nil
}

// RebuildExpenseSpend 在受控命令下从已验摘要的最新完整快照重建贡献并保留修复审计。
// 参数：ctx 控制事务，tenant/env/claim 限定来源，actor/reason 为可信操作者与修复理由。
// 返回值：重建和审计同事务提交，任何验证或持久化失败返回错误。
func (r *Repository) RebuildExpenseSpend(ctx context.Context, tenant, env, claim, actor, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s := domain.SpendSnapshot{TenantID: tenant, SourceEnvironment: env, ClaimID: claim}
		var cursor expenseCursorRow
		if err := expenseClaimQuery(tx, "pm_expense_cursor", s).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&cursor).Error; err != nil {
			return err
		}
		snapshot, err := readExpenseSnapshot(tx, cursor)
		if err != nil {
			return err
		}
		if err := domain.ValidateSpendSnapshot(snapshot); err != nil {
			return err
		}
		if snapshot.SnapshotHash != cursor.SnapshotHash {
			return domain.ErrSpendProtocol
		}
		if err := replaceExpenseProjection(tx, snapshot); err != nil {
			return err
		}
		row := expenseRepairRow{ID: ulid.Make().String(), TenantID: tenant, SourceEnvironment: env, ClaimID: claim, Actor: actor, Reason: reason, CreatedAt: time.Now().UTC()}
		return tx.Table("pm_expense_repair").Create(&row).Error
	})
}

// ReadExpenseSpendSummary 返回一个项目按来源单据分组的最小只读现金支出摘要。
// 参数：ctx 控制读取，tenant/project 为已通过项目范围校验的目标，after 为更新时间及单据标识组成的不透明游标，limit 为分页大小。
// 返回值：不含银行或发票内容的净额摘要；数据库失败返回错误。
func (r *Repository) ReadExpenseSpendSummary(ctx context.Context, tenant, project, after string, limit int) ([]domain.ExpenseSpendSummary, error) {
	type summaryRow struct {
		ClaimID            string
		PaidNetMinor       string
		ProjectionRevision uint64
		UpdatedAt          time.Time
		Payload            string
	}
	var rows []summaryRow
	err := r.db.WithContext(ctx).Table("pm_expense_projection AS p").Select("p.claim_id, CAST(SUM(p.paid_net_minor) AS CHAR) AS paid_net_minor, c.projection_revision, c.updated_at, s.payload").Joins("JOIN pm_expense_cursor c ON c.tenant_id=p.tenant_id AND c.source_environment=p.source_environment AND c.claim_id=p.claim_id").Joins("JOIN pm_expense_snapshot s ON s.tenant_id=c.tenant_id AND s.source_environment=c.source_environment AND s.claim_id=c.claim_id AND s.projection_revision=c.projection_revision").Where("p.tenant_id=? AND p.project_id=? AND p.claim_id>?", tenant, project, after).Group("p.tenant_id,p.source_environment,p.claim_id,c.projection_revision,c.updated_at,s.payload").Order("p.claim_id").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := []domain.ExpenseSpendSummary{}
	for _, row := range rows {
		var s domain.SpendSnapshot
		if err := json.Unmarshal([]byte(row.Payload), &s); err != nil {
			return nil, err
		}
		summary := domain.ExpenseSpendSummary{ClaimID: row.ClaimID, ClaimNo: s.ClaimNo, Currency: s.Currency, PaidNetMinor: row.PaidNetMinor, ProjectionRevision: row.ProjectionRevision, UpdatedAt: row.UpdatedAt}
		summary.FeeCategories, summary.LastPaymentOccurredAt = expenseProjectSummary(s, project)
		out = append(out, summary)
	}
	return out, nil
}

// expenseProjectSummary 从完整历史中仅提取本项目类别净额及真实正向付款日。
// 参数：snapshot 为权威完整快照，project 为已授权当前项目。
// 返回值：有序类别净额、最近付款发生时间；退款日期不冒充付款日期。
func expenseProjectSummary(snapshot domain.SpendSnapshot, project string) ([]domain.ExpenseSpendCategory, string) {
	totals := map[string]*big.Int{}
	for _, contribution := range snapshot.Contributions {
		if contribution.ProjectID != project {
			continue
		}
		value, ok := new(big.Int).SetString(contribution.PaidNetMinor, 10)
		if !ok {
			continue
		}
		if totals[contribution.FeeType] == nil {
			totals[contribution.FeeType] = new(big.Int)
		}
		totals[contribution.FeeType].Add(totals[contribution.FeeType], value)
	}
	categories := []domain.ExpenseSpendCategory{}
	for fee, amount := range totals {
		categories = append(categories, domain.ExpenseSpendCategory{FeeType: fee, PaidNetMinor: amount.String()})
	}
	sort.Slice(categories, func(i, j int) bool { return categories[i].FeeType < categories[j].FeeType })
	var last time.Time
	for _, effect := range snapshot.Effects {
		if effect.Kind != "PAYMENT_RECORDED" {
			continue
		}
		for _, allocation := range effect.Allocations {
			if allocation.ProjectID != project {
				continue
			}
			at, e := time.Parse(time.RFC3339, effect.OccurredAt)
			if e == nil && at.After(last) {
				last = at
			}
			break
		}
	}
	if last.IsZero() {
		return categories, ""
	}
	return categories, last.UTC().Format(time.RFC3339)
}
