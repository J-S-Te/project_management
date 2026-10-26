package mysql

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/auditctx"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func applyItemEvent(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	// 先前置读取、再事务行锁之间可能已有其他命令完成。撤销命令把客户端看到的版本
	// 带入事件，在锁内二次比较，避免后到的撤销覆盖最新责任链。
	if expected := expectedVersionValue(event.Payload); expected != 0 && item.Version != expected {
		return application.ErrConflict
	}
	updates := map[string]any{"updated_at": event.CreatedAt, "updated_by": event.ActorUserID}
	switch event.Type {
	case application.EventTeamAssigned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = stringValue(event.Payload, "team_lead_id")
	case application.EventTeamAssignmentRevoked:
		if item.Status != "待分配" || item.TeamLeadID == "" {
			return application.ErrValidation
		}
		updates["team_lead_id"] = ""
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		// required_codes 是服务项的必检能力基线（来自检测类别配置），撤销分配只是解除人员/设备，
		// 不能把它一起清空：否则重新分配时应用层只能拿到客户端追加要求，
		// 可以把任务分给不具备必备资质的人员（资质校验被绕过）。
		updates["conflict_status"] = "UNCHECKED"
	case application.EventDecompositionReturned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		updates["status"] = "待确认"
		updates["team_lead_id"] = ""
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		updates["conflict_status"] = "UNCHECKED"
		updates["tech_review_status"] = "NONE"
		updates["tech_reviewed_at"] = nil
		updates["tech_reviewed_by"] = ""
		updates["tech_review_comment"] = ""
	case application.EventExecutionTeamAssigned:
		if item.Status != "待分配" {
			return application.ErrValidation
		}
		if item.TeamLeadID == "" {
			return application.ErrValidation
		}
		updates["project_manager_id"] = stringValue(event.Payload, "project_manager_id")
		updates["engineer_ids"] = jsonValue(event.Payload["engineer_ids"])
		updates["equipment_ids"] = jsonValue(event.Payload["equipment_ids"])
		updates["required_codes"] = jsonValue(event.Payload["required_codes"])
		updates["conflict_status"] = stringValue(event.Payload, "conflict_status")
	case application.EventExecutionAssignmentRevoked:
		if item.Status != "待分配" || item.TeamLeadID == "" || item.ProjectManagerID == "" {
			return application.ErrValidation
		}
		updates["project_manager_id"] = ""
		updates["engineer_ids"] = jsonValue([]string{})
		updates["equipment_ids"] = jsonValue([]string{})
		// 同上：撤销执行分配不得清空必检能力基线。
		updates["conflict_status"] = "UNCHECKED"
	case application.EventImplementationPlanned:
		// 与 PlanImplementation 共用同一套前置规则：行锁内复查可覆盖读后状态变化的竞态，
		// 并且仍然返回可执行的原因而不是笼统的参数错误。
		if err := application.CheckImplementationPlanPrecondition(application.PlanPrecondition{
			Status: item.Status, ProjectManagerID: item.ProjectManagerID,
			ConflictStatus: item.ConflictStatus, Special: item.Special, TechReviewStatus: item.TechReviewStatus,
		}); err != nil {
			return err
		}
		if item.TestMode == "PENETRATION" && stringValue(event.Payload, "penetration_test_plan") == "" {
			return application.ValidationError("请填写渗透测试专项计划")
		}
		updates["planned_start"] = rfc3339Value(event.Payload, "planned_start")
		updates["planned_end"] = rfc3339Value(event.Payload, "planned_end")
		updates["status"] = "待实施"
		if err := upsertImplPlan(tx, item, event); err != nil {
			return err
		}
	case application.EventImplementationPlanRevoked:
		if item.Status != "待实施" {
			return application.ErrValidation
		}
		if err := clearImplPlan(tx, item, event, true); err != nil {
			return err
		}
		updates["planned_start"] = nil
		updates["planned_end"] = nil
		updates["status"] = "待分配"
	case application.EventSpecialMethodReviewed:
		if item.Special != "是" {
			return application.ErrValidation
		}
		decision := strings.ToUpper(stringValue(event.Payload, "decision"))
		if decision != "APPROVED" && decision != "REJECTED" {
			return application.ErrValidation
		}
		if item.TechReviewStatus != "PENDING" && item.TechReviewStatus != "REJECTED" {
			return application.ErrValidation
		}
		updates["tech_review_status"] = decision
		updates["tech_reviewed_at"] = event.CreatedAt
		updates["tech_reviewed_by"] = event.ActorUserID
		updates["tech_review_comment"] = stringValue(event.Payload, "comment")
	case application.EventReportStatusUpdated:
		if item.Status != "现场实施完成" {
			return application.ErrValidation
		}
		phase := strings.ToUpper(stringValue(event.Payload, "phase"))
		if !allowedReportPhase(phase) {
			return application.ErrValidation
		}
		current := reportPhaseRank(item.ReportStatus)
		next := reportPhaseRank(phase)
		if next != current+1 {
			return application.ErrValidation
		}
		if err := validatePenetrationReportGate(tx, item, phase); err != nil {
			return err
		}
		revision, err := lockReportRevision(tx, item)
		if err != nil {
			return err
		}
		if revision.ValidityStatus != "ACTIVE" || reportPhaseRank(revision.Status) != current {
			return application.ErrConflict
		}
		if err := advanceReportRevision(tx, &revision, phase, event); err != nil {
			return err
		}
		updates["report_status"] = phase
		updates["report_updated_at"] = event.CreatedAt
		updates["report_updated_by"] = event.ActorUserID
		switch phase {
		case "COMPILING":
			updates["report_prepared_by"] = event.ActorUserID
		case "REVIEWED":
			updates["report_reviewed_by"] = event.ActorUserID
		case "ISSUED":
			updates["report_issued_by"] = event.ActorUserID
		}
	case application.EventEquipmentReturned:
		// 设备归还只能作用于已进入交付的服务项：待确认/待复核/待分配还没有设备清单可还，
		// 已终止是终态，允许事后归还等于让终态数据可被改写。
		if item.Status == "待确认" || item.Status == "待复核" || item.Status == "待分配" || item.Status == domain.ProjectStatusTerminated {
			return application.ErrValidation
		}
		if err := markEquipmentReturned(tx, item, event); err != nil {
			return err
		}
	case application.EventPreparationStarted:
		// 首次准备从“待实施”进入；现场回退会回到“实施准备中”，允许负责人重新核验
		// 设备与行程后再次提交准备。新的 PREPARATION_STARTED 事件是重新进入现场节点
		// 的明确边界，避免刚回退的项目继续残留在现场实施列表。
		if !canStartPreparation(item.Status) {
			return application.ErrValidation
		}
		if err := syncEquipmentReservations(tx, item, event); err != nil {
			return err
		}
		if err := updateImplPlanEquipment(tx, item, event); err != nil {
			return err
		}
		updates["status"] = "实施准备中"
	case application.EventPreparationRevoked:
		if item.Status != "实施准备中" {
			return application.ErrValidation
		}
		if err := deleteEquipmentReservations(tx, item.TenantID, item.ID, ""); err != nil {
			return err
		}
		if err := clearImplPlan(tx, item, event, false); err != nil {
			return err
		}
		updates["status"] = "待实施"
	case application.EventRollbackApproved:
		if err := validateRollbackApproval(tx, item, event); err != nil {
			return err
		}
		switch stringValue(event.Payload, "kind") {
		case "FIELD_TO_PREPARATION":
			if item.Status != "实施中" {
				return application.ErrValidation
			}
			updates["status"] = "实施准备中"
		case "REPORT_TO_FIELD":
			if item.Status != "现场实施完成" || (item.ReportStatus != "COMPILING" && item.ReportStatus != "REVIEWED") {
				return application.ErrValidation
			}
			// 报告返工：把当前修订标记为 SUPERSEDED（保留其文件与审批证据），并开一个新修订，
			// 否则重新编制时 lockReportRevision 会发现旧修订仍停在 COMPILING/REVIEWED 而返回冲突。
			if err := reworkReportRevision(tx, item, event); err != nil {
				return err
			}
			updates["status"] = "实施中"
			updates["report_status"] = "NONE"
			updates["report_revision"] = item.ReportRevision + 1
			updates["report_updated_at"] = event.CreatedAt
			updates["report_updated_by"] = event.ActorUserID
		default:
			return application.ErrValidation
		}
	case application.EventRollbackWithdrawn:
		if err := validateRollbackWithdrawal(tx, item, event); err != nil {
			return err
		}
	case application.EventReportCorrectionApproved:
		if err := validateReportCorrectionApproval(tx, item, event); err != nil {
			return err
		}
		if item.Status != "现场实施完成" || (item.ReportStatus != "ISSUED" && item.ReportStatus != "ARCHIVED") {
			return application.ErrValidation
		}
		if err := invalidateAndCreateReportRevision(tx, item, event); err != nil {
			return err
		}
		updates["report_status"] = "NONE"
		updates["report_revision"] = item.ReportRevision + 1
		updates["report_prepared_by"] = ""
		updates["report_reviewed_by"] = ""
		updates["report_issued_by"] = ""
		updates["report_updated_at"] = event.CreatedAt
		updates["report_updated_by"] = event.ActorUserID
	case application.EventFieldStarted:
		if item.Status != "实施准备中" {
			return application.ErrValidation
		}
		updates["status"] = "实施中"
	case application.EventFieldRecordSubmitted:
		// 开始现场测评是独立动作；记录只能追加到已经进入实施中的服务项。
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		if err := persistEvidenceFiles(tx, item, event, "FIELD"); err != nil {
			return err
		}
	case application.EventFieldCheckedIn:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
	case application.EventFieldSignatureCaptured:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		if err := persistEvidenceFiles(tx, item, event, "FIELD"); err != nil {
			return err
		}
	case application.EventFieldCompleted:
		// 按服务项确认现场完成：先做完的项不必等项目里最后一个动作"顺带"完成。
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		if err := validatePenetrationFieldCompletionGate(tx, item); err != nil {
			return err
		}
		missing, err := missingFieldEvidence(tx, item.TenantID, item.ID)
		if err != nil {
			return err
		}
		if len(missing) > 0 && strings.TrimSpace(stringValue(event.Payload, "incomplete_reason")) == "" {
			return application.PreconditionError("现场证据尚不完整（缺少" + strings.Join(missing, "、") + "）；确认确实无法补齐时请填写缺项说明")
		}
		updates["status"] = "现场实施完成"
		updates["report_status"] = "NONE"
		if err := ensureReportRevision(tx, item, event.CreatedAt); err != nil {
			return err
		}
	case application.EventDeviationReported:
		if item.Status != "实施中" {
			return application.ErrValidation
		}
		updates["status"] = "异常处理中"
		if err := persistEvidenceFiles(tx, item, event, "DEVIATION"); err != nil {
			return err
		}
	case application.EventDeviationReviewed:
		// 评审必须绑定「当前待处理的偏离实例」：
		//   1) 被评审的偏离必须仍是该服务项最新上报的那一个（旧偏离的评审不能作用于新异常）；
		//   2) 同一偏离重复评审只做幂等返回，不再改变服务项状态。
		reviewedDeviationID := stringValue(event.Payload, "deviation_id")
		if reviewedDeviationID == "" {
			return application.ErrValidation
		}
		var latestReported deliveryEventRecord
		if err := tx.Where("tenant_id=? AND service_item_id=? AND event_type=?", event.TenantID, event.ServiceItemID, application.EventDeviationReported).
			Order("created_at DESC, id DESC").First(&latestReported).Error; err != nil {
			return mapNotFound(err)
		}
		var reportedPayload map[string]any
		if err := json.Unmarshal(latestReported.Payload, &reportedPayload); err != nil {
			return err
		}
		if stringValue(reportedPayload, "deviation_id") != reviewedDeviationID {
			// 历史偏离：已不是当前异常，拒绝而不是放行/终止当前服务项。
			return application.ErrConflict
		}
		var alreadyReviewed int64
		if err := tx.Model(&deliveryEventRecord{}).
			Where("tenant_id=? AND service_item_id=? AND event_type=? AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deviation_id'))=?",
				event.TenantID, event.ServiceItemID, application.EventDeviationReviewed, reviewedDeviationID).
			Count(&alreadyReviewed).Error; err != nil {
			return err
		}
		if alreadyReviewed > 0 {
			// 幂等：同一偏离的重复评审不再改变状态。
			return nil
		}
		if item.Status != "异常处理中" {
			return application.ErrValidation
		}
		status, ok := deviationReviewStatus(stringValue(event.Payload, "decision"))
		if !ok {
			return application.ErrValidation
		}
		updates["status"] = status
		if status == domain.ProjectStatusTerminated {
			if err := deleteEquipmentReservations(tx, item.TenantID, item.ID, ""); err != nil {
				return err
			}
		}
	default:
		// 审计类事件只留痕、不改服务项状态。其余未知类型必须显式拒绝：
		// 静默成功会让未接线的新事件在接口层返回成功、审计流显示「已发生」，
		// 而业务状态停在原地，排查时无从判断。
		if isAuditOnlyEvent(event.Type) {
			return nil
		}
		return application.ErrValidation
	}
	// 状态真正变化时记录「进入当前状态的时刻」，作为 SLA 停留时长的唯一基准。
	if _, changed := updates["status"]; changed {
		updates["status_changed_at"] = event.CreatedAt
	}
	// 条件更新 + 版本自增：把"写入必须基于事务内读到的那一版"写成显式不变量。
	// 当前调用方已在事务内加了 FOR UPDATE，这是防御性的第二道闸；
	// 将来若有旁路写路径忘记加锁，会以冲突而不是静默覆盖收场。
	updates["version"] = gorm.Expr("version + 1")
	result := tx.Model(&serviceItemRecord{}).
		Where("tenant_id=? AND id=? AND version=?", item.TenantID, item.ID, item.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

func deviationReviewStatus(decision string) (string, bool) {
	switch decision {
	case "RELEASE", "RETEST":
		// 原型 FR-PJ-304 明确要求重测回到实施中重新执行，而不是重新走计划/准备。
		return "实施中", true
	case "TERMINATE":
		return "已终止", true
	default:
		return "", false
	}
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func missingFieldEvidence(tx *gorm.DB, tenantID, itemID string) ([]string, error) {
	required := []struct{ eventType, label string }{
		{application.EventFieldCheckedIn, "GPS 签到"},
		{application.EventFieldRecordSubmitted, "现场记录"},
		{application.EventFieldSignatureCaptured, "电子签名"},
	}
	missing := make([]string, 0, len(required))
	for _, requirement := range required {
		var count int64
		if err := tx.Model(&deliveryEventRecord{}).Where("tenant_id=? AND service_item_id=? AND event_type=?", tenantID, itemID, requirement.eventType).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			missing = append(missing, requirement.label)
		}
	}
	return missing, nil
}

func canStartPreparation(status string) bool {
	return status == "待实施" || status == "实施准备中"
}

// isAuditOnlyEvent 列出不改变服务项状态、仅用于留痕的事件类型。
func isAuditOnlyEvent(eventType string) bool {
	switch eventType {
	case application.EventContractActivated, application.EventContractStampStatus,
		application.EventWarningTriggered, application.EventAutomationTriggered,
		application.EventRollbackRequested, application.EventRollbackRejected,
		application.EventReportCorrectionRequested, application.EventReportCorrectionRejected:
		return true
	}
	return false
}

func applyProjectEvent(tx *gorm.DB, project *projectRecord, event domain.DeliveryEvent) error {
	updates := map[string]any{"updated_at": event.CreatedAt}
	switch event.Type {
	case application.EventScopeChangeDetected:
		// 范围变更检测命中：项目进入补充协议处理中，等待合同回写后重新确认拆解。
		updates["supplement_status"] = "REQUIRED"
		updates["status"] = "补充协议处理中"
	case application.EventDecompositionAdjusted:
		var existing int64
		if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=? AND status NOT IN ?", project.TenantID, project.ID, []string{"待确认", "待复核"}).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return application.ErrValidation
		}
		encoded, _ := json.Marshal(event.Payload["service_items"])
		var items []domain.ServiceItem
		if err := json.Unmarshal(encoded, &items); err != nil || len(items) == 0 {
			return application.ErrValidation
		}
		var oldItems []serviceItemRecord
		if err := tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Find(&oldItems).Error; err != nil {
			return err
		}
		// 实施计划和交付事件属于旧服务项的历史证据，随服务项归档保留，不做物理删除。
		if err := tx.Where("tenant_id=? AND project_id=?", project.TenantID, project.ID).Delete(&serviceItemRecord{}).Error; err != nil {
			return err
		}
		// 拆解调整的服务项由应用层只带业务字段，编号必须在这里补齐：
		// 主键为空会让多行互相冲突而整单回滚，单行则会落成 id='' 的孤儿行——
		// 事件分发以 service_item_id != "" 判定，该服务项之后再也无法被确认或实施。
		maxSequence := 0
		for _, old := range oldItems {
			if sequence := serviceItemSequence(old.ID); sequence > maxSequence {
				maxSequence = sequence
			}
		}
		for index, item := range items {
			if strings.TrimSpace(item.ID) == "" {
				item.ID = serviceItemIDFor(project.ID, maxSequence+index+1)
			}
			rec := serviceItemRecord{ID: item.ID, TenantID: project.TenantID, ProjectID: project.ID, SourceServiceID: item.SourceServiceID, Batch: item.Batch, Site: item.Site, SiteCode: item.SiteCode, Category: item.Category, Requirement: item.Requirement, System: item.System, SystemLevel: item.SystemLevel, SystemStandard: item.SystemStandard, RequiredCodes: jsonValue(item.RequiredCodes), Special: item.Special, TestMode: item.TestMode, Status: item.Status, TechReviewStatus: item.TechReviewStatus, ConflictStatus: item.ConflictStatus, StatusChangedAt: &event.CreatedAt, CreatedAt: event.CreatedAt, UpdatedAt: event.CreatedAt, UpdatedBy: event.ActorUserID}
			if err := tx.Create(&rec).Error; err != nil {
				return err
			}
		}
		updates["services"] = len(items)
		updates["supplement_status"] = "REQUIRED"
		updates["status"] = "补充协议处理中"
	}
	if len(updates) == 1 {
		return nil
	}
	// 项目业务版本：仅在项目自身内容变更时自增（派生状态缓存同步不算业务变更）。
	// 与条件更新配对，让"我基于的是旧版本"这类并发冲突能被显式发现而不是静默覆盖。
	updates["version"] = gorm.Expr("version + 1")
	result := tx.Model(&projectRecord{}).
		Where("tenant_id=? AND id=? AND version=?", project.TenantID, project.ID, project.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return application.ErrConflict
	}
	return nil
}

