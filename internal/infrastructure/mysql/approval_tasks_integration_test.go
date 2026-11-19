package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

// TestApprovalTasksRespectPermissionsAndHistory 验证隔离数据库待办的权限、租户和终态详情。
// 参数：t 为测试上下文，只使用显式 PM_TEST_DSN 下新建的合成租户。
// 返回值：无；越权可见、待办未关闭或历史详情丢失时失败。
func TestApprovalTasksRespectPermissionsAndHistory(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).
		Updates(map[string]any{"team_lead_id": "lead", "project_manager_id": "manager"}).Error; err != nil {
		t.Fatal(err)
	}
	request := domain.DeliveryEvent{ID: "approval-request", TenantID: f.tenant, ProjectID: f.projectID,
		ServiceItemID: f.itemID, ActorUserID: "manager", Type: application.EventRollbackRequested,
		CreatedAt: time.Now().UTC(), Payload: map[string]any{"kind": "REPORT_TO_FIELD", "reason": "历史返工"}}
	if err := createEvent(f.db, request); err != nil {
		t.Fatal(err)
	}
	activity := []deliveryEventRecord{}
	for range 501 {
		activity = append(activity, deliveryEventRecord{ID: ulid.Make().String(), TenantID: f.tenant,
			ProjectID: f.projectID, ServiceItemID: f.itemID, EventType: "TEST_ACTIVITY", ActorUserID: "SYSTEM",
			Payload: []byte(`{}`), CreatedAt: request.CreatedAt.Add(time.Second)})
	}
	if err := f.db.CreateInBatches(activity, 100).Error; err != nil {
		t.Fatal(err)
	}
	filter := platform.ScopeFilter{TenantID: f.tenant, AllowAll: true}
	query := domain.ApprovalQuery{UserID: "director", CanRollback: true, View: "assigned", Page: 1, PageSize: 20}
	rows, total, err := f.repo.ListApprovalTasks(t.Context(), filter, query)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ApproverUserID != "" {
		t.Fatalf("rows=%+v total=%d err=%v", rows, total, err)
	}
	if original, err := f.repo.GetApprovalRequest(t.Context(), filter, f.itemID, "approval-request",
		application.EventRollbackRequested); err != nil || original.ID != "approval-request" {
		t.Fatalf("old request lookup=%+v err=%v", original, err)
	}
	query.UserID, query.CanRollback = "other-lead", false
	if _, total, err := f.repo.ListApprovalTasks(t.Context(), filter, query); err != nil || total != 0 {
		t.Fatalf("unrelated lead sees total=%d err=%v", total, err)
	}
	query.UserID, query.View = "manager", "submitted"
	if _, total, err := f.repo.ListApprovalTasks(t.Context(), filter, query); err != nil || total != 1 {
		t.Fatalf("requester total=%d err=%v", total, err)
	}
	query.UserID, query.View, query.CanRollback = "director", "assigned", true
	filter.TenantID = "other-tenant"
	if _, total, err := f.repo.ListApprovalTasks(t.Context(), filter, query); err != nil || total != 0 {
		t.Fatalf("cross tenant total=%d err=%v", total, err)
	}
	filter.TenantID = f.tenant
	request.ID, request.Type, request.ActorUserID = "approval-decision", application.EventRollbackRejected, "lead"
	request.Payload = map[string]any{"request_id": "approval-request", "comment": "补充原因后重提"}
	if err := createEvent(f.db, request); err != nil {
		t.Fatal(err)
	}
	if _, total, err := f.repo.ListApprovalTasks(t.Context(), filter, query); err != nil || total != 0 {
		t.Fatalf("closed request remains pending total=%d err=%v", total, err)
	}
	query.RequestID = "approval-request"
	rows, total, err = f.repo.ListApprovalTasks(t.Context(), filter, query)
	if err != nil || total != 1 || rows[0].DecisionType != application.EventRollbackRejected || rows[0].DecisionComment != "补充原因后重提" {
		t.Fatalf("history rows=%+v total=%d err=%v", rows, total, err)
	}
}

