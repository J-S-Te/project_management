package handover

import (
	"time"

	"gorm.io/gorm"
)

// transferServiceItemOwnership 在同一事务内把服务项上的实际实施责任从原人员转给接任者：
// team_lead_id / project_manager_id（标量）与 engineer_ids（JSON 数组）。
//
// 以 updated_at 作为并发标记（服务项没有版本列）；任何一处未命中都按版本冲突拒绝并写回执，
// 避免"交接成功了但服务项责任还在原人员名下"。
func transferServiceItemOwnership(tx *gorm.DB, in TransferInput, now, expected time.Time, outcome *Receipt) error {
	scalar := tx.Table("pm_service_item").
		Where("tenant_id = ? AND id = ? AND updated_at = ? AND archived_at IS NULL AND (team_lead_id = ? OR project_manager_id = ?)",
			in.TenantID, in.ResourceID, expected, in.FromUserID, in.FromUserID).
		Updates(map[string]any{
			"team_lead_id":       gorm.Expr("CASE WHEN team_lead_id = ? THEN ? ELSE team_lead_id END", in.FromUserID, in.ToUserID),
			"project_manager_id": gorm.Expr("CASE WHEN project_manager_id = ? THEN ? ELSE project_manager_id END", in.FromUserID, in.ToUserID),
			"updated_at":         now.UTC(),
		})
	if scalar.Error != nil {
		return scalar.Error
	}
	if scalar.RowsAffected == 1 {
		return writeReceipt(tx, in, now, ReceiptApplied, uint64(now.UTC().UnixMilli()), "", "pm:service_item:"+in.ResourceID, outcome)
	}
	engineer := tx.Exec("UPDATE pm_service_item SET "+
		"engineer_ids = JSON_REPLACE(engineer_ids, JSON_UNQUOTE(JSON_SEARCH(engineer_ids, 'one', ?)), ?), "+
		"updated_at = ? "+
		"WHERE tenant_id = ? AND id = ? AND updated_at = ? AND archived_at IS NULL "+
		"AND JSON_CONTAINS(COALESCE(engineer_ids, JSON_ARRAY()), JSON_QUOTE(?))",
		in.FromUserID, in.ToUserID, now.UTC(), in.TenantID, in.ResourceID, expected, in.FromUserID)
	if engineer.Error != nil {
		return engineer.Error
	}
	if engineer.RowsAffected == 1 {
		return writeReceipt(tx, in, now, ReceiptApplied, uint64(now.UTC().UnixMilli()), "", "pm:service_item:"+in.ResourceID, outcome)
	}
	return writeReceipt(tx, in, now, ReceiptRejected, 0, versionConflictReason, "", outcome)
}
