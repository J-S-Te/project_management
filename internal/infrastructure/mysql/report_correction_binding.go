package mysql

import (
	"encoding/json"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// correctionRevision 读取更正申请冻结的修订号，保留合法修订0与缺失的区别。
// 参数：payload为原申请持久化载荷，不能使用审批调用者自报的修订号。
// 返回值：冻结修订；缺失、负数或非法类型返回冲突。
func correctionRevision(payload []byte) (uint64, error) {
	var value struct {
		Revision *uint64 `json:"old_revision"`
	}
	if json.Unmarshal(payload, &value) != nil || value.Revision == nil {
		return 0, application.ErrConflict
	}
	return *value.Revision, nil
}

// pendingReportCorrections 在服务项锁内读取最新更正请求及终决议，防止双会话写入。
// 参数：tx为持有服务项行锁的事务，item为同租户服务项。
// 返回值：未处理原申请；读取或既有终决议载荷异常时返回错误。
func pendingReportCorrections(tx *gorm.DB, item *serviceItemRecord) ([]deliveryEventRecord, error) {
	var rows []deliveryEventRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"tenant_id=? AND service_item_id=? AND event_type IN ?", item.TenantID, item.ID,
		[]string{application.EventReportCorrectionRequested, application.EventReportCorrectionApproved,
			application.EventReportCorrectionRejected}).Order("created_at,id").Find(&rows).Error; err != nil {
		return nil, err
	}
	closed := map[string]bool{}
	for _, row := range rows {
		if row.EventType == application.EventReportCorrectionRequested {
			continue
		}
		var value struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(row.Payload, &value) != nil || value.RequestID == "" {
			return nil, application.ErrConflict
		}
		closed[value.RequestID] = true
	}
	var pending []deliveryEventRecord
	for _, row := range rows {
		if row.EventType == application.EventReportCorrectionRequested && !closed[row.ID] {
			pending = append(pending, row)
		}
	}
	return pending, nil
}

// validateReportCorrectionRequest 在行锁事务内限定当前已签发修订只能有一个待决申请。
// 参数：tx/item为服务项事务，event为申请命令及其冻结修订。
// 返回值：报告状态、修订或同修订待决冲突时拒绝，不改变业务记录。
func validateReportCorrectionRequest(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	if item.Status != "现场实施完成" || (item.ReportStatus != "ISSUED" && item.ReportStatus != "ARCHIVED") {
		return application.ErrConflict
	}
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	revision, err := correctionRevision(raw)
	if err != nil || revision != item.ReportRevision {
		return application.ErrConflict
	}
	pending, err := pendingReportCorrections(tx, item)
	if err != nil {
		return err
	}
	for _, request := range pending {
		old, err := correctionRevision(request.Payload)
		if err != nil || old == revision {
			return application.ErrConflict
		}
	}
	return nil
}

// closeSupersededReportCorrections 留痕关闭旧版遗留的其他申请，不伪装人工驳回意见。
// 参数：tx/item为原修订事务，event为实际批准事件并记录自动失效的因果来源。
// 返回值：任一证据写入失败则整体回滚更正，不留下半完成版本。
func closeSupersededReportCorrections(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	pending, err := pendingReportCorrections(tx, item)
	if err != nil {
		return err
	}
	for _, request := range pending {
		if request.ID == stringValue(event.Payload, "request_id") {
			continue
		}
		revision, err := correctionRevision(request.Payload)
		if err != nil || revision > item.ReportRevision {
			return application.ErrConflict
		}
		closed := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: item.TenantID,
			ProjectID: item.ProjectID, ServiceItemID: item.ID, ActorUserID: "SYSTEM",
			Type: application.EventReportCorrectionRejected, CreatedAt: event.CreatedAt,
			Payload: map[string]any{"request_id": request.ID, "old_revision": revision,
				"automatic": true, "cause_event_id": event.ID, "initiated_by": event.ActorUserID,
				"comment": "原报告修订已由其他更正申请替代，本申请自动失效"}}
		if err := createEvent(tx, closed); err != nil {
			return err
		}
	}
	return nil
}