// serviceItemIDFor 生成服务项编号，与创建、合同激活路径保持同一语义：SI-<项目后缀>-NNN。

func serviceItemIDFor(projectID string, sequence int) string {
	return fmt.Sprintf("SI-%s-%03d", strings.TrimPrefix(projectID, "PJ-"), sequence)
}

// serviceItemSequence 解析既有服务项编号末段的序号；无法解析时返回 0，
// 使拆解调整总能从 1 开始顺延而不是与归档行重号。
func serviceItemSequence(itemID string) int {
	parts := strings.Split(strings.TrimSpace(itemID), "-")
	if len(parts) == 0 {
		return 0
	}
	sequence, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || sequence < 0 {
		return 0
	}
	return sequence
}

// syncProjectStatusColumn 在事件事务提交前按派生规则重算并回写 pm_project.status。
// 与读侧（ListProjects/GetProject/Dashboard/FindProjectByContract）共用
// domain.DeriveProjectStatus 单一口径，保证存储列永不分叉：服务项状态推进到哪，
// 项目状态缓存就立刻对齐到哪，不再依赖某条事件手工设置项目状态。
func syncProjectStatusColumn(tx *gorm.DB, tenantID, projectID string) error {
	var project projectRecord
	// 与事件路径同一加锁顺序（先服务项、后项目行）：项目行锁把所有针对同一项目的
	// 写入串行化，随后的服务项投影读才是稳定快照，不会把落后一档的状态写回存储列。
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenantID, projectID).First(&project).Error; err != nil {
		return err
	}
	var items []domain.ProjectStatusItem
	if err := tx.Model(&serviceItemRecord{}).Where("tenant_id=? AND project_id=?", tenantID, projectID).Select("status, report_status").Scan(&items).Error; err != nil {
		return err
	}
	derived := domain.DeriveProjectStatus(items, project.SupplementStatus, project.Status)
	// 进度与状态同源：两者都由同一次服务项投影派生，避免进度退化成没人写的装饰字段。
	progress := domain.DeriveProjectProgress(items)
	return tx.Model(&projectRecord{}).Where("tenant_id=? AND id=?", tenantID, projectID).
		Updates(map[string]any{"status": derived, "progress": progress}).Error
}

