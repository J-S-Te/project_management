package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ApplyExpenseSpend 在单个事务中消费完整快照并冻结回执，冲突另行隔离留证。
// 参数：ctx 控制事务，s 为经过可信机器边界检查的完整快照。
// 返回值：只有持久化提交后返回回执；冲突或数据库错误不返回成功。
func (r *Repository) ApplyExpenseSpend(ctx context.Context, s domain.SpendSnapshot) (domain.SpendReceipt, error) {
	if err := domain.ValidateSpendSnapshot(s); err != nil {
		return domain.SpendReceipt{}, err
	}
	body, _ := json.Marshal(s)
	sum := sha256.Sum256(body)
	eventHash := hex.EncodeToString(sum[:])
	var receipt domain.SpendReceipt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		receipt, err = consumeExpenseSpend(tx, s, eventHash, string(body))
		return err
	})
	if errors.Is(err, domain.ErrSpendProtocol) {
		conflict := expenseConflictRow{ID: ulid.Make().String(), TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, ClaimID: s.ClaimID, EventID: s.EventID, EventHash: eventHash, CreatedAt: time.Now().UTC()}
		if saveErr := r.db.WithContext(ctx).Table("pm_expense_conflict").Create(&conflict).Error; saveErr != nil {
			return domain.SpendReceipt{}, saveErr
		}
	}
	return receipt, err
}

// expenseClaimQuery 限定台账操作的完整租户、环境和单据边界。
// 参数：tx 为事务，table 为固定内部表名，s 为来源快照。
// 返回值：已绑定三项来源隔离条件的查询。
func expenseClaimQuery(tx *gorm.DB, table string, s domain.SpendSnapshot) *gorm.DB {
	return tx.Table(table).Where("tenant_id=? AND source_environment=? AND claim_id=?", s.TenantID, s.SourceEnvironment, s.ClaimID)
}

// consumeExpenseSpend 锁定单据游标并检查事件、版本和完整历史后应用贡献。
// 参数：tx 为当前事务，s 为完整快照，eventHash 为冻结载荷摘要，payload 为原始类型载荷。
// 返回值：待提交回执；任何冲突返回协议错误并回滚整个事务。
func consumeExpenseSpend(tx *gorm.DB, s domain.SpendSnapshot, eventHash, payload string) (domain.SpendReceipt, error) {
	cursor := expenseCursorRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, ClaimID: s.ClaimID}
	if err := tx.Table("pm_expense_cursor").Clauses(clause.OnConflict{DoNothing: true}).Create(&cursor).Error; err != nil {
		return domain.SpendReceipt{}, err
	}
	if err := expenseClaimQuery(tx, "pm_expense_cursor", s).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&cursor).Error; err != nil {
		return domain.SpendReceipt{}, err
	}
	var inbox expenseInboxRow
	err := tx.Table("pm_expense_inbox").Where("tenant_id=? AND source_environment=? AND event_id=?", s.TenantID, s.SourceEnvironment, s.EventID).Take(&inbox).Error
	if err == nil {
		var frozen domain.SpendReceipt
		if inbox.EventHash != eventHash {
			return frozen, domain.ErrSpendProtocol
		}
		err = json.Unmarshal([]byte(inbox.Receipt), &frozen)
		return frozen, err
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.SpendReceipt{}, err
	}
	var version expenseSnapshotRow
	err = expenseClaimQuery(tx, "pm_expense_snapshot", s).Where("projection_revision=?", s.ProjectionRevision).Take(&version).Error
	if err == nil && version.SnapshotHash != s.SnapshotHash {
		return domain.SpendReceipt{}, domain.ErrSpendProtocol
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.SpendReceipt{}, err
	}
	if err := verifyExpenseHistory(tx, s, cursor.ProjectionRevision); err != nil {
		return domain.SpendReceipt{}, err
	}
	if version.ProjectionRevision == 0 {
		version = expenseSnapshotRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, ClaimID: s.ClaimID, ProjectionRevision: s.ProjectionRevision, SnapshotHash: s.SnapshotHash, Payload: payload}
		if err := tx.Table("pm_expense_snapshot").Create(&version).Error; err != nil {
			return domain.SpendReceipt{}, err
		}
	}
	receipt := domain.SpendReceipt{Status: "SUPERSEDED", ReceivedRevision: s.ProjectionRevision, AppliedRevision: cursor.ProjectionRevision, SnapshotHash: s.SnapshotHash}
	if s.ProjectionRevision > cursor.ProjectionRevision {
		if err := replaceExpenseProjection(tx, s); err != nil {
			return receipt, err
		}
		if err := expenseClaimQuery(tx, "pm_expense_cursor", s).Updates(map[string]any{"projection_revision": s.ProjectionRevision, "snapshot_hash": s.SnapshotHash, "updated_at": time.Now().UTC()}).Error; err != nil {
			return receipt, err
		}
		receipt.Status = "APPLIED"
		receipt.AppliedRevision = s.ProjectionRevision
	} else if s.ProjectionRevision == cursor.ProjectionRevision {
		receipt.Status = "DUPLICATE"
	}
	frozen, _ := json.Marshal(receipt)
	inbox = expenseInboxRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, EventID: s.EventID, ClaimID: s.ClaimID, EventHash: eventHash, Receipt: string(frozen), ReceivedAt: time.Now().UTC()}
	return receipt, tx.Table("pm_expense_inbox").Create(&inbox).Error
}

