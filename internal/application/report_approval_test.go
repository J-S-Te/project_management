package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestReportReworkCreatesDirectorReminder 验证返工申请附带技术总监直达提醒。
// 参数：t 为测试上下文。
// 返回值：无；审批归属、收件人或申请直达地址缺失时失败。
func TestReportReworkCreatesDirectorReminder(t *testing.T) {
	repo := &reportCorrectionRepository{item: domain.ServiceItem{
		ID: "item", ProjectID: "project", TenantID: "tenant-1", TeamLeadID: "lead",
		ProjectManagerID: "manager", Status: "现场实施完成", ReportStatus: "COMPILING", Version: 7,
	}}
	service := Service{Repo: repo}
	id, err := service.RequestRollback(t.Context(), reportCorrectionPrincipal("manager", "project.rollback.request"),
		"item", domain.RollbackRequestInput{Kind: "REPORT_TO_FIELD", Reason: "报告需重新检测", ExpectedVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	event := repo.events[0]
	if event.Notification == nil || event.Notification.RecipientRole != "technical_director" ||
		event.Notification.ExcludeUserID != "manager" || len(event.Notification.Recipients) != 0 {
		t.Fatalf("notification=%+v, want independent technical director", event.Notification)
	}
	raw, _ := json.Marshal(event.Notification)
	var message map[string]any
	_ = json.Unmarshal(raw, &message)
	if message["target_url"] != "/project_management/approvals?request_id="+id {
		t.Fatalf("target=%v", message["target_url"])
	}
}

type approvalAudienceOutbox struct {
	boundaryOutboxRepo
	snapshot domain.NotificationMessage
}

// SaveNotificationAudience 保存投递前冻结的合成收件人快照。
// 参数：ctx 为请求，tenant/id/owner/now 为租约身份，message 为冻结后的通知。
// 返回值：保存快照并更新测试队列；无外部副作用。
func (r *approvalAudienceOutbox) SaveNotificationAudience(ctx context.Context, tenant string, id uint64, owner string, now time.Time, message domain.NotificationMessage) error {
	r.snapshot = message
	r.item.Message = message
	return nil
}

// TestApprovalNotificationBatchesFreezeAudience 验证超过平台五百人上限时稳定拆批和部分失败重试。
// 参数：t 为测试上下文，平台摄取仅用可失败的边界函数替身。
// 返回值：无；批次超限、重试重新解析目录或幂等身份变化时失败。
func TestApprovalNotificationBatchesFreezeAudience(t *testing.T) {
	users := []string{}
	for i := 0; i <= 500; i++ {
		users = append(users, fmt.Sprintf("director-%03d", i))
	}
	repo := &approvalAudienceOutbox{boundaryOutboxRepo: boundaryOutboxRepo{
		item: &domain.NotificationOutboxItem{ID: 1, Message: domain.NotificationMessage{
			EventID: "event", EventType: EventReportCorrectionRequested, RecipientRole: "technical_director",
			IdempotencyKey: "approval-event", TargetURL: "/project_management/approvals?request_id=request",
		}},
	}}
	publications := []platform.NotificationEvent{}
	publisher := boundaryPublisher(func(ctx context.Context, event platform.NotificationEvent) error {
		publications = append(publications, event)
		if len(publications) == 2 {
			return errors.New("second batch unavailable")
		}
		return nil
	})
	service := Service{Repo: repo, Notifications: publisher,
		Personnel: pagedApprovalDirectory{userIDs: users}}
	sent, err := service.DispatchNotificationOutbox(t.Context(), "tenant", "worker", 1, time.Now().UTC())
	if err != nil || sent != 0 || repo.retried != 1 || len(publications) != 2 {
		t.Fatalf("sent=%d retry=%d publications=%d err=%v", sent, repo.retried, len(publications), err)
	}
	if repo.snapshot.RecipientRole != "" || len(repo.snapshot.Recipients) != 501 {
		t.Fatalf("unfrozen audience=%+v", repo.snapshot)
	}
	repo.item = &domain.NotificationOutboxItem{ID: 1, RetryCount: 1, Message: repo.snapshot}
	service.Personnel = nil
	sent, err = service.DispatchNotificationOutbox(t.Context(), "tenant", "worker", 1, time.Now().UTC())
	if err != nil || sent != 1 || len(publications) != 4 {
		t.Fatalf("retry sent=%d publications=%d err=%v", sent, len(publications), err)
	}
	if len(publications[0].Recipients) != 500 || len(publications[1].Recipients) != 1 ||
		publications[0].IdempotencyKey != publications[2].IdempotencyKey ||
		publications[1].IdempotencyKey != publications[3].IdempotencyKey ||
		publications[0].IdempotencyKey == publications[1].IdempotencyKey {
		t.Fatalf("unstable or oversized batches: %+v", publications)
	}
}

type pagedApprovalDirectory struct{ userIDs []string }

// List 模拟真实平台负责人目录每页最多五十人的分页契约。
// 参数：ctx 为请求，query 为请求页码及页大小。
// 返回值：含一位申请人与五十位独立技术总监的完整分页结构。
func (directory pagedApprovalDirectory) List(ctx context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	pageSize := min(query.PageSize, 50)
	all := []platform.OwnerDirectoryUser{{UserID: "manager"}}
	for i := 1; i <= 50; i++ {
		all = append(all, platform.OwnerDirectoryUser{UserID: fmt.Sprintf("director-%02d", i)})
	}
	if directory.userIDs != nil {
		all = []platform.OwnerDirectoryUser{}
		for _, user := range directory.userIDs {
			all = append(all, platform.OwnerDirectoryUser{UserID: user})
		}
	}
	start := min((query.Page-1)*pageSize, len(all))
	end := min(start+pageSize, len(all))
	return platform.OwnerDirectoryPage{Items: all[start:end], Total: int64(len(all)),
		Page: query.Page, PageSize: pageSize}, nil
}

// TestApprovalRecipientsFollowDirectoryPagination 验证审批提醒不会漏掉真实目录的第二页。
// 参数：t 为测试上下文，目录边界遵循平台五十条上限。
// 返回值：无；最后一位独立技术总监被跳过或申请人被误通知时失败。
func TestApprovalRecipientsFollowDirectoryPagination(t *testing.T) {
	service := Service{Personnel: pagedApprovalDirectory{}}
	users, err := service.resolveApprovalRecipients(t.Context(), domain.NotificationMessage{
		RecipientRole: "technical_director", ExcludeUserID: "manager",
	})
	if err != nil || len(users) != 50 || !slices.Contains(users, "director-50") || slices.Contains(users, "manager") {
		t.Fatalf("recipients=%v err=%v", users, err)
	}
}

// TestApprovalOutboxResolvesDirectorAndRetries 验证角色提醒投递和目录故障的持久重试。
// 参数：t 为测试上下文。
// 返回值：无；无审批人时被错误确认成功、误通知申请人或丢失直达目标时失败。
func TestApprovalOutboxResolvesDirectorAndRetries(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory unavailable", true: "resolved"}[available], func(t *testing.T) {
			repo := &approvalAudienceOutbox{boundaryOutboxRepo: boundaryOutboxRepo{item: &domain.NotificationOutboxItem{ID: 1,
				Message: domain.NotificationMessage{EventID: "event", EventType: EventReportCorrectionRequested,
					RecipientRole: "technical_director", ExcludeUserID: "manager",
					TargetURL: "/project_management/approvals?request_id=request"}}}}
			publisher := &notificationStub{}
			service := Service{Repo: repo, Notifications: publisher}
			if available {
				service.Personnel = roleDirectoryStub{byRole: map[string][]string{
					"technical_director": {"director", "manager", "director"},
				}}
			}
			sent, err := service.DispatchNotificationOutbox(t.Context(), "tenant", "worker", 1, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if !available {
				if sent != 0 || repo.retried != 1 || len(publisher.published) != 0 {
					t.Fatalf("sent=%d retry=%d publications=%v", sent, repo.retried, publisher.published)
				}
				return
			}
			if sent != 1 || len(publisher.published) != 1 || len(publisher.published[0].Recipients) != 1 ||
				publisher.published[0].Recipients[0] != "director" {
				t.Fatalf("sent=%d publications=%+v", sent, publisher.published)
			}
			raw, _ := json.Marshal(publisher.published[0])
			var message map[string]any
			_ = json.Unmarshal(raw, &message)
			if message["TargetURL"] != "/project_management/approvals?request_id=request" {
				t.Fatalf("target not forwarded: %s", raw)
			}
		})
	}
}