// ListSlaOverdue 返回超期服务项：计划完成时间早于当前 UTC 且尚未进入终态。
// 与列表/仪表盘一致地套用服务项数据范围，超期时长由数据库按小时取整。

func auditActorType(eventType string) string {
	switch eventType {
	case "AUTOMATION_TRIGGERED", "WARNING_TRIGGERED":
		// 由 Worker 在源事件之后追加，属于系统自动执行，不能伪装成人工操作。
		return "SYSTEM"
	}
	return "USER"
}

func createEvent(tx *gorm.DB, event domain.DeliveryEvent) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	var operationID *string
	var capturedAt *time.Time
	if event.ClientOperationID != "" {
		value := event.ClientOperationID
		operationID = &value
	}
	if event.CapturedAt != nil {
		value := *event.CapturedAt
		capturedAt = &value
	}
	if err := tx.Create(&deliveryEventRecord{ID: event.ID, TenantID: event.TenantID, ProjectID: event.ProjectID, ServiceItemID: event.ServiceItemID, EventType: event.Type, ActorUserID: event.ActorUserID, ClientOperationID: operationID, CapturedAt: capturedAt, Payload: payload, CreatedAt: event.CreatedAt}).Error; err != nil {
		return err
	}
	if err := createDeliveryEffects(tx, event); err != nil {
		return err
	}
	// AUD-IP-2026 / 批次 2：审计事件与交付事件同事务落库，平台不可用时由投递器重试，
	// 不再出现"业务已提交、审计同步发送失败返 503"的不一致。
	meta := auditctx.FromContext(tx.Statement.Context)
	return tx.Create(&auditOutboxRecord{
		ID: ulid.Make().String(), TenantID: event.TenantID, EventID: ulid.Make().String(),
		ActorID: actorPointer(event.ActorUserID), ActorType: auditActorType(event.Type),
		Action:       "PROJECT_MANAGEMENT:" + string(event.Type),
		ResourceType: "project", ResourceID: event.ProjectID,
		LoginIP: auditIPBytes(meta.LoginIP), ClientIP: auditIPBytes(meta.ClientIP), IPSource: auditIPSource(meta.IPSource),
		Result: "SUCCESS", DeliveryStatus: "pending", NextAttemptAt: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	}).Error
}