// verifyExpenseHistory 校验不可变效果及新旧完整快照历史集合关系并保存新效果。
// 参数：tx 为当前事务，s 为待消费快照，current 为当前最高版本。
// 返回值：缺失历史、跨单复用、效果篡改或数据库失败时返回错误。
func verifyExpenseHistory(tx *gorm.DB, s domain.SpendSnapshot, current uint64) error {
	var known []expenseEffectRow
	if err := expenseClaimQuery(tx, "pm_expense_effect", s).Find(&known).Error; err != nil {
		return err
	}
	incoming := map[string]domain.SpendEffect{}
	for _, e := range s.Effects {
		incoming[e.EffectID] = e
	}
	if s.ProjectionRevision >= current {
		for _, e := range known {
			if _, ok := incoming[e.EffectID]; !ok {
				return domain.ErrSpendProtocol
			}
		}
	}
	for _, e := range s.Effects {
		var existing expenseEffectRow
		err := tx.Table("pm_expense_effect").Where("tenant_id=? AND source_environment=? AND (effect_id=? OR (payment_record_id=? AND kind=?))", s.TenantID, s.SourceEnvironment, e.EffectID, e.PaymentRecordID, e.Kind).Take(&existing).Error
		if err == nil {
			if existing.EffectID != e.EffectID || existing.ClaimID != s.ClaimID || existing.EffectHash != domain.SpendEffectHash(e) {
				return domain.ErrSpendProtocol
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if s.ProjectionRevision < current {
			return domain.ErrSpendProtocol
		}
		if err := persistExpenseEffect(tx, s, e); err != nil {
			return err
		}
	}
	return nil
}

// persistExpenseEffect 保存不可变效果和每行原始分摊并验证历史项目归属。
// 参数：tx 为当前事务，s 为来源边界，effect 为尚不存在的效果。
// 返回值：归属不符或持久化失败返回错误，不因项目终止拒绝历史付款事实。
func persistExpenseEffect(tx *gorm.DB, s domain.SpendSnapshot, effect domain.SpendEffect) error {
	payload, _ := json.Marshal(effect)
	row := expenseEffectRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, EffectID: effect.EffectID, ClaimID: s.ClaimID, PaymentRecordID: effect.PaymentRecordID, Kind: effect.Kind, EffectHash: domain.SpendEffectHash(effect), Payload: string(payload)}
	if err := tx.Table("pm_expense_effect").Create(&row).Error; err != nil {
		return err
	}
	for _, a := range effect.Allocations {
		var count int64
		if err := tx.Model(&projectRecord{}).Where("tenant_id=? AND id=?", s.TenantID, a.ProjectID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return domain.ErrSpendProtocol
		}
		item := ""
		if a.ServiceItemID != nil {
			item = *a.ServiceItemID
			// 历史事实接受被替换后归档的原服务项，仍必须属于同一项目与租户。
			if err := tx.Unscoped().Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=? AND id=?", s.TenantID, a.ProjectID, item).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return domain.ErrSpendProtocol
			}
		}
		amount, _ := strconv.ParseInt(a.AmountMinor, 10, 64)
		allocation := expenseAllocationRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, EffectID: effect.EffectID, ProjectID: a.ProjectID, ServiceItemID: item, FeeType: a.FeeType, AmountMinor: amount}
		if err := tx.Table("pm_expense_allocation").Create(&allocation).Error; err != nil {
			return err
		}
	}
	return nil
}

// replaceExpenseProjection 从已验证完整快照替换整单贡献而不逐条累加。
// 参数：tx 为持有单据游标锁的事务，s 为权威完整快照。
// 返回值：删除或新建任一贡献失败时返回错误供整个事务回滚。
func replaceExpenseProjection(tx *gorm.DB, s domain.SpendSnapshot) error {
	if err := expenseClaimQuery(tx, "pm_expense_projection", s).Delete(&expenseProjectionRow{}).Error; err != nil {
		return err
	}
	for _, c := range s.Contributions {
		item := ""
		if c.ServiceItemID != nil {
			item = *c.ServiceItemID
		}
		amount, _ := strconv.ParseInt(c.PaidNetMinor, 10, 64)
		row := expenseProjectionRow{TenantID: s.TenantID, SourceEnvironment: s.SourceEnvironment, ClaimID: s.ClaimID, ProjectID: c.ProjectID, ServiceItemID: item, FeeType: c.FeeType, PaidNetMinor: amount}
		if err := tx.Table("pm_expense_projection").Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
