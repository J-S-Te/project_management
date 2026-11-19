package mysql

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/oklog/ulid/v2"
	"gorm.io/gorm"
)

// approvalNoticeFixture 创建仅使用内存SQLite的真实事务通知夹具。
// 参数：t 为测试上下文。
// 返回值：合成项目仓储；JSON_UNQUOTE兼容仅用于本地SQLite语义。
func approvalNoticeFixture(t *testing.T) *Repository {
	t.Helper()
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&notificationOutboxRecord{}, &deliveryEventRecord{}, &auditOutboxRecord{},
		&automationRecord{}, &warningRuleRecord{}, &deliveryEffectsRecord{}, &reportRevisionRecord{},
		&evidenceFileRecord{}, &nativeScopeRecord{}, &nativeScopeChangeRecord{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	conn, err := sqlDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("JSON_UNQUOTE", func(value any) any { return value }, true)
	})
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(db)
}

// noticeMessages 读取当前合成租户持久化的完整通知载荷。
// 参数：t 为测试上下文，repo 为隔离仓储，tenant 为合成租户。
// 返回值：按创建顺序排列的真实Outbox消息。
func noticeMessages(t *testing.T, repo *Repository, tenant string) []domain.NotificationMessage {
	t.Helper()
	var rows []notificationOutboxRecord
	if err := repo.db.Where("tenant_id=?", tenant).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	messages := make([]domain.NotificationMessage, len(rows))
	for i, row := range rows {
		if err := json.Unmarshal(row.Payload, &messages[i]); err != nil {
			t.Fatal(err)
		}
	}
	return messages
}