// upsertImplPlan 把实施计划（含渗透测试专项合规要素）落到 pm_impl_plan，与服务项 1:1。
// 同名唯一键冲突时原地刷新，保证重复发布实施计划不会产生脏数据。
func upsertImplPlan(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	record := implPlanRecord{
		ID:                  ulid.Make().String(),
		TenantID:            item.TenantID,
		ServiceItemID:       item.ID,
		PlannedStart:        rfc3339Time(event.Payload, "planned_start"),
		PlannedEnd:          rfc3339Time(event.Payload, "planned_end"),
		SitePlan:            stringValue(event.Payload, "site_plan"),
		PenetrationTestPlan: stringValue(event.Payload, "penetration_test_plan"),
		AuthDocNo:           stringValue(event.Payload, "auth_doc_no"),
		AuthStart:           rfc3339Time(event.Payload, "auth_start"),
		AuthEnd:             rfc3339Time(event.Payload, "auth_end"),
		AuthScope:           stringValue(event.Payload, "auth_scope"),
		TestScope:           stringValue(event.Payload, "test_scope"),
		TestWindow:          stringValue(event.Payload, "test_window"),
		EmergencyContact:    stringValue(event.Payload, "emergency_contact"),
		RollbackPlan:        stringValue(event.Payload, "rollback_plan"),
		Personnel:           jsonBytes(event.Payload, "personnel"),
		UpdatedAt:           event.CreatedAt,
		UpdatedBy:           event.ActorUserID,
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "service_item_id"}}, DoUpdates: clause.AssignmentColumns([]string{"planned_start", "planned_end", "site_plan", "penetration_test_plan", "auth_doc_no", "auth_start", "auth_end", "auth_scope", "test_scope", "test_window", "emergency_contact", "rollback_plan", "personnel", "updated_at", "updated_by"})}).Create(&record).Error
}

