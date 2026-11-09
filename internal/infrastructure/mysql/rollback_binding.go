package mysql

import (
	"encoding/json"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pendingRollbacks 在服务项锁内以当前读取得尚未终决的回退申请。
// 参数：tx为已锁定服务项的事务，item为租户及服务项身份。
// 返回值：待决申请；载荷损坏或数据库读取失败时返回错误。
func pendingRollbacks(tx *gorm.DB, item *serviceItemRecord) ([]deliveryEventRecord, error) {
	var rows []deliveryEventRecord
	kinds := []string{application.EventRollbackRequested, application.EventRollbackApproved,
		application.EventRollbackRejected, application.EventRollbackWithdrawn}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"tenant_id=? AND service_item_id=? AND event_type IN ?", item.TenantID, item.ID, kinds).
		Order("created_at,id").Find(&rows).Error; err != nil {
		return nil, err
	}
	closed := map[string]bool{}
	for _, row := range rows {
		if row.EventType == application.EventRollbackRequested {
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
		if row.EventType == application.EventRollbackRequested && !closed[row.ID] {
			pending = append(pending, row)
		}
	}
	return pending, nil
}

// rollbackCycleStartedAt 取得当前实施轮次的稳定业务边界。
// 参数：item为已锁定的服务项，状态时间为空时表示历史初始轮次。
// 返回值：UTC标准时间文本；无边界时返回空字符串。
func rollbackCycleStartedAt(item *serviceItemRecord) string {
	if item.StatusChangedAt == nil {
		return ""
	}
	return item.StatusChangedAt.UTC().Format(time.RFC3339Nano)
}

// validateRollbackRequest 限制单个待决回退并由服务端冻结原修订与实施轮次。
// 参数：tx/item为服务项锁内事务，event为携带原因及类型的申请。
// 返回值：非法状态、重复待决或存储错误时拒绝，成功时补齐持久化绑定。
func validateRollbackRequest(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	kind := stringValue(event.Payload, "kind")
	allowed := kind == "FIELD_TO_PREPARATION" && item.Status == "实施中"
	allowed = allowed || kind == "REPORT_TO_FIELD" && item.Status == "现场实施完成" &&
		(item.ReportStatus == "RECTIFYING" || item.ReportStatus == "COMPILING" || item.ReportStatus == "REVIEWED")
	if !allowed {
		return application.ErrConflict
	}
	pending, err := pendingRollbacks(tx, item)
	if err != nil {
		return err
	}
	for _, request := range pending {
		var value struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(request.Payload, &value) != nil || value.Kind == kind {
			return application.ErrConflict
		}
	}
	event.Payload["rollback_revision"] = item.ReportRevision
	event.Payload["rollback_cycle_started_at"] = rollbackCycleStartedAt(item)
	return nil
}

// validateRollbackDecision 只允许独立审批人或原申请人关闭同租户的待决申请。
// 参数：tx/item为锁定服务项，event为批准、驳回或撤回命令。
// 返回值：原申请；身份、终态或批准轮次不符时返回冲突或禁止错误。
func validateRollbackDecision(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) (deliveryEventRecord, error) {
	pending, err := pendingRollbacks(tx, item)
	if err != nil {
		return deliveryEventRecord{}, err
	}
	for _, request := range pending {
		if request.ID != stringValue(event.Payload, "request_id") {
			continue
		}
		withdrawing := event.Type == application.EventRollbackWithdrawn
		if (request.ActorUserID == event.ActorUserID) != withdrawing {
			return request, application.ErrForbidden
		}
		var value struct {
			Kind     string  `json:"kind"`
			Revision *uint64 `json:"rollback_revision"`
			Cycle    *string `json:"rollback_cycle_started_at"`
			Version  uint64  `json:"expected_version"`
		}
		if json.Unmarshal(request.Payload, &value) != nil {
			return request, application.ErrConflict
		}
		if event.Type == application.EventRollbackApproved {
			// 历史申请没有轮次绑定时仅接受原版本，不能猜测它属于后来重开的轮次。
			if value.Revision == nil || value.Cycle == nil {
				if value.Version == 0 || value.Version != item.Version {
					return request, application.ErrConflict
				}
			} else if *value.Revision != item.ReportRevision || *value.Cycle != rollbackCycleStartedAt(item) {
				return request, application.ErrConflict
			}
		}
		event.Payload["kind"] = value.Kind
		return request, nil
	}
	return deliveryEventRecord{}, application.ErrConflict
}

// closeOtherRollbacks 在批准回退时关闭历史遗留兄弟申请，保留自动失效的因果证据。
// 参数：tx/item为原轮次事务，event为已核验的批准命令。
// 返回值：任一事件或审计写入失败则整体回滚。
func closeOtherRollbacks(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	pending, err := pendingRollbacks(tx, item)
	if err != nil {
		return err
	}
	for _, request := range pending {
		if request.ID == stringValue(event.Payload, "request_id") {
			continue
		}
		closed := domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: item.TenantID,
			ProjectID: item.ProjectID, ServiceItemID: item.ID, Type: application.EventRollbackRejected,
			ActorUserID: "SYSTEM", CreatedAt: event.CreatedAt, Payload: map[string]any{
				"request_id": request.ID, "automatic": true, "cause_event_id": event.ID,
				"initiated_by": event.ActorUserID, "comment": "原业务轮次已回退，本申请自动失效"}}
		if err := createEvent(tx, closed); err != nil {
			return err
		}
	}
	return nil
}