// approvalFixturePrincipal 构造仅用于隔离租户的可信审批测试主体。
// 参数：f 为合成租户，user 为角色身份，permissions 为显式业务权限。
// 返回值：具有本租户应用范围的测试主体，不连接实际平台用户目录。
func approvalFixturePrincipal(f rectifyingFixture, user string, permissions ...string) platform.Principal {
	grants := map[string]bool{"project.read": true}
	for _, permission := range permissions {
		grants[permission] = true
	}
	return platform.Principal{TenantID: f.tenant, IdentityID: user, UserID: user,
		Permissions: grants, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
}

// TestNotificationAudienceSnapshotRequiresOwnedLease 验证角色受众冻结不能越过租户或租约。
// 参数：t 为隔离 MySQL 测试上下文。
// 返回值：无；其他租户、其他Worker或过期租约能改写受众时失败。
func TestNotificationAudienceSnapshotRequiresOwnedLease(t *testing.T) {
	f := newCorrectionBindingFixture(t)
	service := application.Service{Repo: f.repo}
	manager := approvalFixturePrincipal(f, "manager", "project.report.correction.request")
	if _, err := service.RequestReportCorrection(t.Context(), manager, f.itemID,
		domain.ReportCorrectionRequestInput{Reason: "测试受众快照", ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	items, err := f.repo.ClaimNotificationOutbox(t.Context(), f.tenant, "worker", 1, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("claimed=%v err=%v", items, err)
	}
	message := items[0].Message
	message.RecipientRole, message.Recipients = "", []string{"director"}
	for _, test := range []struct {
		tenant, worker string
		clock          time.Time
	}{
		{"other-tenant", "worker", now},
		{f.tenant, "other-worker", now},
		{f.tenant, "worker", now.Add(3 * time.Minute)},
	} {
		if err := f.repo.SaveNotificationAudience(t.Context(), test.tenant, items[0].ID,
			test.worker, test.clock, message); !errors.Is(err, application.ErrConflict) {
			t.Fatalf("unowned audience write=%v", err)
		}
	}
	if err := f.repo.SaveNotificationAudience(t.Context(), f.tenant, items[0].ID, "worker", now, message); err != nil {
		t.Fatal(err)
	}
	var row notificationOutboxRecord
	if err := f.db.Where("tenant_id=? AND id=?", f.tenant, items[0].ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	var saved domain.NotificationMessage
	if err := json.Unmarshal(row.Payload, &saved); err != nil || saved.RecipientRole != "" ||
		len(saved.Recipients) != 1 || saved.Recipients[0] != "director" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}

// TestReportApprovalsPersistTasksAndNotifications 验证两种报告申请到待办、审批及提醒的真实事务链。
// 参数：t 为测试上下文，只使用新建隔离 MySQL 中的合成报告。
// 返回值：无；自批、重复审批、状态或通知持久化不正确时失败。
func TestReportApprovalsPersistTasksAndNotifications(t *testing.T) {
	for _, correction := range []bool{false, true} {
		t.Run(map[bool]string{false: "rework", true: "correction"}[correction], func(t *testing.T) {
			f := newCorrectionBindingFixture(t)
			phase := "COMPILING"
			if correction {
				phase = "ISSUED"
			}
			if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).
				Update("report_status", phase).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&reportRevisionRecord{}).Where("tenant_id=?", f.tenant).
				Update("status", phase).Error; err != nil {
				t.Fatal(err)
			}
			service := application.Service{Repo: f.repo}
			manager := approvalFixturePrincipal(f, "manager", "project.rollback.request", "project.report.correction.request")
			director := approvalFixturePrincipal(f, "director", "project.rollback.approve", "project.report.correction.approve")
			var requestID string
			var err error
			if correction {
				requestID, err = service.RequestReportCorrection(t.Context(), manager, f.itemID,
					domain.ReportCorrectionRequestInput{Reason: "隔离更正", ExpectedVersion: 1})
			} else {
				requestID, err = service.RequestRollback(t.Context(), manager, f.itemID,
					domain.RollbackRequestInput{Kind: "REPORT_TO_FIELD", Reason: "隔离返工", ExpectedVersion: 1})
			}
			if err != nil {
				t.Fatal(err)
			}
			page, err := service.ListApprovalTasks(t.Context(), director,
				domain.ApprovalQuery{Page: 1, PageSize: 20, View: "assigned"})
			if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].CanDecide {
				t.Fatalf("page=%+v err=%v", page, err)
			}
			legacy, err := f.repo.GetApprovalRequest(t.Context(), platform.ScopeFilter{TenantID: f.tenant, AllowAll: true},
				f.itemID, requestID, map[bool]string{false: application.EventRollbackRequested, true: application.EventReportCorrectionRequested}[correction])
			if err != nil {
				t.Fatal(err)
			}
			legacy.ID, legacy.ActorUserID = ulid.Make().String(), "legacy-manager"
			legacy.Payload["requester_user_id"] = "legacy-manager"
			if err := createEvent(f.db, legacy); err != nil {
				t.Fatal(err)
			}
			decide := func(actor platform.Principal) error {
				if correction {
					return service.DecideReportCorrection(t.Context(), actor, f.itemID, requestID,
						domain.ReportCorrectionDecisionInput{Decision: "APPROVED", Comment: "同意更正", ExpectedVersion: page.Items[0].Version})
				}
				return service.DecideRollback(t.Context(), actor, f.itemID, requestID,
					domain.RollbackDecisionInput{Decision: "APPROVED", Comment: "同意返工", ExpectedVersion: page.Items[0].Version})
			}
			self := director
			self.UserID, self.IdentityID = "manager", "manager"
			if err := decide(self); !errors.Is(err, application.ErrForbidden) {
				t.Fatalf("self approval=%v", err)
			}
			if err := decide(director); err != nil {
				t.Fatal(err)
			}
			if err := decide(director); err == nil {
				t.Fatal("duplicate approval succeeded")
			}
			page, err = service.ListApprovalTasks(t.Context(), manager,
				domain.ApprovalQuery{Page: 1, PageSize: 20, View: "submitted", RequestID: requestID})
			if err != nil || len(page.Items) != 1 || page.Items[0].Status != "APPROVED" || page.Items[0].CanDecide {
				t.Fatalf("resolved page=%+v err=%v", page, err)
			}
			var notices []notificationOutboxRecord
			if err := f.db.Where("tenant_id=?", f.tenant).Find(&notices).Error; err != nil || len(notices) != 3 {
				t.Fatalf("notifications=%d err=%v", len(notices), err)
			}
			legacyNotice := false
			for _, row := range notices {
				var message domain.NotificationMessage
				if err := json.Unmarshal(row.Payload, &message); err != nil ||
					message.TargetURL != "/project_management/approvals?request_id="+message.ReferenceID {
					t.Fatalf("message=%+v err=%v", message, err)
				}
				if message.ReferenceID == legacy.ID && len(message.Recipients) == 1 && message.Recipients[0] == "legacy-manager" {
					legacyNotice = true
				}
			}
			if !legacyNotice {
				t.Fatal("legacy automatically closed request has no result notification")
			}
		})
	}
}

// TestApprovalOutcomeOnlyNotifiesApplicant 验证真实审批终决只投递结果通知。
// 参数：t 为测试上下文，仅使用明确隔离的 MySQL 合成数据。
// 返回值：无；待办未关闭、申请人产生待办或结果通知不正确时失败。
func TestApprovalOutcomeOnlyNotifiesApplicant(t *testing.T) {
	for _, decision := range []string{"APPROVED", "REJECTED"} {
		for _, kind := range []string{"FIELD_TO_PREPARATION", "REPORT_TO_FIELD", "CORRECTION"} {
			t.Run(kind+"/"+decision, func(t *testing.T) {
				verifyApprovalOutcome(t, kind, decision)
			})
		}
	}
}

// verifyApprovalOutcome 按审批类型和决定验证任务、可读历史与事务通知。
// 参数：t 为测试上下文，kind 为申请类型，decision 为终审结果。
// 返回值：无；任何业务断言不符时终止用例。
func verifyApprovalOutcome(t *testing.T, kind, decision string) {
	f := newCorrectionBindingFixture(t)
	if kind != "CORRECTION" {
		state := map[string]any{"report_status": "COMPILING"}
		if kind == "FIELD_TO_PREPARATION" {
			state["status"] = "实施中"
		}
		if err := f.db.Model(&serviceItemRecord{}).Where("id=?", f.itemID).Updates(state).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := application.Service{Repo: f.repo}
	manager := approvalFixturePrincipal(f, "manager", "project.rollback.request", "project.report.correction.request")
	director := approvalFixturePrincipal(f, "director", "project.rollback.approve", "project.report.correction.approve")
	requestID, err := requestApprovalOutcomeFixture(t.Context(), service, manager, f.itemID, kind)
	if err != nil {
		t.Fatal(err)
	}
	query := domain.ApprovalQuery{Page: 1, PageSize: 20, View: "assigned"}
	before, err := service.ListApprovalTasks(t.Context(), director, query)
	if err != nil || before.Total != 1 || !before.Items[0].CanDecide {
		t.Fatalf("审批人待办=%+v，错误=%v", before, err)
	}
	comment := "隔离验收审批意见"
	if err := decideApprovalOutcomeFixture(t.Context(), service, director, f.itemID, requestID,
		kind, decision, comment, before.Items[0].Version); err != nil {
		t.Fatal(err)
	}
	for _, principal := range []platform.Principal{manager, director} {
		page, err := service.ListApprovalTasks(t.Context(), principal, query)
		if err != nil || page.Total != 0 {
			t.Fatalf("%s待办=%+v，错误=%v", principal.UserID, page, err)
		}
	}
	query.View, query.RequestID = "submitted", requestID
	history, err := service.ListApprovalTasks(t.Context(), manager, query)
	if err != nil || len(history.Items) != 1 || history.Items[0].Status != decision ||
		history.Items[0].CanDecide || history.Items[0].DecisionComment != comment {
		t.Fatalf("申请记录=%+v，错误=%v", history, err)
	}
	verifyApprovalOutcomeOutbox(t, f, requestID, kind, decision)
}

// requestApprovalOutcomeFixture 通过正式应用服务新建合成审批。
// 参数：ctx 为测试请求，service 为真实服务，actor 为申请人，itemID 和 kind 指定对象与类型。
// 返回值：审批申请编号和业务错误。
func requestApprovalOutcomeFixture(ctx context.Context, service application.Service, actor platform.Principal,
	itemID, kind string) (string, error) {
	if kind == "CORRECTION" {
		return service.RequestReportCorrection(ctx, actor, itemID,
			domain.ReportCorrectionRequestInput{Reason: "隔离更正", ExpectedVersion: 1})
	}
	return service.RequestRollback(ctx, actor, itemID,
		domain.RollbackRequestInput{Kind: kind, Reason: "隔离回退", ExpectedVersion: 1})
}

// decideApprovalOutcomeFixture 通过正式应用服务提交合成审批结果。
// 参数：ctx 为测试请求，service 为真实服务，actor 为审批人，itemID 和 requestID 标识申请，
// kind 为类型，decision 和 comment 为决定及意见，version 为待办的版本。
// 返回值：实际审批错误，成功时为 nil。
func decideApprovalOutcomeFixture(ctx context.Context, service application.Service, actor platform.Principal,
	itemID, requestID, kind, decision, comment string, version uint64) error {
	if kind == "CORRECTION" {
		return service.DecideReportCorrection(ctx, actor, itemID, requestID,
			domain.ReportCorrectionDecisionInput{Decision: decision, Comment: comment, ExpectedVersion: version})
	}
	return service.DecideRollback(ctx, actor, itemID, requestID,
		domain.RollbackDecisionInput{Decision: decision, Comment: comment, ExpectedVersion: version})
}

// verifyApprovalOutcomeOutbox 核对同一申请只有一条投递给原申请人的终决通知。
// 参数：t 为测试上下文，f 为隔离租户，requestID、kind、decision 为待核验的审批事实。
// 返回值：无；接收人、审批结果、链接或条数不符时失败。
func verifyApprovalOutcomeOutbox(t *testing.T, f rectifyingFixture, requestID, kind, decision string) {
	var rows []notificationOutboxRecord
	if err := f.db.Where("tenant_id=?", f.tenant).Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("申请与终决通知共 %d 条，错误=%v", len(rows), err)
	}
	subject := map[string]string{"FIELD_TO_PREPARATION": "现场回退申请", "REPORT_TO_FIELD": "报告返工申请",
		"CORRECTION": "报告更正申请"}[kind]
	outcome := map[string]string{"APPROVED": "已通过", "REJECTED": "已驳回"}[decision]
	for _, row := range rows {
		var message domain.NotificationMessage
		if err := json.Unmarshal(row.Payload, &message); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(message.EventType, "_"+decision) {
			continue
		}
		if message.Title != subject+outcome || message.RecipientRole != "" ||
			len(message.Recipients) != 1 || message.Recipients[0] != "manager" ||
			message.TargetURL != "/project_management/approvals?request_id="+requestID {
			t.Fatalf("终决通知=%+v", message)
		}
		return
	}
	t.Fatal("缺少预期终决通知")
}
