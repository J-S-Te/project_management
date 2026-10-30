package mysql

import (
	"context"
	"errors"
	"fmt"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

// TestImportLinkRejectsExistingPrimary 验证新绑定不能把另一项目主合同映射到目标项目。
// 参数：t为本地隔离事务测试上下文。
// 返回值：无；错误归属或错误回执未被拒绝时失败。
func TestImportLinkRejectsExistingPrimary(t *testing.T) {
	db := importBindingDB(t)
	input := importBindingCommand("tenant")
	for _, row := range []projectRecord{{ID: "P1", TenantID: "tenant", ContractID: input.ContractID, Contract: input.Contract.ContractNumber, CustomerID: "customer", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}, {ID: "P2", TenantID: "tenant", ContractID: "original", Contract: "HT-OLD", CustomerID: "customer", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}} {
		if e := db.Create(&row).Error; e != nil {
			t.Fatal(e)
		}
	}
	input.ProjectMode, input.ExistingProjectID, input.Relationship = "LINK_EXISTING", "P2", "SUPPLEMENT"
	input.OriginalContractRef, input.ExpectedProjectVersion = "original", 1
	_, err := NewRepository(db).BindContractImport(context.Background(), input, "digest", true, func(application.Repository) (domain.Project, error) {
		t.Fatal("LINK不应创建")
		return domain.Project{}, nil
	})
	if !errors.Is(err, application.ErrImportConflict) {
		t.Fatalf("已属于P1的合同被绑定P2: %v", err)
	}
	var count int64
	if e := db.Model(&contractImportBindingRecord{}).Count(&count).Error; e != nil || count != 0 {
		t.Fatalf("冲突留下映射 count=%d error=%v", count, e)
	}
}

// TestOrdinarySupplementRemainsShared 验证普通补充仍可使用已建主项目的合同并跨项目共享。
// 参数：t为隔离事务测试上下文。
// 返回值：无；普通共享被误认独占或专用导入冲突未拦截时失败。
func TestOrdinarySupplementRemainsShared(t *testing.T) {
	db := importBindingDB(t)
	if err := db.Create(&projectRecord{ID: "primary", TenantID: "tenant", ContractID: "ordinary", Contract: "HT-ORDINARY", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"first", "second"} {
		if err := db.Transaction(func(tx *gorm.DB) error { return checkOrdinarySupplementImport(tx, "tenant", "ordinary", project) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&contractImportBindingRecord{TenantID: "tenant", CommandID: "command", ContractID: "exclusive", OriginImportItemID: "item", SourceApplication: "contract_management", ProjectID: "import-target", PayloadSHA256: "digest", CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return checkOrdinarySupplementImport(tx, "tenant", "exclusive", "other") }); !errors.Is(err, application.ErrImportConflict) {
		t.Fatalf("历史专用映射未隔离: %v", err)
	}
}

// TestImportLinkRacesOrdinaryCreateMySQL 验证跨关系竞争只有一个真实归属。
// 参数：t为显式隔离MySQL上下文。
// 返回值：无；普通主合同和导入补充同时成功、查询分裂或锁错误时失败。
func TestImportLinkRacesOrdinaryCreateMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	tenant = tenant[len(tenant)-26:]
	ctx := context.Background()
	repo := NewRepository(db)
	t.Cleanup(func() {
		for _, table := range []string{"pm_contract_identity_guard", "pm_contract_import_binding", "pm_project", "prj_audit_outbox"} {
			if e := db.Table(table).Where("tenant_id=?", tenant).Delete(map[string]any{}).Error; e != nil {
				t.Error(e)
			}
		}
	})
	for n := range 8 {
		suffix := fmt.Sprint(n)
		input := importBindingCommand(tenant)
		input.ContractID = "C-" + suffix
		input.Contract.ContractID = input.ContractID
		input.Contract.ContractNumber = "HT-" + suffix
		input.CommandID = fmt.Sprintf("%032d", n+1)
		input.OriginImportItemID = "item-" + suffix
		input.ProjectMode, input.Relationship, input.ExistingProjectID = "LINK_EXISTING", "SUPPLEMENT", "target-"+suffix
		input.OriginalContractRef, input.ExpectedProjectVersion = "original-"+suffix, 1
		target := projectRecord{ID: input.ExistingProjectID, TenantID: tenant, ContractID: input.OriginalContractRef, Contract: "OLD-" + suffix, CustomerID: "customer", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if e := db.Create(&target).Error; e != nil {
			t.Fatal(e)
		}
		ordinary := domain.Project{ID: "ordinary-" + suffix, TenantID: tenant, ContractID: input.ContractID, Contract: input.Contract.ContractNumber, CustomerID: "customer", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var linkErr, createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, linkErr = repo.BindContractImport(ctx, input, "digest", true, func(application.Repository) (domain.Project, error) {
				return domain.Project{}, errors.New("unexpected CREATE")
			})
		}()
		go func() { defer wg.Done(); <-start; createErr = repo.CreateProject(ctx, ordinary) }()
		close(start)
		wg.Wait()
		if (linkErr == nil) == (createErr == nil) {
			t.Fatalf("归属竞争结果错误: link=%v create=%v", linkErr, createErr)
		}
		if linkErr != nil && !errors.Is(linkErr, application.ErrImportConflict) {
			t.Fatal(linkErr)
		}
		if createErr != nil && !errors.Is(createErr, application.ErrDuplicateProject) {
			t.Fatal(createErr)
		}
		found, e := repo.FindProjectByContract(ctx, platform.ScopeFilter{TenantID: tenant, AllowAll: true}, input.ContractID, input.Contract.ContractNumber)
		if e != nil {
			t.Fatal(e)
		}
		expected := ordinary.ID
		if linkErr == nil {
			expected = target.ID
		}
		if found.ID != expected {
			t.Fatalf("查询与完成归属分裂: got=%s expected=%s", found.ID, expected)
		}
	}
}
