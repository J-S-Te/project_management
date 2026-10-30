package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"gorm.io/gorm"
)

// importBindingCommand 提供冻结的合法接续样本，不包含个人信息或外部凭据。
// 参数：tenant 为隔离测试租户。
// 返回值：版本一CREATE_NEW命令。
func importBindingCommand(tenant string) domain.ContractImportBindingCommand {
	return domain.ContractImportBindingCommand{SchemaVersion: 1, CommandID: strings.Repeat("a", 32), TenantID: tenant, ContractID: "contract-import", ContractVersion: "3", OriginImportItemID: "import-item", SourceRevision: 1, PolicyDigest: strings.Repeat("b", 64), ReleaseVersion: 1, ImportMode: "CONTINUE_LIVE", ProjectMode: "CREATE_NEW", Relationship: "PRIMARY", TargetOwnerUserID: "owner", Contract: domain.ContractActivation{ContractID: "contract-import", ContractNumber: "HT-IMPORT", ContractVersion: "3", ContractName: "接续测试", Customer: "测试客户", CustomerID: "customer", EffectiveAt: time.Now().UTC(), Services: []domain.ContractService{{SourceID: "service-1", Site: "测试站点", Batch: "一期", Category: "检测", TestMode: "STANDARD"}}}}
}

// importBindingDB 创建内存事务夹具；不连接现行业务环境。
// 参数：t 为测试上下文。
// 返回值：具备项目与接续表的SQLite连接。
func importBindingDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := equipmentAtomicityDB(t)
	if err := db.AutoMigrate(&contractImportBindingRecord{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// testImportProjectBindingIdempotent 核验原子写入、重放和来源唯一性。
// 参数：t 为测试上下文，db 为隔离连接，tenant 为专用租户。
// 返回值：无。
func testImportProjectBindingIdempotent(t *testing.T, db *gorm.DB, tenant string) {
	t.Helper()
	input := importBindingCommand(tenant)
	repo := NewRepository(db)
	called := 0
	create := func(tx application.Repository) (domain.Project, error) {
		called++
		row := projectRecord{ID: "P-IMPORT", TenantID: tenant, ContractID: input.ContractID, Contract: "HT-IMPORT", ContractVersion: "3", CustomerID: "customer", OwnerIdentityID: "owner", Name: "项目", Customer: "客户", SupplementStatus: "NONE", Status: "待拆解确认", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Version: 1}
		if err := tx.(*Repository).db.Create(&row).Error; err != nil {
			return domain.Project{}, err
		}
		return projectFromRecord(row), nil
	}
	result, err := repo.BindContractImport(context.Background(), input, "digest", true, create)
	if err != nil || result.ProjectID != "P-IMPORT" || result.ProjectVersion != 1 || result.Status != "PROJECT_BOUND" {
		t.Fatalf("create: %+v %v", result, err)
	}
	replay, err := repo.BindContractImport(context.Background(), input, "digest", false, create)
	if err != nil || replay != result || called != 1 {
		t.Fatalf("disabled replay: %+v %v calls=%d", replay, err, called)
	}
	if _, err := repo.BindContractImport(context.Background(), input, "different", true, create); !errors.Is(err, application.ErrImportConflict) {
		t.Fatalf("changed command content: %v", err)
	}
	input.CommandID = strings.Repeat("c", 32)
	if _, err := repo.BindContractImport(context.Background(), input, "digest", true, create); !errors.Is(err, application.ErrImportConflict) {
		t.Fatalf("same source new command: %v", err)
	}
	var count int64
	if err := db.Model(&projectRecord{}).Where("tenant_id=?", tenant).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("projects=%d err=%v", count, err)
	}
	queried, err := repo.GetContractImportBinding(context.Background(), tenant, "import-item")
	if err != nil || queried != result {
		t.Fatalf("query=%+v %v", queried, err)
	}
	if _, err := repo.GetContractImportBinding(context.Background(), tenant+"-other", "import-item"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("cross tenant query: %v", err)
	}
	input.OriginImportItemID = "second-item"
	input.ContractID = "second-contract"
	if _, err := repo.BindContractImport(context.Background(), input, "digest", false, create); !errors.Is(err, application.ErrImportDisabled) {
		t.Fatalf("disabled new: %v", err)
	}
}

// TestImportProjectBindingIdempotent 核验默认测试环境的真实本地事务和重放。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingIdempotent(t *testing.T) {
	testImportProjectBindingIdempotent(t, importBindingDB(t), "tenant")
}

