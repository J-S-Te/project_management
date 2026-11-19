package mysql

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/oklog/ulid/v2"
)

type approvalWorkFixture struct {
	rectifyingFixture
	method, deviation, native, nativeProject string
	deviationItem, specialResult             string
}

// newApprovalWorkFixture 在显式隔离的数据库中准备同租户的四种真实待办来源与一个历史结论。
// 参数：t 为测试上下文。
// 返回值：合成业务坐标及原项目仓储。
func newApprovalWorkFixture(t *testing.T) approvalWorkFixture {
	t.Helper()
	f := newCorrectionBindingFixture(t)
	w := approvalWorkFixture{rectifyingFixture: f, method: "SI-" + ulid.Make().String(),
		deviationItem: "SI-" + ulid.Make().String(), deviation: "DV-" + ulid.Make().String(),
		native: ulid.Make().String(), nativeProject: "PJ-" + ulid.Make().String(),
		specialResult: ulid.Make().String()}
	methodProject, deviationProject := "PJ-"+ulid.Make().String(), "PJ-"+ulid.Make().String()
	for _, row := range []any{
		&projectRecord{ID: methodProject, TenantID: f.tenant, Contract: "C-" + methodProject,
			ContractVersion: "v1", Name: "特殊方法", CreatedAt: f.now},
		&projectRecord{ID: deviationProject, TenantID: f.tenant, Contract: "C-" + deviationProject,
			ContractVersion: "v1", Name: "现场偏离", CreatedAt: f.now},
		&projectRecord{ID: w.nativeProject, TenantID: f.tenant, SourceKind: "NATIVE",
			SourceID: "native-origin", ScopeVersion: 1, Name: "原生项目", CreatedAt: f.now},
		&serviceItemRecord{ID: w.method, TenantID: f.tenant, ProjectID: methodProject, Site: "特殊现场",
			Status: "待分配", Special: "是", TechReviewStatus: "PENDING", CreatedAt: f.now},
		&serviceItemRecord{ID: w.deviationItem, TenantID: f.tenant, ProjectID: deviationProject,
			Status: "异常处理中", TeamLeadID: "lead-user", EngineerIDs: []byte(`["reporter"]`),
			CreatedAt: f.now},
		&nativeScopeChangeRecord{ID: w.native, TenantID: f.tenant, ProjectID: w.nativeProject,
			OperationID: "operation", PendingProject: &w.nativeProject, BaseScopeVersion: 1,
			Version: 1,
			Status:  "PENDING", RequestedBy: "submitter-identity", Reason: "原生范围更新",
			ServiceItems: []byte(`[]`), CreatedAt: f.now.Add(3 * time.Second)},
	} {
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := f.db.Where("tenant_id=?", f.tenant).Delete(&nativeScopeChangeRecord{}).Error; err != nil {
			t.Error(err)
		}
	})
	for _, row := range []domain.DeliveryEvent{
		{ID: ulid.Make().String(), ProjectID: methodProject, ServiceItemID: w.method,
			Type: "SPECIAL_METHOD_REVIEW_REQUESTED", ActorUserID: "submitter-user", CreatedAt: f.now,
			Payload: map[string]any{"review_requester_user_id": "submitter-user"}},
		{ID: "older-" + ulid.Make().String(), ProjectID: deviationProject, ServiceItemID: w.deviationItem,
			Type: application.EventDeviationReported, ActorUserID: "reporter", CreatedAt: f.now,
			Payload: map[string]any{"deviation_id": "DV-OLD"}},
		{ID: ulid.Make().String(), ProjectID: deviationProject, ServiceItemID: w.deviationItem,
			Type: application.EventDeviationReported, ActorUserID: "reporter", CreatedAt: f.now.Add(time.Second),
			Payload: map[string]any{"deviation_id": w.deviation, "description": "现场条件变更", "severity": "HIGH"}},
		{ID: ulid.Make().String(), ProjectID: f.projectID, ServiceItemID: f.itemID,
			Type: application.EventReportCorrectionRequested, ActorUserID: "manager",
			CreatedAt: f.now.Add(2 * time.Second), Payload: map[string]any{"reason": "报告更正"}},
		{ID: w.specialResult, ProjectID: methodProject, ServiceItemID: w.method,
			Type: application.EventSpecialMethodReviewed, ActorUserID: "director", CreatedAt: f.now.Add(-time.Second),
			Payload: map[string]any{"decision": "REJECTED", "comment": "原轮次结论",
				"review_requester_user_id": "submitter-user"}},
	} {
		row.TenantID = f.tenant
		if err := createEvent(f.db, row); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// TestExtendedApprovalWorkList 验证新旧来源统一分页、业务路径和原有权限约束。
// 参数：t 为真实隔离 MySQL 测试上下文。
// 返回值：无；权限扩大、跨范围可见、历史误入或坐标错误时失败。
func TestExtendedApprovalWorkList(t *testing.T) {
	f := newApprovalWorkFixture(t)
	service := application.Service{Repo: f.repo, NativeEnabled: true}
	director := approvalFixturePrincipal(f.rectifyingFixture, "director",
		"project.special_method.review", "project.deviation.review",
		"project.native_scope.approve", "project.report.correction.approve")
	director.Roles = []string{"technical_director"}
	list := func(p platform.Principal, q domain.ApprovalQuery) domain.ApprovalPage {
		t.Helper()
		page, err := service.ListApprovalTasks(t.Context(), p, q)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	query := domain.ApprovalQuery{View: "assigned", Page: 1, PageSize: 2}
	page := list(director, query)
	if page.Total != 4 || len(page.Items) != 2 || page.Items[0].ID != f.native ||
		page.Items[1].Type != "REPORT_CORRECTION" || !page.Items[0].CanDecide {
		t.Fatalf("first page=%+v", page)
	}
	nativeURL, err := url.Parse(page.Items[0].TargetURL)
	if err != nil || nativeURL.Path != "/project_management/project_detail" ||
		nativeURL.Query().Get("project") != f.nativeProject || nativeURL.Query().Get("scope_change_id") != f.native {
		t.Fatalf("native path=%+v err=%v", nativeURL, err)
	}
	query.Page = 2
	page = list(director, query)
	if len(page.Items) != 2 || page.Items[0].ID != f.deviation || page.Items[1].ID != f.method ||
		page.Items[0].Severity != "HIGH" || page.Items[0].Type != domain.ApprovalDeviation ||
		page.Items[1].TargetURL != "/project_management/methods?service_item_id="+f.method {
		t.Fatalf("second page=%+v", page)
	}
	query.Page, query.PageSize = 1, 20
	differentTenant := director
	differentTenant.TenantID = "other-tenant"
	if page = list(differentTenant, query); page.Total != 0 {
		t.Fatalf("cross tenant page=%+v", page)
	}
	projectOnly := director
	projectOnly.DataScopes = []platform.DataScope{{ScopeType: "PROJECT", ScopeID: f.nativeProject}}
	if page = list(projectOnly, query); page.Total != 1 || page.Items[0].ID != f.native {
		t.Fatalf("explicit project scope page=%+v", page)
	}
	business := approvalFixturePrincipal(f.rectifyingFixture, "submitter-user", "project.decomposition.manage")
	business.IdentityID, business.Roles = "submitter-identity", []string{"business_admin"}
	query.View = "submitted"
	if page = list(business, query); page.Total != 2 || page.Items[0].ID != f.native ||
		page.Items[0].CanDecide || page.Items[1].ID != f.method || page.Items[1].CanDecide {
		t.Fatalf("submitted native proposals must use identity without granting decision: %+v", page)
	}
	query.View = "assigned"
	lead := approvalFixturePrincipal(f.rectifyingFixture, "lead-user", "project.deviation.review")
	lead.Roles = []string{"team_lead"}
	if page = list(lead, query); page.Total != 1 || page.Items[0].ID != f.deviation {
		t.Fatalf("assigned lead page=%+v", page)
	}
	lead.UserID, lead.IdentityID = "unassigned", "unassigned-identity"
	if page = list(lead, query); page.Total != 0 {
		t.Fatalf("unassigned lead page=%+v", page)
	}
	director.Roles = []string{"quality_manager"}
	if page = list(director, query); page.Total != 3 {
		t.Fatalf("native role guard page=%+v", page)
	}
	director.Roles, director.IdentityID = []string{"technical_director"}, "submitter-identity"
	if page = list(director, query); page.Total != 3 {
		t.Fatalf("native identity cannot self-approve: %+v", page)
	}
	director.IdentityID = "director"
	service.NativeEnabled = false
	if page = list(director, query); page.Total != 3 {
		t.Fatalf("native feature gate lost: %+v", page)
	}
}

// TestExtendedApprovalWorkReadsOwnedResults 验证旧通知可精确找回原结果，其他账号看不到提交者的结果。
// 参数：t 为真实隔离 MySQL 测试上下文。
// 返回值：无；读错复核轮次、扩大权限或历史结果可重复操作时失败。
func TestExtendedApprovalWorkReadsOwnedResults(t *testing.T) {
	f := newApprovalWorkFixture(t)
	service := application.Service{Repo: f.repo, NativeEnabled: true}
	submitter := approvalFixturePrincipal(f.rectifyingFixture, "submitter-user")
	q := domain.ApprovalQuery{View: "assigned", Page: 1, PageSize: 20,
		RequestID: f.specialResult, TaskType: domain.ApprovalSpecialMethod}
	page, err := service.ListApprovalTasks(t.Context(), submitter, q)
	if err != nil || page.Total != 1 || page.Items[0].CanDecide || page.Items[0].Status != "REJECTED" ||
		page.Items[0].DecisionComment != "原轮次结论" {
		t.Fatalf("special result page=%+v err=%v", page, err)
	}
	submitter.UserID = "different-person"
	page, err = service.ListApprovalTasks(t.Context(), submitter, q)
	if err != nil || page.Total != 0 {
		t.Fatalf("unauthorized special result=%+v err=%v", page, err)
	}
	reporter := approvalFixturePrincipal(f.rectifyingFixture, "reporter", "project.deviation.report")
	reporter.Roles = []string{"engineer"}
	q.RequestID, q.TaskType, q.View = f.deviation, domain.ApprovalDeviation, "submitted"
	page, err = service.ListApprovalTasks(t.Context(), reporter, q)
	if err != nil || page.Total != 1 || page.Items[0].CanDecide {
		t.Fatalf("own pending deviation=%+v err=%v", page, err)
	}
	projectID := page.Items[0].ProjectID
	if err := createEvent(f.db, domain.DeliveryEvent{ID: ulid.Make().String(), TenantID: f.tenant,
		ProjectID: projectID, ServiceItemID: f.deviationItem, Type: application.EventDeviationReviewed,
		ActorUserID: "lead-user", CreatedAt: f.now.Add(4 * time.Second),
		Payload: map[string]any{"deviation_id": f.deviation, "decision": "RETEST", "comment": "重新核查"},
	}); err != nil {
		t.Fatal(err)
	}
	page, err = service.ListApprovalTasks(t.Context(), reporter, q)
	if err != nil || page.Total != 1 || page.Items[0].CanDecide || page.Items[0].Decision != "RETEST" ||
		page.Items[0].DecisionComment != "重新核查" {
		t.Fatalf("own deviation result=%+v err=%v", page, err)
	}
	q.RequestID, q.TaskType = "", ""
	page, err = service.ListApprovalTasks(t.Context(), reporter, q)
	if err != nil || page.Total != 0 {
		t.Fatalf("closed and old items omitted=%+v err=%v", page, err)
	}
}

// TestNativeScopeApprovalLinkReadsOriginalProposal 验证旧结果通知精确打开原提案，避开最近二十条限制。
// 参数：t 为隔离 MySQL 测试上下文。
// 返回值：无；错误落到新提案或跨范围可读时失败。
func TestNativeScopeApprovalLinkReadsOriginalProposal(t *testing.T) {
	f := newApprovalWorkFixture(t)
	scope := nativeScopeRecord{TenantID: f.tenant, ProjectID: f.nativeProject, Version: 1,
		SourceID: "native-origin", ActorIdentityID: "submitter-identity",
		ServiceItems: []byte(`[]`), CreatedAt: f.now}
	if err := f.db.Create(&scope).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("tenant_id=?", f.tenant).Delete(&nativeScopeRecord{}).Error; err != nil {
			t.Error(err)
		}
	})
	rows := make([]nativeScopeChangeRecord, 21)
	for i := range rows {
		id := ulid.Make().String()
		now := f.now.Add(-time.Duration(i+1) * time.Second)
		rows[i] = nativeScopeChangeRecord{ID: id, TenantID: f.tenant,
			ProjectID: f.nativeProject, OperationID: id, BaseScopeVersion: 1, Status: "REJECTED",
			RequestedBy: "submitter-identity", DecidedBy: "director", Comment: "原提案结论",
			ServiceItems: []byte(`[]`), CreatedAt: now, DecidedAt: &now}
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	id := rows[len(rows)-1].ID
	service := application.Service{Repo: f.repo, NativeEnabled: true}
	p := approvalFixturePrincipal(f.rectifyingFixture, "submitter-user", "project.decomposition.manage")
	p.IdentityID, p.Roles = "submitter-identity", []string{"business_admin"}
	page, err := service.ListApprovalTasks(t.Context(), p, domain.ApprovalQuery{
		View: "assigned", Page: 1, PageSize: 1, RequestID: id, TaskType: domain.ApprovalNativeScope,
	})
	if err != nil || page.Total != 1 || page.Items[0].Status != "REJECTED" ||
		page.Items[0].CanDecide || page.Items[0].DecisionComment != "原提案结论" {
		t.Fatalf("exact native result=%+v err=%v", page, err)
	}
	state, err := service.ReadNativeScope(t.Context(), p, f.nativeProject)
	if err != nil || len(state.Changes) != 20 || state.Changes[0].ID != f.native {
		t.Fatalf("ordinary history=%+v err=%v", state, err)
	}
	state, err = service.ReadNativeScopeChange(t.Context(), p, f.nativeProject, id)
	if err != nil || len(state.Changes) != 1 || state.Changes[0].ID != id ||
		state.Changes[0].Status != "REJECTED" || state.CanApprove {
		t.Fatalf("precise history=%+v err=%v", state, err)
	}
	state, err = service.ReadNativeScopeChange(t.Context(), p, f.nativeProject, "not-present")
	if err != nil || len(state.Changes) != 0 {
		t.Fatalf("missing link must never fall back to pending: %+v err=%v", state, err)
	}
	p.DataScopes = []platform.DataScope{{ScopeType: "PROJECT", ScopeID: f.projectID}}
	if _, err := service.ReadNativeScopeChange(t.Context(), p, f.nativeProject, id); err == nil {
		t.Fatal("scoped account read another project's proposal")
	}
}

// TestNativeScopeApprovalDecisionQueuesOriginalSubmitter 验证真实拒绝和重放只为原提交者落一条结果。
// 参数：t 为隔离 MySQL 测试上下文，用户编号和审批身份刻意区分。
// 返回值：无；待办未关闭、重复通知、发送地址或精确链接错误时失败。
func TestNativeScopeApprovalDecisionQueuesOriginalSubmitter(t *testing.T) {
	f := newApprovalWorkFixture(t)
	var proposal nativeScopeChangeRecord
	var project projectRecord
	if err := f.db.Where("id=?", f.native).Take(&proposal).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("id=?", f.nativeProject).Take(&project).Error; err != nil {
		t.Fatal(err)
	}
	if err := saveNativeScopeEvent(t.Context(), f.db, project, proposal,
		"NATIVE_SCOPE_CHANGE_REQUESTED", proposal.RequestedBy, "submitter-user"); err != nil {
		t.Fatal(err)
	}
	reviewer := approvalFixturePrincipal(f.rectifyingFixture, "director", "project.native_scope.approve")
	reviewer.Roles = []string{"technical_director"}
	service := application.Service{Repo: f.repo, NativeEnabled: true}
	decision := domain.NativeScopeDecision{OperationID: "decision-1",
		ExpectedVersion: 1, Decision: "REJECT", Comment: "请更新范围"}
	for range 2 {
		got, err := service.DecideNativeScope(t.Context(), reviewer, f.nativeProject, f.native, decision)
		if err != nil || got.Status != "REJECTED" {
			t.Fatalf("decision=%+v err=%v", got, err)
		}
	}
	page, err := service.ListApprovalTasks(t.Context(), reviewer,
		domain.ApprovalQuery{View: "assigned", Page: 1, PageSize: 20})
	if err != nil || page.Total != 0 {
		t.Fatalf("completed proposal remains assigned: %+v err=%v", page, err)
	}
	var rows []notificationOutboxRecord
	if err := f.db.Where("tenant_id=? AND event_type=?", f.tenant, "NATIVE_SCOPE_CHANGE_REJECTED").
		Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("result outbox rows=%d err=%v", len(rows), err)
	}
	var notice domain.NotificationMessage
	if err := json.Unmarshal(rows[0].Payload, &notice); err != nil || len(notice.Recipients) != 1 ||
		notice.Recipients[0] != "submitter-user" || notice.ReferenceID != f.native {
		t.Fatalf("result notice recipient/reference mismatch, err=%v", err)
	}
	target, err := url.Parse(notice.TargetURL)
	if err != nil || target.Query().Get("project") != f.nativeProject ||
		target.Query().Get("scope_change_id") != f.native {
		t.Fatalf("result target is not the original proposal: %v", err)
	}
}