// markEquipmentReturned 在计划行的设备清单里给对应设备写上归还时间。找不到该设备时按幂等
// 处理：可能是重复归还，或设备已被重新保存的清单替换，都不应报错。
func markEquipmentReturned(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	resourceID := strings.TrimSpace(stringValue(event.Payload, "resource_id"))
	if resourceID == "" {
		return application.ErrValidation
	}
	locked, err := lockItemEquipment(tx, item.TenantID, item.ID, nil)
	if err != nil {
		return err
	}
	// SQL 资源 ID 使用 MySQL 大小写及重音不敏感排序规则，
	// 而不可变 JSON 快照使用精确字符串。匹配快照或释放
	// 任何预留前，先解析规范主记录身份。
	var master capabilityRecord
	err = tx.Where("tenant_id=? AND resource_type=? AND resource_id=?", item.TenantID, "EQUIPMENT", resourceID).Take(&master).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var record implPlanRecord
	if err := tx.Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).Take(&record).Error; err != nil {
		return mapNotFound(err)
	}
	var resources []domain.PlanResource
	if len(record.Equipment) > 0 {
		if err := json.Unmarshal(record.Equipment, &resources); err != nil {
			return err
		}
	}
	updated := make([]domain.PlanResource, 0, len(resources))
	changed := false
	for _, resource := range resources {
		if resource.ResourceID == master.ResourceID && strings.TrimSpace(resource.ReturnedAt) == "" {
			resource.ReturnedAt = event.CreatedAt.Format(time.RFC3339)
			changed = true
		}
		updated = append(updated, resource)
	}
	if !changed {
		return nil
	}
	masterLocked := false
	for _, row := range locked {
		if row.ID == master.ID {
			masterLocked = true
			break
		}
	}
	if !masterLocked {
		return application.ErrConflict
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	if err := tx.Model(&implPlanRecord{}).Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).
		Updates(map[string]any{"equipment": encoded, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}).Error; err != nil {
		return err
	}
	return deleteEquipmentReservationRows(tx, item.TenantID, item.ID, master.ResourceID)
}