// TestReportReworkApprovalUsesDirectorPermission 验证返工仍需技术总监审批权限。
// 参数：t 为测试上下文。
// 返回值：无；错误审批人、自批或现场回退被错误放行时失败。
func TestReportReworkApprovalUsesDirectorPermission(t *testing.T) {
	for _, test := range []struct {
		name, actor, kind, permission string
		want                          error
	}{
		{"director", "director", "REPORT_TO_FIELD", "project.rollback.approve", nil},
		{"another director", "other-director", "REPORT_TO_FIELD", "project.rollback.approve", nil},
		{"team lead denied", "lead", "REPORT_TO_FIELD", "project.execution.assign", ErrForbidden},
		{"self approval", "manager", "REPORT_TO_FIELD", "project.rollback.approve", ErrForbidden},
		{"field rollback isolation", "lead", "FIELD_TO_PREPARATION", "project.execution.assign", ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &reportCorrectionRepository{item: domain.ServiceItem{ID: "item", ProjectID: "project",
				TeamLeadID: "lead", Status: "现场实施完成", ReportStatus: "COMPILING", Version: 7}}
			repo.events = []domain.DeliveryEvent{{ID: "request", ServiceItemID: "item", Type: EventRollbackRequested,
				ActorUserID: "manager", Payload: map[string]any{"kind": test.kind}}}
			service := Service{Repo: repo}
			err := service.DecideRollback(context.Background(), reportCorrectionPrincipal(test.actor, test.permission),
				"item", "request", domain.RollbackDecisionInput{Decision: "APPROVED", Comment: "同意返工", ExpectedVersion: 7})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

// TestReportCorrectionRequestCreatesRoleNotification 验证更正提醒持久化角色收件人与申请地址。
// 参数：t 为测试上下文。
// 返回值：无；提醒丢失、错误通知执行人员或缺失直达地址时失败。
func TestReportCorrectionRequestCreatesRoleNotification(t *testing.T) {
	repo := &reportCorrectionRepository{item: domain.ServiceItem{ID: "item", ProjectID: "project",
		Status: "现场实施完成", ReportStatus: "ARCHIVED", Version: 7}}
	service := Service{Repo: repo}
	id, err := service.RequestReportCorrection(t.Context(), reportCorrectionPrincipal("manager", "project.report.correction.request"),
		"item", domain.ReportCorrectionRequestInput{Reason: "更正客户名称", ExpectedVersion: 7})
	if err != nil {
		t.Fatal(err)
	}
	if repo.events[0].Notification == nil {
		t.Fatal("missing correction approval notification")
	}
	raw, _ := json.Marshal(repo.events[0].Notification)
	var message map[string]any
	_ = json.Unmarshal(raw, &message)
	if message["recipient_role"] != "technical_director" || message["exclude_user_id"] != "manager" ||
		message["target_url"] != "/project_management/approvals?request_id="+id {
		t.Fatalf("notification=%s", raw)
	}
}