// TestServiceItemApprovalResultsQueueActualSubmitters 验证原业务接口在真实迁移结构中保存处理结果及定向通知。
// 参数：t 为隔离 MySQL 测试上下文。
// 返回值：无；结果状态和投递事务无法正确收口时失败。
func TestServiceItemApprovalResultsQueueActualSubmitters(t *testing.T) {
	f := newApprovalWorkFixture(t)
	service := application.Service{Repo: f.repo}
	reviewer := approvalFixturePrincipal(f.rectifyingFixture, "director",
		"project.special_method.review", "project.deviation.review")
	reviewer.Roles = []string{"technical_director"}
	if err := service.ReviewSpecialMethod(t.Context(), reviewer, f.method,
		domain.SpecialMethodReviewInput{Decision: "APPROVED", Comment: "核验通过"}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReviewDeviation(t.Context(), reviewer, f.deviation,
		domain.DeviationReviewInput{Decision: "RELEASE", Comment: "批准放行"}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListApprovalTasks(t.Context(), reviewer,
		domain.ApprovalQuery{View: "assigned", Page: 1, PageSize: 20})
	if err != nil || page.Total != 0 {
		t.Fatalf("completed reviews remain assigned: %+v err=%v", page, err)
	}
	for _, want := range []struct {
		event, recipient, path, key string
	}{
		{application.EventSpecialMethodReviewed, "submitter-user", "/project_management/methods", "review_id"},
		{application.EventDeviationReviewed, "reporter", "/project_management/exceptions", "deviation_id"},
	} {
		var rows []notificationOutboxRecord
		if err := f.db.Where("tenant_id=? AND event_type=?", f.tenant, want.event).
			Find(&rows).Error; err != nil || len(rows) != 1 {
			t.Fatalf("%s outbox=%d err=%v", want.event, len(rows), err)
		}
		var notice domain.NotificationMessage
		if err := json.Unmarshal(rows[0].Payload, &notice); err != nil || len(notice.Recipients) != 1 ||
			notice.Recipients[0] != want.recipient {
			t.Fatalf("%s result recipient mismatch, err=%v", want.event, err)
		}
		target, err := url.Parse(notice.TargetURL)
		if err != nil || target.Path != want.path || target.Query().Get(want.key) != notice.ReferenceID {
			t.Fatalf("%s result target mismatch, err=%v", want.event, err)
		}
	}
}