// TestImportProjectBindingIdempotentMySQL 在专用MySQL验证唯一键、迁移和原子回执。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingIdempotentMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	t.Cleanup(func() { db.Where("tenant_id=?", tenant).Delete(&contractImportBindingRecord{}) })
	testImportProjectBindingIdempotent(t, db, tenant)
}

// TestImportProjectBindingLinkScopeConflict 检查LINK的租户、客户、原身份及版本边界。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingLinkScopeConflict(t *testing.T) {
	for _, scenario := range []string{"customer", "original", "version", "tenant", "primary", "terminated", "success"} {
		t.Run(scenario, func(t *testing.T) {
			db := importBindingDB(t)
			row := projectRecord{ID: "existing", TenantID: "tenant", Name: "既有", ContractID: "original", Contract: "HT-OLD", ContractVersion: "1", CustomerID: "customer", Version: 5, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if scenario == "terminated" {
				row.Status = domain.ProjectStatusTerminated
			}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			input := importBindingCommand("tenant")
			input.ProjectMode = "LINK_EXISTING"
			input.ExistingProjectID = "existing"
			input.ExpectedProjectVersion = 5
			input.OriginalContractRef = "original"
			input.Relationship = "SUPPLEMENT"
			switch scenario {
			case "customer":
				input.Contract.CustomerID = "other"
			case "original":
				input.OriginalContractRef = "other"
			case "version":
				input.ExpectedProjectVersion = 4
			case "tenant":
				input.TenantID = "other"
			case "primary":
				input.Relationship = "PRIMARY"
			}
			result, err := NewRepository(db).BindContractImport(context.Background(), input, "digest", true, func(application.Repository) (domain.Project, error) {
				t.Fatal("LINK invoked create")
				return domain.Project{}, nil
			})
			if scenario == "success" {
				if err != nil || result.ProjectVersion != 6 {
					t.Fatalf("link=%+v %v", result, err)
				}
			} else if !errors.Is(err, application.ErrImportConflict) {
				t.Fatalf("%s: %v", scenario, err)
			}
			var latest projectRecord
			if err := db.First(&latest, "id=?", "existing").Error; err != nil {
				t.Fatal(err)
			}
			if latest.ContractID != "original" || latest.Contract != "HT-OLD" {
				t.Fatal("link overwrote main contract")
			}
			if scenario == "success" {
				service := application.Service{Repo: NewRepository(db)}
				p := platform.Principal{TenantID: "tenant", UserID: "contract_management", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}}
				if _, err := service.ActivateContract(context.Background(), p, input.Contract); !errors.Is(err, application.ErrImportPolicy) {
					t.Fatalf("supplement relation entered legacy stamp path: %v", err)
				}
				mapped, err := NewRepository(db).FindProjectByContract(context.Background(), platform.ScopeFilter{TenantID: "tenant", AllowAll: true}, input.ContractID, input.Contract.ContractNumber)
				if err != nil || mapped.ID != "existing" {
					t.Fatalf("authorized contract relation lookup=%+v %v", mapped, err)
				}
				_, err = NewRepository(db).FindProjectByContract(context.Background(), platform.ScopeFilter{TenantID: "tenant", OrganizationIDs: []string{"denied"}}, input.ContractID, input.Contract.ContractNumber)
				if !errors.Is(err, application.ErrNotFound) {
					t.Fatalf("relation lookup widened scope: %v", err)
				}
			}
		})
	}
}

