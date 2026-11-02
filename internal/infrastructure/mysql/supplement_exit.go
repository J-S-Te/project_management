package mysql

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// clearSupplementStatusAfterConfirm 在确认拆解的事务内判定补充协议分支能否受控退出
// （BIZ-PROJECT-04）。
//
// 修复前仓储无条件把 supplement_status 清零，"再点一次确认"就能抹掉补充协议分支。
// 现在退出必须以事件账本里的证据为准：最近一次拆解调整（DECOMPOSITION_ADJUSTED）
// 必须存在且其生效范围指纹与项目当前拆解结果一致；否则失败关闭，由应用层提示用户
// 先做「拆解调整」（补充协议目录已应用）或显式「补充协议关账」，既不会静默清除，
// 也不会把项目永久钉在补充协议分支。
func clearSupplementStatusAfterConfirm(tx *gorm.DB, tenantID, projectID string, now time.Time) error {
	var project projectRecord
	// 与事件路径同一加锁顺序（先服务项、后项目行）：确认流程已经锁定服务项行，
	// 这里锁定项目行，后续 syncProjectStatusColumn 复用同一把锁。
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", tenantID, projectID).First(&project).Error; err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(project.SupplementStatus), "REQUIRED") {
		applied, err := appliedSupplementScope(tx, tenantID, projectID)
		if err != nil {
			return err
		}
		var records []serviceItemRecord
		if err := tx.Where("tenant_id = ? AND project_id = ?", tenantID, projectID).Find(&records).Error; err != nil {
			return err
		}
		current := make([]domain.ServiceItem, 0, len(records))
		for _, record := range records {
			current = append(current, serviceFromRecord(record))
		}
		if err := application.VerifySupplementExit(applied, current); err != nil {
			return err
		}
	}
	return tx.Model(&projectRecord{}).Where("tenant_id = ? AND id = ?", tenantID, projectID).
		Updates(map[string]any{"supplement_status": "NONE", "updated_at": now}).Error
}

// appliedSupplementScope 读取项目最近一次拆解调整生效的范围指纹。
// 事件不存在返回 nil（表示尚未应用补充协议目录）；载荷无法解析同样按"未应用"处理，
// 失败关闭而不是把无证据当成证据。
func appliedSupplementScope(tx *gorm.DB, tenantID, projectID string) ([]string, error) {
	var event deliveryEventRecord
	err := tx.Where("tenant_id = ? AND project_id = ? AND event_type = ?", tenantID, projectID, application.EventDecompositionAdjusted).
		Order("created_at DESC, id DESC").Limit(1).Take(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var payload struct {
		ServiceItems []domain.ServiceItem `json:"service_items"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || len(payload.ServiceItems) == 0 {
		return nil, nil
	}
	return application.DecompositionScopeSnapshot(payload.ServiceItems), nil
}