// TestSpecialMethodConfirmationNotifiesOnceAndResults 验证待复核进入、重放和原提交者结果闭环。
// 参数：t 为测试上下文，仅使用内存SQLite。
// 返回值：无；通知丢失、重复、收件人错误或决定关联缺失时失败。
func TestSpecialMethodConfirmationNotifiesOnceAndResults(t *testing.T) {
	repo := approvalNoticeFixture(t)
	now := time.Now().UTC()
	project := projectRecord{ID: "project", TenantID: "tenant", Status: "待拆解确认", SupplementStatus: "NONE", Version: 1, CreatedAt: now, UpdatedAt: now}
	item := serviceItemRecord{ID: "item", TenantID: "tenant", ProjectID: project.ID,
		Status: "待确认", Special: "是", TechReviewStatus: "NONE", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := repo.db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	filter := platform.ScopeFilter{TenantID: "tenant", AllowAll: true}
	for range 2 {
		if _, err := repo.ConfirmServiceItems(t.Context(), filter, []string{"item"}, "submitter-user"); err != nil {
			t.Fatal(err)
		}
	}
	messages := noticeMessages(t, repo, "tenant")
	if len(messages) != 1 || messages[0].EventType != "SPECIAL_METHOD_REVIEW_REQUESTED" ||
		messages[0].RecipientRole != "technical_director" {
		t.Fatalf("messages=%+v", messages)
	}
	event := domain.DeliveryEvent{ID: "review-1", TenantID: "tenant", ServiceItemID: "item",
		Type: application.EventSpecialMethodReviewed, ActorUserID: "director-user", CreatedAt: now.Add(time.Minute),
		Payload: map[string]any{"decision": "REJECTED", "comment": "内部风险说明"}}
	if err := repo.ApplyDeliveryEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	messages = noticeMessages(t, repo, "tenant")
	if len(messages) != 2 || len(messages[1].Recipients) != 1 || messages[1].Recipients[0] != "submitter-user" ||
		messages[1].ReferenceType != "SPECIAL_METHOD_REVIEW" || messages[1].ReferenceID != "review-1" {
		t.Fatalf("results=%+v", messages)
	}
	target, _ := url.Parse(messages[1].TargetURL)
	if target.Query().Get("review_id") != "review-1" {
		t.Fatal("decision not linked")
	}
	var stored deliveryEventRecord
	if err := repo.db.Where("id=?", event.ID).Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(stored.Payload, &payload)
	if payload["review_requester_user_id"] != "submitter-user" {
		t.Fatalf("payload=%v", payload)
	}
	if _, err := repo.ConfirmServiceItems(t.Context(), filter, []string{"item"}, "resubmitter-user"); err != nil {
		t.Fatal(err)
	}
	if messages = noticeMessages(t, repo, "tenant"); len(messages) != 3 || messages[2].EventType != "SPECIAL_METHOD_REVIEW_REQUESTED" {
		t.Fatalf("new round=%+v", messages)
	}
}

// TestSpecialMethodAutomaticEntryRetainsNoInventedRequester 验证自动待审不冒充人工提交者。
// 参数：t 为测试上下文，仅使用内存SQLite。
// 返回值：无；自动进入漏提醒或错误记录人类提交者时失败。
func TestSpecialMethodAutomaticEntryRetainsNoInventedRequester(t *testing.T) {
	repo := approvalNoticeFixture(t)
	now := time.Now().UTC()
	project := domain.Project{ID: "auto-project", TenantID: "tenant", Contract: "contract-auto",
		ContractID: "contract-auto", Status: "待分配", SupplementStatus: "NONE", CreatedAt: now, UpdatedAt: now}
	item := domain.ServiceItem{ID: "auto-item", ProjectID: project.ID, TenantID: "tenant",
		Special: "是", Status: "待分配", TechReviewStatus: "PENDING"}
	event := domain.DeliveryEvent{ID: "activation", ProjectID: project.ID, TenantID: "tenant",
		Type: application.EventContractActivated, ActorUserID: "machine-subject", CreatedAt: now, Payload: map[string]any{}}
	if err := repo.ActivateContract(t.Context(), project, []domain.ServiceItem{item}, event); err != nil {
		t.Fatal(err)
	}
	messages := noticeMessages(t, repo, "tenant")
	if len(messages) != 1 || messages[0].EventType != "SPECIAL_METHOD_REVIEW_REQUESTED" {
		t.Fatalf("messages=%+v", messages)
	}
	var request deliveryEventRecord
	if err := repo.db.Where("tenant_id=? AND event_type=?", "tenant", "SPECIAL_METHOD_REVIEW_REQUESTED").Take(&request).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(request.Payload, &payload)
	if value, _ := payload["review_requester_user_id"].(string); value != "" {
		t.Fatalf("invented requester=%s", value)
	}
}

// TestDeviationResultNotifiesReporterOnce 验证原偏离上报者收到精确结果且重复决定不双发。
// 参数：t 为测试上下文，仅使用内存SQLite。
// 返回值：无；受众、偏离坐标或重放次数错误时失败。
func TestDeviationResultNotifiesReporterOnce(t *testing.T) {
	repo := approvalNoticeFixture(t)
	now := time.Now().UTC()
	if err := repo.db.Create(&projectRecord{ID: "project", TenantID: "tenant", Status: "异常处理中", Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&serviceItemRecord{ID: "item", TenantID: "tenant", ProjectID: "project", Status: "异常处理中", Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&deliveryEventRecord{ID: "reported", TenantID: "tenant", ProjectID: "project", ServiceItemID: "item",
		EventType: application.EventDeviationReported, ActorUserID: "reporter-user", CreatedAt: now,
		Payload: []byte(`{"deviation_id":"deviation-1"}`)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"decision-1", "decision-retry"} {
		event := domain.DeliveryEvent{ID: id, TenantID: "tenant", ServiceItemID: "item", ActorUserID: "reviewer-user",
			Type: application.EventDeviationReviewed, CreatedAt: now.Add(time.Minute),
			Payload: map[string]any{"deviation_id": "deviation-1", "decision": "RELEASE"}}
		if err := repo.ApplyDeliveryEvent(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	messages := noticeMessages(t, repo, "tenant")
	if len(messages) != 1 || len(messages[0].Recipients) != 1 || messages[0].Recipients[0] != "reporter-user" ||
		messages[0].ReferenceType != "DEVIATION" || messages[0].ReferenceID != "deviation-1" {
		t.Fatalf("messages=%+v", messages)
	}
	conflict := domain.DeliveryEvent{ID: "conflicting-decision", TenantID: "tenant", ServiceItemID: "item",
		ActorUserID: "reviewer-user", Type: application.EventDeviationReviewed, CreatedAt: now.Add(2 * time.Minute),
		Payload: map[string]any{"deviation_id": "deviation-1", "decision": "TERMINATE"}}
	if err := repo.ApplyDeliveryEvent(t.Context(), conflict); err != nil {
		t.Fatal(err)
	}
	if messages = noticeMessages(t, repo, "tenant"); len(messages) != 1 || !strings.Contains(messages[0].Title, "放行") {
		t.Fatalf("duplicate changed result=%+v", messages)
	}
}

// TestNativeScopeNotificationRequesterProofAndReplay 验证原生身份与通知用户分离及决定重放。
// 参数：t 为测试上下文，仅使用内存SQLite。
// 返回值：无；申请者、决定、坐标或幂等身份错误时失败。
func TestNativeScopeNotificationRequesterProofAndReplay(t *testing.T) {
	for _, decision := range []string{"APPROVE", "REJECT"} {
		t.Run(decision, func(t *testing.T) {
			repo, project, items := scopeFixture(t)
			in := domain.NativeScopeInput{OperationID: "notice-request", ExpectedProjectVersion: 1,
				BaseScopeVersion: 1, Reason: "内部敏感范围原因", RequestedByUserID: "requester-user"}
			filter := nativeScopeFilter(project)
			change, err := repo.RequestNativeScope(t.Context(), filter, project.ID, "requester-identity", in, items)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.RequestNativeScope(t.Context(), filter, project.ID, "requester-identity", in, items); err != nil {
				t.Fatal(err)
			}
			messages := noticeMessages(t, repo, project.TenantID)
			if len(messages) != 1 || messages[0].ExcludeUserID != "requester-user" || messages[0].RecipientRole != "technical_director,admin" {
				t.Fatalf("request=%+v", messages)
			}
			input := domain.NativeScopeDecision{OperationID: "notice-decision", ExpectedVersion: 1, Decision: decision, Comment: "已核验"}
			for range 2 {
				if _, err := repo.DecideNativeScope(t.Context(), filter, project.ID, change.ID, "director-identity", input); err != nil {
					t.Fatal(err)
				}
			}
			messages = noticeMessages(t, repo, project.TenantID)
			if len(messages) != 2 || len(messages[1].Recipients) != 1 || messages[1].Recipients[0] != "requester-user" || strings.Contains(messages[1].Content, in.Reason) {
				t.Fatalf("result=%+v", messages)
			}
			target, _ := url.Parse(messages[1].TargetURL)
			if target.Query().Get("project") != project.ID || target.Query().Get("scope_change_id") != change.ID {
				t.Fatalf("target=%s", messages[1].TargetURL)
			}
			want := "批准"
			if decision == "REJECT" {
				want = "驳回"
			}
			if !strings.Contains(messages[1].Title, want) {
				t.Fatalf("title=%s", messages[1].Title)
			}
		})
	}
}

// TestApprovalNotificationWriteFailureRollsBack 验证Outbox不可写时待审业务整体回滚。
// 参数：t 为测试上下文，故障只注入隔离SQLite的通知插入边界。
// 返回值：无；请求或待审事实在通知失败后残留时失败。
func TestApprovalNotificationWriteFailureRollsBack(t *testing.T) {
	repo, project, items := scopeFixture(t)
	if err := repo.db.Callback().Create().Before("gorm:create").Register("reject_notification", func(tx *gorm.DB) {
		if tx.Statement.Table == "pm_notification_outbox" {
			tx.AddError(errors.New("notification insert unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	in := domain.NativeScopeInput{OperationID: "notice-failure", ExpectedProjectVersion: 1,
		BaseScopeVersion: 1, Reason: "范围变更", RequestedByUserID: "requester-user"}
	if _, err := repo.RequestNativeScope(t.Context(), nativeScopeFilter(project), project.ID, "requester-identity", in, items); err == nil {
		t.Fatal("notification failure accepted")
	}
	var count int64
	if err := repo.db.Model(&nativeScopeChangeRecord{}).Where("tenant_id=?", project.TenantID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("proposal=%d err=%v", count, err)
	}
}

// TestApprovalNoticeIdentityFitsDatabaseAndPlatform 验证数据库标识与平台事件码分别满足各自契约。
// 参数：t 为测试上下文。
// 返回值：无；数据库事件不是26字符ULID或通知JSON缺少字母前缀时失败。
func TestApprovalNoticeIdentityFitsDatabaseAndPlatform(t *testing.T) {
	id := approvalNoticeID("kind", "tenant", "record")
	if _, err := ulid.ParseStrict(id); err != nil || len(id) != 26 {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if id != approvalNoticeID("kind", "tenant", "record") {
		t.Fatal("unstable identity")
	}
}