// TestImportProjectBindingRollback 证明业务写入后失败不会留下项目或成功映射。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingRollback(t *testing.T) {
	db := importBindingDB(t)
	_, err := NewRepository(db).BindContractImport(context.Background(), importBindingCommand("tenant"), "digest", true, func(tx application.Repository) (domain.Project, error) {
		err := tx.(*Repository).db.Create(&projectRecord{ID: "rollback", TenantID: "tenant", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error
		if err != nil {
			return domain.Project{}, err
		}
		return domain.Project{}, application.ErrValidation
	})
	if !errors.Is(err, application.ErrValidation) {
		t.Fatal(err)
	}
	for _, table := range []string{"pm_project", "pm_contract_import_binding"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("table=%s count=%d err=%v", table, count, err)
		}
	}
}

type importOwnerDirectory struct{}

// List 模拟受控人员目录的精确有效用户，不替代真实平台验收。
// 参数：ctx 为请求，q 为精确人员查询。
// 返回值：仅owner是有效用户。
func (importOwnerDirectory) List(ctx context.Context, q platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if q.UserID != "owner" {
		return platform.OwnerDirectoryPage{}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: "owner", DisplayName: "测试负责人"}}, Total: 1}, nil
}

// TestImportProjectBindingCreateMySQL 以真实隔离MySQL贯通现有拆解、事件及回执事务。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingCreateMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	tenant = strings.TrimPrefix(tenant, "SNAP-")
	t.Cleanup(func() {
		for _, table := range []string{"pm_contract_import_binding", "pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "prj_audit_outbox", "pm_service_item", "pm_project"} {
			db.Table(table).Where("tenant_id=?", tenant).Delete(map[string]any{})
		}
	})
	service := application.Service{Repo: NewRepository(db), Personnel: importOwnerDirectory{}}
	input := importBindingCommand(tenant)
	p := platform.Principal{TenantID: tenant, UserID: "contract_management", IdentityID: "contract_management", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}}
	result, err := service.BindImportedContract(context.Background(), p, input, true)
	if err != nil {
		t.Fatal(err)
	}
	var row projectRecord
	if err := db.First(&row, "tenant_id=? AND id=?", tenant, result.ProjectID).Error; err != nil {
		t.Fatal(err)
	}
	if row.OwnerIdentityID != "owner" || row.Status != "待拆解确认" || result.ProjectVersion != row.Version {
		t.Fatalf("project=%+v receipt=%+v", row, result)
	}
	var items []serviceItemRecord
	if err := db.Where("tenant_id=? AND project_id=?", tenant, row.ID).Find(&items).Error; err != nil || len(items) != 1 || items[0].Status != domain.ServiceItemStatusPendingConfirm {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	var event deliveryEventRecord
	if err := db.Where("tenant_id=? AND project_id=? AND event_type=?", tenant, row.ID, application.EventContractActivated).Take(&event).Error; err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["stamped_contract_uploaded"] != false || payload["origin_kind"] != "IMPORTED" || event.ActorUserID != "contract_management" {
		t.Fatalf("import event lost provenance: %+v", payload)
	}
	var audit auditOutboxRecord
	if err := db.Where("tenant_id=? AND resource_id=? AND action=?", tenant, row.ID, "PROJECT_MANAGEMENT:CONTRACT_ACTIVATED").Take(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.ActorType != "SYSTEM" {
		t.Fatalf("machine import incorrectly audited as %s", audit.ActorType)
	}
	badOwner := input
	badOwner.CommandID = strings.Repeat("d", 32)
	badOwner.OriginImportItemID = "bad-owner-item"
	badOwner.ContractID = "bad-owner-contract"
	badOwner.Contract.ContractID = badOwner.ContractID
	badOwner.TargetOwnerUserID = "contract_management"
	if _, err := service.BindImportedContract(context.Background(), p, badOwner, true); !errors.Is(err, application.ErrValidation) {
		t.Fatalf("machine cannot become owner: %v", err)
	}
	service.Personnel = nil
	replay, err := service.BindImportedContract(context.Background(), p, input, false)
	if err != nil || replay != result {
		t.Fatalf("recovery after owner directory unavailable=%+v %v", replay, err)
	}
}

// TestImportProjectBindingLinkMySQL 核验真实行锁事务中既有项目关系和版本边界。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingLinkMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	t.Cleanup(func() { db.Where("tenant_id=?", tenant).Delete(&contractImportBindingRecord{}) })
	row := projectRecord{ID: "P-LINK-" + tenant[5:20], TenantID: tenant, Name: "既有项目", Customer: "客户", CustomerID: "customer", Contract: "HT-OLD", ContractID: "original", ContractVersion: "1", SupplementStatus: "NONE", Version: 5, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	command := importBindingCommand(tenant)
	command.ProjectMode = "LINK_EXISTING"
	command.ExistingProjectID = row.ID
	command.ExpectedProjectVersion = 5
	command.Relationship = "SUPPLEMENT"
	command.OriginalContractRef = "original"
	noCreate := func(application.Repository) (domain.Project, error) {
		t.Fatal("LINK called CREATE")
		return domain.Project{}, nil
	}
	repo := NewRepository(db)
	for _, scenario := range []string{"customer", "original", "version", "tenant", "primary"} {
		changed := command
		switch scenario {
		case "customer":
			changed.Contract.CustomerID = "other"
		case "original":
			changed.OriginalContractRef = "other"
		case "version":
			changed.ExpectedProjectVersion = 4
		case "tenant":
			changed.TenantID = tenant + "-other"
		case "primary":
			changed.Relationship = "PRIMARY"
		}
		if _, err := repo.BindContractImport(context.Background(), changed, "digest", true, noCreate); !errors.Is(err, application.ErrImportConflict) {
			t.Fatalf("%s accepted: %v", scenario, err)
		}
	}
	result, err := repo.BindContractImport(context.Background(), command, "digest", true, noCreate)
	if err != nil || result.ProjectVersion != 6 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var latest projectRecord
	if err := db.First(&latest, "id=?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if latest.ContractID != row.ContractID || latest.Contract != row.Contract || latest.Version != 6 {
		t.Fatalf("main contract overwritten: %+v", latest)
	}
}

// TestImportProjectBindingConcurrentMySQL 核验响应丢失后的并发重试只提交一份项目和回执。
// 参数：t 为测试上下文。
// 返回值：无。
func TestImportProjectBindingConcurrentMySQL(t *testing.T) {
	db, tenant := snapshotMySQLFixture(t)
	t.Cleanup(func() { db.Where("tenant_id=?", tenant).Delete(&contractImportBindingRecord{}) })
	input := importBindingCommand(tenant)
	projectID := "P-RACE-" + tenant[5:25]
	var creates atomic.Int32
	callback := func(tx application.Repository) (domain.Project, error) {
		creates.Add(1)
		row := projectRecord{ID: projectID, TenantID: tenant, ContractID: input.ContractID, Contract: "HT-RACE", ContractVersion: "1", Name: "并发项目", Customer: "客户", SupplementStatus: "NONE", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := tx.(*Repository).db.Create(&row).Error; err != nil {
			return domain.Project{}, err
		}
		return projectFromRecord(row), nil
	}
	type outcome struct {
		receipt domain.ContractImportBindingReceipt
		err     error
	}
	results := make(chan outcome, 4)
	for i := 0; i < 4; i++ {
		go func() {
			receipt, err := NewRepository(db).BindContractImport(context.Background(), input, "digest", true, callback)
			results <- outcome{receipt, err}
		}()
	}
	for i := 0; i < 4; i++ {
		result := <-results
		if result.err != nil || result.receipt.ProjectID != projectID {
			t.Fatalf("concurrent receipt=%+v err=%v", result.receipt, result.err)
		}
	}
	if creates.Load() != 1 {
		t.Fatalf("create callbacks=%d", creates.Load())
	}
}
