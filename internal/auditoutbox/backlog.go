package auditoutbox

import (
	"context"
	"errors"
)

// BacklogCounts 是审计 outbox 的积压快照（C2 指标口径）。
type BacklogCounts struct {
	// Pending 是仍需投递的行数：待投递、退避重试、租约中以及未来新增的未完成状态。
	Pending int64
	// Dead 是达到重试上限、停在终态失败等待人工处理的行数。
	Dead int64
}

type outboxStatusCount struct {
	DeliveryStatus string `gorm:"column:delivery_status"`
	Total          int64  `gorm:"column:total"`
}

// CountBacklog 汇总表积压：pending 含 pending/retry/processing，dead 为终态 failed。
// 参数：ctx 用于查询超时与取消。
// 返回值：积压快照；数据库不可用时返回错误，调用方必须把失败当成"未知"而不是 0。
func (s *GORMStore) CountBacklog(ctx context.Context) (BacklogCounts, error) {
	if s == nil || s.DB == nil {
		return BacklogCounts{}, errors.New("audit outbox store unavailable")
	}
	var rows []outboxStatusCount
	if err := s.DB.WithContext(ctx).Model(&outboxRow{}).
		Select("delivery_status, COUNT(*) AS total").Group("delivery_status").Scan(&rows).Error; err != nil {
		return BacklogCounts{}, err
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		counts[row.DeliveryStatus] = row.Total
	}
	return backlogFromStatusCounts(counts), nil
}

// backlogFromStatusCounts 把按状态分组的行数折算成 pending/dead 两种积压。
// 参数：counts 为 delivery_status 到行数的映射。
// 返回值：已投递不计积压；failed 计入 dead；其余状态一律计入 pending。
func backlogFromStatusCounts(counts map[string]int64) BacklogCounts {
	var result BacklogCounts
	for status, total := range counts {
		if total <= 0 {
			continue
		}
		switch status {
		case "delivered":
			// 已成功投递不构成积压。
		case "failed":
			result.Dead += total
		default:
			result.Pending += total
		}
	}
	return result
}
