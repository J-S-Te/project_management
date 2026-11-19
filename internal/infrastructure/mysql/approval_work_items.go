package mysql

import (
	"context"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

const legacyApprovalColumns = `req.id, req.project_id, req.service_item_id, pm_service_item.site,
 JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.reason')) AS reason, req.actor_user_id AS requester_user_id,
 '' AS approver_user_id, req.created_at, pm_service_item.version, pm_service_item.report_status,
 pm_service_item.status AS service_item_status,
 CASE WHEN req.event_type='REPORT_CORRECTION_REQUESTED' THEN 'REPORT_CORRECTION'
 WHEN JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.kind'))='REPORT_TO_FIELD' THEN 'REPORT_REWORK'
 ELSE 'FIELD_ROLLBACK' END AS type, COALESCE(decided.event_type,'') AS decision_type,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(decided.payload,'$.comment')),
 JSON_UNQUOTE(JSON_EXTRACT(decided.payload,'$.reason')),'') AS decision_comment, '' AS severity, '' AS decision`

// approvalWorkItems 将各来源的授权待办统一合并，整体排序和分页留给数据库。
// 参数：ctx/filter 为可信租户及数据范围，q 为服务端构造的用户能力和筛选条件。
// 返回值：可查询的统一待办；无权浏览指定类型时返回 nil。
func (r *Repository) approvalWorkItems(ctx context.Context, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	db := r.db.WithContext(ctx)
	parts := []any{}
	if q.TaskType == "" {
		parts = append(parts, r.approvalTaskQuery(ctx, filter, q).Select(legacyApprovalColumns))
		// 裸申请编号沿用旧详情语义，防止不同业务来源的标识互相覆盖。
		if q.RequestID != "" {
			return db.Table("(?) AS approval_work", parts[0])
		}
	}
	if (q.TaskType == "" || q.TaskType == domain.ApprovalSpecialMethod) &&
		(q.CanSpecial || q.View == "submitted" || q.RequestID != "") {
		parts = append(parts, specialMethodApprovalQuery(db, filter, q))
	}
	if q.TaskType == domain.ApprovalSpecialMethod && q.RequestID != "" {
		parts = append(parts, specialReviewResultQuery(db, filter, q))
	}
	if (q.TaskType == "" || q.TaskType == domain.ApprovalDeviation) && deviationApprovalVisible(q) {
		parts = append(parts, deviationApprovalQuery(db, filter, q))
	}
	if (q.TaskType == "" || q.TaskType == domain.ApprovalNativeScope) && nativeApprovalVisible(q) {
		parts = append(parts, nativeScopeApprovalQuery(db, filter, q))
	}
	if len(parts) == 0 {
		return nil
	}
	format := strings.TrimSuffix(strings.Repeat("? UNION ALL ", len(parts)), " UNION ALL ")
	return db.Table("(?) AS approval_work", db.Raw(format, parts...))
}

// approvalProjectItems 读取服务项和审批页面使用的项目派生状态。
// 参数：db 为数据库会话，filter 为可信租户与范围。
// 返回值：仅含当前可读在用服务项的查询。
func approvalProjectItems(db *gorm.DB, filter platform.ScopeFilter) *gorm.DB {
	return applyServiceItemScope(db.Table("pm_service_item"), db, filter).
		Joins(`JOIN pm_project ON pm_project.tenant_id=pm_service_item.tenant_id
 AND pm_project.id=pm_service_item.project_id`).
		Joins(derivedProjectStatusJoin(), filter.TenantID).
		Where("pm_service_item.archived_at IS NULL")
}

// approvalNodeItems 沿用原工作台的服务项和项目阶段门槛。
// 参数：db/filter 为会话和数据范围，status 为原栏目的业务状态。
// 返回值：仍处于该业务节点的服务项查询。
func approvalNodeItems(db *gorm.DB, filter platform.ScopeFilter, status string) *gorm.DB {
	return approvalProjectItems(db, filter).
		Where("pm_service_item.status = ?", status).
		Where(derivedProjectStatusPredicate(status), status)
}

// specialMethodApprovalQuery 读取尚待复核的特殊方法服务项。
// 参数：db/filter 为授权读取范围，q 可限定服务项的精确编号。
// 返回值：与统一待办列顺序一致的 SQL 查询。
func specialMethodApprovalQuery(db *gorm.DB, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	const requester = "JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.review_requester_user_id'))"
	query := approvalNodeItems(db, filter, domain.ProjectStatusPendingAllocation).
		Where("pm_service_item.special = ? AND pm_service_item.tech_review_status = ?", "是", "PENDING").
		Joins(`LEFT JOIN pm_delivery_event AS req ON req.id=(
 SELECT latest.id FROM pm_delivery_event AS latest
 WHERE latest.tenant_id=pm_service_item.tenant_id AND latest.service_item_id=pm_service_item.id
 AND latest.event_type='SPECIAL_METHOD_REVIEW_REQUESTED'
 ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1)`)
	if q.RequestID != "" {
		query = query.Where("pm_service_item.id = ?", q.RequestID)
	}
	if q.View == "submitted" || !q.CanSpecial {
		query = query.Where(requester+" = ?", q.UserID)
	}
	return query.Select(`pm_service_item.id, pm_service_item.project_id, pm_service_item.id AS service_item_id,
 pm_service_item.site, '特殊方法待复核' AS reason, COALESCE(`+requester+`,'') AS requester_user_id,
 '' AS approver_user_id, COALESCE(req.created_at, pm_service_item.status_changed_at, pm_service_item.created_at) AS created_at,
 pm_service_item.version, pm_service_item.report_status, pm_service_item.status AS service_item_status,
 ? AS type, '' AS decision_type, '' AS decision_comment, '' AS severity, '' AS decision`, domain.ApprovalSpecialMethod)
}

// deviationApprovalVisible 决定当前账号是否需要查询偏离候选。
// 参数：q 为当前读取视图、精确编号及既有评审权限。
// 返回值：可查看当前评审范围或本人上报时返回 true。
func deviationApprovalVisible(q domain.ApprovalQuery) bool {
	return q.CanDeviation || q.View == "submitted" || q.RequestID != ""
}

// specialReviewResultQuery 精确读取已保存的特殊方法决定，而非服务项的下一轮复核。
// 参数：db/filter 为授权范围，q 携带真实主体及原审批事件编号。
// 返回值：仅对原提交者或原复核权限持有人可见的决定查询。
func specialReviewResultQuery(db *gorm.DB, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	const requester = "JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.review_requester_user_id'))"
	const decision = "JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.decision'))"
	query := applyServiceItemScope(db.Table("pm_service_item"), db, filter).
		Joins(`JOIN pm_delivery_event AS req ON req.tenant_id=pm_service_item.tenant_id
 AND req.service_item_id=pm_service_item.id AND req.event_type='SPECIAL_METHOD_REVIEWED'`).
		Where("pm_service_item.archived_at IS NULL AND req.id = ?", q.RequestID).
		Where(decision + " IN ('APPROVED','REJECTED')")
	if !q.CanSpecial {
		query = query.Where(requester+" = ?", q.UserID)
	}
	return query.Select(`req.id, pm_service_item.project_id, pm_service_item.id AS service_item_id,
 pm_service_item.site, '特殊方法复核结果' AS reason, COALESCE(`+requester+`,'') AS requester_user_id,
 req.actor_user_id AS approver_user_id, req.created_at, pm_service_item.version, pm_service_item.report_status,
 pm_service_item.status AS service_item_status, ? AS type,
 CONCAT('SPECIAL_METHOD_', `+decision+`) AS decision_type,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.comment')),'') AS decision_comment,
 '' AS severity, `+decision+` AS decision`, domain.ApprovalSpecialMethod)
}

const deviationCoordinate = "JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.deviation_id'))"
const latestDeviationReport = `(
 SELECT latest.id FROM pm_delivery_event AS latest
 WHERE latest.tenant_id=pm_service_item.tenant_id
 AND latest.service_item_id=pm_service_item.id AND latest.event_type='DEVIATION_REPORTED'
 ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1)`
const matchingDeviationDecision = `(
 SELECT closed.id FROM pm_delivery_event AS closed
 WHERE closed.tenant_id=req.tenant_id AND closed.service_item_id=req.service_item_id
 AND closed.event_type='DEVIATION_REVIEWED'
 AND JSON_UNQUOTE(JSON_EXTRACT(closed.payload,'$.deviation_id'))=` + deviationCoordinate + `
 ORDER BY closed.created_at DESC, closed.id DESC LIMIT 1)`

// deviationApprovalQuery 查询当前待评审或按精确坐标查询历史处置结果。
// 参数：db/filter 为授权范围，q 为视图及可选偏离编号。
// 返回值：保留原偏离编号、问题描述及已保存结论的统一待办查询。
func deviationApprovalQuery(db *gorm.DB, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	query := approvalNodeItems(db, filter, domain.ProjectStatusException).
		Joins("JOIN pm_delivery_event AS req ON req.id=" + latestDeviationReport).
		Joins("LEFT JOIN pm_delivery_event AS decided ON decided.id=" + matchingDeviationDecision).
		Where("decided.id IS NULL AND " + deviationCoordinate + " NOT IN ('','null')")
	if q.RequestID != "" {
		query = deviationDetailQuery(db, filter, q.RequestID)
	}
	if q.View == "submitted" || !q.CanDeviation {
		query = query.Where("req.actor_user_id = ?", q.UserID)
	}
	const decision = "JSON_UNQUOTE(JSON_EXTRACT(decided.payload,'$.decision'))"
	return query.Select(deviationCoordinate+` AS id, pm_service_item.project_id,
 pm_service_item.id AS service_item_id, pm_service_item.site,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.description')),'') AS reason,
 req.actor_user_id AS requester_user_id, '' AS approver_user_id, req.created_at,
 pm_service_item.version, pm_service_item.report_status, pm_service_item.status AS service_item_status,
 ? AS type, CASE WHEN decided.id IS NULL THEN '' WHEN `+decision+`='RELEASE' THEN 'DEVIATION_APPROVED'
 ELSE 'DEVIATION_REJECTED' END AS decision_type,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(decided.payload,'$.comment')),'') AS decision_comment,
 COALESCE(JSON_UNQUOTE(JSON_EXTRACT(req.payload,'$.severity')),'') AS severity,
 COALESCE(`+decision+`,'') AS decision`, domain.ApprovalDeviation)
}

// deviationDetailQuery 按真实偏离编号读取结果，旧通知不能作用于后续实例。
// 参数：db/filter 为授权范围，id 为偏离编号。
// 返回值：有实际处置结论的历史记录或当前仍可办理的偏离。
func deviationDetailQuery(db *gorm.DB, filter platform.ScopeFilter, id string) *gorm.DB {
	return approvalProjectItems(db, filter).
		Joins(`JOIN pm_delivery_event AS req ON req.tenant_id=pm_service_item.tenant_id
 AND req.service_item_id=pm_service_item.id AND req.event_type='DEVIATION_REPORTED'`).
		Joins("LEFT JOIN pm_delivery_event AS decided ON decided.id="+matchingDeviationDecision).
		Where(deviationCoordinate+" = ?", id).
		Where("(decided.id IS NOT NULL OR (req.id="+latestDeviationReport+
			" AND pm_service_item.status = ? AND "+derivedProjectStatusPredicate(domain.ProjectStatusException)+"))",
			domain.ProjectStatusException, domain.ProjectStatusException)
}

// nativeApprovalVisible 决定是否按现有岗位和权限读取原生范围待办。
// 参数：q 为当前读取视图和经应用层核验的原生范围能力。
// 返回值：可读取审批队列或本人提案时返回 true。
func nativeApprovalVisible(q domain.ApprovalQuery) bool {
	return q.NativeAvailable && (q.CanNative || q.View == "submitted" || q.RequestID != "")
}

// nativeScopeApprovalQuery 读取授权原生项目的待办或按编号查询原提案结果。
// 参数：db/filter 为授权读取范围，q 携带可信身份及可选提案编号。
// 返回值：沿用原提案身份口径且审批队列排除本人提案的查询。
func nativeScopeApprovalQuery(db *gorm.DB, filter platform.ScopeFilter, q domain.ApprovalQuery) *gorm.DB {
	query := applyProjectScope(db.Table("pm_project"), filter, "pm_project").
		Joins(`JOIN pm_native_scope_change AS req ON req.tenant_id=pm_project.tenant_id
 AND req.project_id=pm_project.id`).
		Where("pm_project.source_kind = ? AND pm_project.scope_version > 0", "NATIVE")
	if q.RequestID != "" {
		query = query.Where("req.id = ? AND req.status IN ?", q.RequestID,
			[]string{"PENDING", "APPROVED", "REJECTED", "WITHDRAWN"})
	} else {
		query = query.Where("req.status = ?", "PENDING")
	}
	if q.View == "submitted" || !q.CanNative {
		query = query.Where("req.requested_by = ?", q.IdentityID)
	} else if q.RequestID == "" {
		query = query.Where("req.requested_by <> ?", q.IdentityID)
	}
	return query.Select(`req.id, req.project_id, '' AS service_item_id, '' AS site, req.reason,
 req.requested_by AS requester_user_id, '' AS approver_user_id, req.created_at, req.version,
 '' AS report_status, '' AS service_item_status, ? AS type,
 CASE WHEN req.status='PENDING' THEN '' ELSE CONCAT('NATIVE_SCOPE_', req.status) END AS decision_type,
 req.comment AS decision_comment, '' AS severity,
 CASE WHEN req.status='PENDING' THEN '' ELSE req.status END AS decision`,
		domain.ApprovalNativeScope)
}