// updateImplPlanEquipment 把实施准备阶段确定的设备清单写回计划行。设备清单与人员清单
// 分列保存，因此重发计划不会覆盖设备，反之亦然。
func updateImplPlanEquipment(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	equipment := jsonBytes(event.Payload, "equipment")
	if len(equipment) == 0 {
		return nil
	}
	result := tx.Model(&implPlanRecord{}).
		Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).
		Updates(map[string]any{"equipment": equipment, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// 相同快照可能导致 MySQL 报告零行变更。应拒绝
		// 缺失计划，但不能将未变化的有效计划误判为冲突。
		var count int64
		if err := tx.Model(&implPlanRecord{}).Where("tenant_id=? AND service_item_id=?", item.TenantID, item.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return application.ErrConflict
		}
	}
	return nil
}

// clearImplPlan 只清除当前有效计划行，完整旧计划/设备快照仍保留在产生它的交付事件中。
// 计划撤销清空人员与合规要素；准备撤销仅释放设备预约，允许在同一计划上重新准备。
func deleteEquipmentReservations(tx *gorm.DB, tenantID, serviceItemID, resourceID string) error {
	if _, err := lockItemEquipment(tx, tenantID, serviceItemID, nil); err != nil {
		return err
	}
	return deleteEquipmentReservationRows(tx, tenantID, serviceItemID, resourceID)
}

