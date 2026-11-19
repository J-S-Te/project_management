package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/oklog/ulid/v2"
)

// TestCRMProjectHistoryScopesAndPaginatesMySQL 验证真实 MySQL 中的客户关联及分页口径。
// 参数：t 为测试上下文；数据库仅来自明确隔离的专用 DSN 文件。
// 返回值：无。
func TestCRMProjectHistoryScopesAndPaginatesMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	suffix := ulid.Make().String()
	old := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	latest := old.Add(time.Hour)
	projects := []projectRecord{
		{ID: "P-1-" + suffix, TenantID: tenant, CustomerID: "42", SourceKind: "CONTRACT"},
		{ID: "P-2-" + suffix, TenantID: tenant, CustomerID: "42", SourceKind: "CONTRACT"},
		{ID: "P-3-" + suffix, TenantID: tenant, CustomerID: "420", SourceKind: "CONTRACT"},
		{ID: "P-4-" + suffix, TenantID: tenant + "-other", CustomerID: "42", SourceKind: "CONTRACT"},
		{
			ID: "P-5-" + suffix, TenantID: tenant, CustomerID: "42", SourceKind: "NATIVE",
			SourceID: "OP-" + suffix, ScopeVersion: 1,
		},
	}
	for i := range projects {
		p := &projects[i]
		p.Name, p.CreatedAt, p.UpdatedAt = p.ID, old, old
		if p.SourceKind == "CONTRACT" {
			p.ContractID, p.Contract = p.ID, p.ID
		}
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	item := serviceItemRecord{
		ID: "S-" + suffix, TenantID: tenant, ProjectID: projects[1].ID,
		Status: domain.ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED",
		CreatedAt: old, UpdatedAt: latest,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repo := NewRepository(db)
	first, total, err := repo.CRMCustomerProjects(ctx, tenant, "42", 1, 1)
	if err != nil || total != 2 || len(first) != 1 || first[0].ProjectID != projects[1].ID ||
		first[0].Progress != 100 || first[0].Status != domain.ProjectStatusCompleted ||
		!first[0].SourceUpdatedAt.Equal(latest) {
		t.Fatalf("first page=%#v total=%d err=%v", first, total, err)
	}
	second, total, err := repo.CRMCustomerProjects(ctx, tenant, "42", 2, 1)
	if err != nil || total != 2 || len(second) != 1 || second[0].ProjectID != projects[0].ID {
		t.Fatalf("second page=%#v total=%d err=%v", second, total, err)
	}
	empty, total, err := repo.CRMCustomerProjects(ctx, tenant, "43", 1, 1)
	if err != nil || total != 0 || empty == nil || len(empty) != 0 {
		t.Fatalf("empty page=%#v total=%d err=%v", empty, total, err)
	}
	if !db.Migrator().HasIndex(&projectRecord{}, "idx_pm_project_crm_customer") {
		t.Fatal("CRM customer project index is missing")
	}
}
