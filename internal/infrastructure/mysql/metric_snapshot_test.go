package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestMetricSnapshotUnknownHistoryAndCommitBoundary 验证历史unknown、更正不重获首次样本及只读提交失败关闭。
// 参数：t 为测试上下文。
// 返回值：无；无证据历史被记通过或失败快照被发布时失败。
func TestMetricSnapshotUnknownHistoryAndCommitBoundary(t *testing.T) {
	repo, boundary, db := snapshotTestDB(t)
	if err := db.AutoMigrate(&reportRevisionRecord{}, &metricReviewRecord{}, &metricResourceMutexRecord{}, &metricCapacityRecord{}, &metricUsageRecord{}, &metricOperationRecord{}, &capabilityRecord{}); err != nil {
		t.Fatal(err)
	}
	_, item := seedMetricProject(t, db, "SERVICE_ITEM")
	var revision reportRevisionRecord
	db.First(&revision)
	if err := db.Model(&revision).Update("review_required", false).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	other := revision
	other.ID = 0
	other.Revision = 1
	other.ReviewRequired = true
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	round := metricReviewRecord{domain.ReportReviewRound{ID: "new-version-round", TenantID: "tenant", ProjectID: item.ProjectID, ServiceItemID: item.ID, SubjectType: "SERVICE_ITEM", SubjectID: item.ID, Revision: 1, RoundNo: 1, Decision: "PASSED", DecidedAt: &now, SubmittedAt: now}}
	if err := db.Create(&round).Error; err != nil {
		t.Fatal(err)
	}
	out, err := repo.ReadMetricFacts(context.Background(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Complete || len(out.Rows) != 1 || out.Rows[0].Status != "UNKNOWN" || out.Rows[0].Period != "" || !boundary.committed || boundary.opts == nil || !boundary.opts.ReadOnly {
		t.Fatalf("invalid unknown snapshot %+v", out)
	}
	boundary.commitErr = errors.New("synthetic commit failure")
	if out, err = repo.ReadMetricFacts(context.Background(), "tenant"); err == nil || out.Complete {
		t.Fatal("failed snapshot published complete")
	}
}

// TestSettlementMetricReferenceItemsIncludeHistoryWithinTenant 验证结算只读服务项身份包含合法历史且不跨租户。
// 参数：t 为测试上下文。
// 返回值：无；漏掉历史身份或泄漏他租户时失败。
func TestSettlementMetricReferenceItemsIncludeHistoryWithinTenant(t *testing.T) {
	repo, db := metricTestDB(t)
	_, item := seedMetricProject(t, db, "SERVICE_ITEM")
	historical := item
	historical.ID = "SI-history"
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&serviceItemRecord{}).Where("id=?", historical.ID).Update("archived_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	other := item
	other.ID = "SI-other"
	other.TenantID = "other"
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := repo.SettlementReferenceServiceItems(context.Background(), "tenant", item.ProjectID)
	if err != nil || len(ids) != 2 || ids[0] != "SI-1" || ids[1] != "SI-history" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

// TestMetricSnapshotCancelledHistoryDoesNotCreateUnknown 捕获合法跨组织容量更正后旧历史制造虚假未知。
// 参数：t 为测试上下文。
// 返回值：无；取消或替换历史进入净指标、或旧证据被删除时失败。
func TestMetricSnapshotCancelledHistoryDoesNotCreateUnknown(t *testing.T) {
	_, db := metricTestDB(t)
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	for _, cap := range []domain.ResourceCapacity{
		{ID: "old", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-a", Status: "SUPERSEDED", Minutes: 240},
		{ID: "new", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-b", Status: "PUBLISHED", Minutes: 240},
	} {
		if err := db.Create(&metricCapacityRecord{cap}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, usage := range []domain.ResourceUsage{
		{ID: "cancelled", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-a", Status: "CANCELLED", Minutes: 60, Start: at, End: at.Add(time.Hour)},
		{ID: "replaced", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-a", Status: "REPLACED", Minutes: 60, Start: at, End: at.Add(time.Hour)},
		{ID: "draft", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-a", Status: "DRAFT"},
		{ID: "returned", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-a", Status: "RETURNED"},
		{ID: "confirmed", TenantID: "tenant", ResourceType: "PERSON", ResourceID: "archive", IdentityID: "person", BusinessDate: "2026-11-01", OrganizationID: "org-b", Status: "CONFIRMED", Minutes: 60, Start: at, End: at.Add(time.Hour)},
	} {
		if err := db.Create(&metricUsageRecord{usage}).Error; err != nil {
			t.Fatal(err)
		}
	}
	out := domain.MetricFactsSnapshot{}
	if err := metricResourceRows(db, "tenant", &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].OrganizationID != "org-b" || out.Rows[0].ActualMinutes != 60 || out.Rows[0].CapacityMinutes != 240 || out.Rows[0].UnknownCount != 0 {
		t.Fatalf("historical false unknown %+v", out.Rows)
	}
	var count int64
	db.Model(&metricUsageRecord{}).Count(&count)
	if count != 5 {
		t.Fatalf("history lost %d", count)
	}
}