func deleteEquipmentReservationRows(tx *gorm.DB, tenantID, serviceItemID, resourceID string) error {
	query := tx.Where("tenant_id = ? AND service_item_id = ?", tenantID, serviceItemID)
	if resourceID != "" {
		query = query.Where("resource_id = ?", resourceID)
	}
	return query.Delete(&equipmentReservationRecord{}).Error
}

type equipmentReservationRecord struct {
	TenantID      string    `gorm:"column:tenant_id;primaryKey"`
	ResourceID    string    `gorm:"column:resource_id;primaryKey"`
	UseDate       time.Time `gorm:"column:use_date;primaryKey"`
	ServiceItemID string    `gorm:"column:service_item_id"`
	WindowStart   time.Time `gorm:"column:window_start"`
	WindowEnd     time.Time `gorm:"column:window_end"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

func (equipmentReservationRecord) TableName() string { return "pm_equipment_reservation" }

// lockEquipmentResources 是删除及所有预留变更的
// 公共边界。所有调用方在访问每日记录或计划快照前，
// 均按相同顺序锁主记录；删除绝不锁服务项。
func lockEquipmentResources(tx *gorm.DB, tenantID string, ids []string) (map[string]capabilityRecord, error) {
	lookup := map[string]bool{}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !lookup[id] {
			keys = append(keys, id)
			lookup[id] = true
		}
	}
	locked := make(map[string]capabilityRecord, len(keys))
	if len(keys) == 0 {
		return locked, nil
	}
	if len(keys) == 1 {
		// 删除仅涉及一个资源，立即执行当前锁定读取，
		// 不能在等待并发准备提交计划及每日记录前，
		// 先创建可重复读快照。
		var row capabilityRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND resource_id=?", tenantID, "EQUIPMENT", keys[0]).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return locked, nil
		}
		if err != nil {
			return nil, err
		}
		locked[strings.ToLower(keys[0])] = row
		return locked, nil
	}
	// 加锁前解析规范主键。若仅排序请求字符串，
	// 在 MySQL 大小写及重音不敏感资源 ID 排序规则下，
	// 等价别名可能产生相反锁顺序。
	var candidates []capabilityRecord
	if err := tx.Where("tenant_id=? AND resource_type=? AND resource_id IN ?", tenantID, "EQUIPMENT", keys).Find(&candidates).Error; err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	for _, candidate := range candidates {
		var row capabilityRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND resource_type=? AND id=?", tenantID, "EQUIPMENT", candidate.ID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		locked[strings.ToLower(row.ResourceID)] = row
	}
	return locked, nil
}

func lockItemEquipment(tx *gorm.DB, tenantID, serviceItemID string, incoming []domain.PlanResource) (map[string]capabilityRecord, error) {
	var plan implPlanRecord
	err := tx.Where("tenant_id=? AND service_item_id=?", tenantID, serviceItemID).Take(&plan).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var resources []domain.PlanResource
	if len(plan.Equipment) > 0 {
		if err := json.Unmarshal(plan.Equipment, &resources); err != nil {
			return nil, err
		}
	}
	resources = append(resources, incoming...)
	var ids []string
	for _, row := range resources {
		ids = append(ids, row.ResourceID)
	}
	var previous []equipmentReservationRecord
	if err := tx.Where("tenant_id=? AND service_item_id=?", tenantID, serviceItemID).Find(&previous).Error; err != nil {
		return nil, err
	}
	for _, row := range previous {
		ids = append(ids, row.ResourceID)
	}
	return lockEquipmentResources(tx, tenantID, ids)
}

// syncEquipmentReservations 在与删除共用的锁下重检当前主状态，
// 再原子替换按日预留索引。
func syncEquipmentReservations(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent) error {
	encoded := jsonBytes(event.Payload, "equipment")
	// 显式 [] 表示释放设备。缺失快照不能在
	// 保留原有效计划的同时释放索引。
	if len(encoded) == 0 {
		return application.ErrValidation
	}
	var resources []domain.PlanResource
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &resources); err != nil {
			return err
		}
	}
	locked, err := lockItemEquipment(tx, item.TenantID, item.ID, resources)
	if err != nil {
		return err
	}
	if err := deleteEquipmentReservationRows(tx, item.TenantID, item.ID, ""); err != nil {
		return err
	}
	for _, resource := range resources {
		if strings.TrimSpace(resource.ReturnedAt) != "" || strings.TrimSpace(resource.ResourceID) == "" {
			continue
		}
		start, end, err := equipmentWindow(item, resource)
		if err != nil {
			return err
		}
		master, exists := locked[strings.ToLower(strings.TrimSpace(resource.ResourceID))]
		if !exists || master.Status != "ACTIVE" || master.UsageScope == domain.EquipmentUsageCompanyOnly ||
			(master.ValidFrom != nil && dayOnly(*master.ValidFrom).After(start)) ||
			(master.ValidUntil != nil && dayOnly(*master.ValidUntil).Before(end)) {
			return fmt.Errorf("%w: 设备「%s」已删除、停用或不再适用于使用时段，请重新选择设备", application.ErrConflict, resource.ResourceID)
		}
		for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
			row := &equipmentReservationRecord{TenantID: item.TenantID, ResourceID: master.ResourceID, UseDate: day, ServiceItemID: item.ID, WindowStart: start, WindowEnd: end, CreatedAt: event.CreatedAt}
			if err := tx.Create(row).Error; err != nil {
				if isDuplicateKey(err) || isDeadlock(err) {
					return fmt.Errorf("%w: 设备 %s 在 %s 已被其他服务项占用", application.ErrConflict, resource.ResourceID, day.Format("2006-01-02"))
				}
				return err
			}
		}
	}
	return nil
}

func equipmentWindow(item *serviceItemRecord, resource domain.PlanResource) (time.Time, time.Time, error) {
	startText, endText := strings.TrimSpace(resource.WindowStart), strings.TrimSpace(resource.WindowEnd)
	if startText == "" || endText == "" {
		if item.PlannedStart == nil || item.PlannedEnd == nil {
			return time.Time{}, time.Time{}, application.ErrValidation
		}
		return dayOnly(*item.PlannedStart), dayOnly(*item.PlannedEnd), nil
	}
	start, err := time.Parse("2006-01-02", startText)
	if err != nil {
		return time.Time{}, time.Time{}, application.ErrValidation
	}
	end, err := time.Parse("2006-01-02", endText)
	if err != nil || end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, application.ErrValidation
	}
	return start, end, nil
}

func dayOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func clearImplPlan(tx *gorm.DB, item *serviceItemRecord, event domain.DeliveryEvent, clearPlan bool) error {
	updates := map[string]any{"equipment": nil, "updated_at": event.CreatedAt, "updated_by": event.ActorUserID}
	if clearPlan {
		updates["planned_start"] = nil
		updates["planned_end"] = nil
		updates["site_plan"] = ""
		updates["penetration_test_plan"] = ""
		updates["auth_doc_no"] = ""
		updates["auth_start"] = nil
		updates["auth_end"] = nil
		updates["auth_scope"] = ""
		updates["test_scope"] = ""
		updates["test_window"] = ""
		updates["emergency_contact"] = ""
		updates["rollback_plan"] = ""
		updates["personnel"] = nil
	}
	return tx.Model(&implPlanRecord{}).Where("tenant_id = ? AND service_item_id = ?", item.TenantID, item.ID).Updates(updates).Error
}

// validateRollbackApproval 在服务项行锁保护下确认请求存在、属于当前项且从未被批准，防止双击
// 或两个审批人并发把同一补偿申请重复执行。
