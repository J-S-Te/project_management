package mysql

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
)

// expensePreparationRow 创建独立测试库准备事件，不写现行业务库。
// 参数：t/r为测试环境，id/item/mode/reference为事件事实来源。
// 返回值：持久化后的事件行。
func expensePreparationRow(t *testing.T, r *Repository, id, item, mode, reference string) deliveryEventRecord {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"expected_version": 3, "travel": domain.TravelArrangementInput{Mode: mode, RequestID: "trip", ReferenceEventID: reference, Origin: "出发地", Destination: "现场", DepartureDate: "2026-11-09", ReturnDate: "2026-11-11", Transport: "铁路", TravelerIDs: []string{"u", "not-disclosed"}}})
	row := deliveryEventRecord{ID: id, TenantID: "t", ProjectID: "p", ServiceItemID: item, EventType: application.EventPreparationStarted, ActorUserID: "manager", Payload: payload, CreatedAt: time.Now().UTC()}
	if err := r.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// TestExpenseTerminatedSearchAndTravelFactsUseCurrentQualification 验证已终止仍参与可见但不可直接提交及行程回源。
// 参数：t为测试上下文。
// 返回值：无；租户、人员、状态或来源边界不符时失败。
func TestExpenseTerminatedSearchAndTravelFactsUseCurrentQualification(t *testing.T) {
	r := expenseRepositoryFixture(t)
	if err := r.db.AutoMigrate(&penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.db.Model(&projectRecord{}).Where("id=?", "p").Updates(map[string]any{"status": domain.ProjectStatusTerminated}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := r.db.Create(&serviceItemRecord{ID: id, TenantID: "t", ProjectID: "p", Site: "现场", Status: domain.ProjectStatusTerminated, EngineerIDs: []byte(`["u"]`), Version: 4}).Error; err != nil {
			t.Fatal(err)
		}
	}
	expensePreparationRow(t, r, "source", "b", "NEW", "")
	expensePreparationRow(t, r, "current", "a", "EXISTING", "source")
	service := application.Service{Repo: r, Personnel: expenseDirectory{Active: true}}
	page, err := service.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{ActorSubjectID: "u", PageSize: 20})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("search %+v %v", page, err)
	}
	for _, ref := range page.Items {
		if ref.Eligible || !ref.ParticipantEligible || !slices.Contains(ref.ReasonCodes, "SERVICE_ITEM_TERMINATED") {
			t.Fatal("state qualification conflated", ref)
		}
	}
	in := application.ExpenseBatchInput{ActorSubjectID: "u", ClaimID: "c", DraftVersion: 1, References: []application.ExpenseReferenceInput{{ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-10"}}}
	validated, err := service.BatchValidateExpenseReferences(ctx, "t", in)
	if err != nil {
		t.Fatal(err)
	}
	ref := validated.Items[0]
	if ref.TravelSnapshot.Status != "KNOWN" || len(ref.TravelSnapshot.SourceChain) != 2 || ref.TravelSnapshot.ActorInTravelers == nil || !*ref.TravelSnapshot.ActorInTravelers {
		t.Fatalf("travel %+v", ref.TravelSnapshot)
	}
	data, _ := json.Marshal(ref)
	if slices.ContainsFunc(ref.TravelSnapshot.ReasonCodes, func(s string) bool { return s == "APPROVED" }) || len(data) == 0 {
		t.Fatal("invented approval")
	}
	page, err = service.SearchExpenseReferences(ctx, "t", application.ExpenseSearchInput{ActorSubjectID: "outsider", PageSize: 20})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("outsider reference", page, err)
	}
	in.ActorSubjectID = "outsider"
	validated, err = service.BatchValidateExpenseReferences(ctx, "t", in)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Items[0].ParticipantEligible || validated.Items[0].TravelSnapshot.Status != "UNKNOWN" {
		t.Fatal("unauthorized facts leaked")
	}
	if _, err = service.BatchValidateExpenseReferences(ctx, "foreign", in); err == nil {
		t.Fatal("tenant boundary ignored")
	}
}

// TestExpenseTravelReaderExactSourceScopeAndRevocation 验证来源精确查询不回退第一条且当前撤销清除有效行程事实。
// 参数：t为测试上下文。
// 返回值：无；任意来源、跨项目服务项或撤销事实被忽略时失败。
func TestExpenseTravelReaderExactSourceScopeAndRevocation(t *testing.T) {
	r := expenseRepositoryFixture(t)
	ctx := context.Background()
	for _, row := range []serviceItemRecord{{ID: "a", TenantID: "t", ProjectID: "p"}, {ID: "b", TenantID: "t", ProjectID: "p"}, {ID: "foreign", TenantID: "t", ProjectID: "other"}} {
		if err := r.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := expensePreparationRow(t, r, "source", "b", "NEW", "")
	expensePreparationRow(t, r, "other-source", "a", "NEW", "")
	event, err := r.ReadExpenseTravelEvent(ctx, "t", "p", source.ID)
	if err != nil || event == nil || event.TenantID != "t" || event.ID != source.ID {
		t.Fatal(event, err)
	}
	if event, err = r.ReadExpenseTravelEvent(ctx, "t", "p", "missing"); err != nil || event != nil {
		t.Fatal("guessed source", event, err)
	}
	if event, err = r.ReadExpenseTravelEvent(ctx, "foreign", "p", source.ID); err != nil || event != nil {
		t.Fatal("cross tenant source", event, err)
	}
	bad := source
	bad.ID = "bad-item"
	bad.ServiceItemID = "foreign"
	if err = r.db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	if event, err = r.ReadExpenseTravelEvent(ctx, "t", "p", bad.ID); err != nil || event != nil {
		t.Fatal("source item crossed project", event, err)
	}
	revoked := deliveryEventRecord{ID: "latest-revoked", TenantID: "t", ProjectID: "p", ServiceItemID: "a", EventType: application.EventPreparationRevoked, Payload: []byte(`{}`), CreatedAt: time.Now().UTC().Add(time.Second)}
	if err = r.db.Create(&revoked).Error; err != nil {
		t.Fatal(err)
	}
	if event, err = r.ReadExpensePreparation(ctx, "t", "p", "a"); err != nil || event == nil || event.ID != revoked.ID {
		t.Fatal("revocation ignored", event, err)
	}
}
