package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type contractImportHTTPDirectory struct{}

// List 返回专用测试负责人的受控目录投影。
// 参数：ctx 为请求，q 为精确用户查询。
// 返回值：有效负责人或空目录，不模拟平台真实授权验收。
func (contractImportHTTPDirectory) List(ctx context.Context, q platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if q.UserID != "owner" {
		return platform.OwnerDirectoryPage{}, nil
	}
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: "owner", DisplayName: "项目负责人"}}, Total: 1}, nil
}

// importHTTPMySQLFixture 创建显式隔离连接并注册专用租户数据清理。
// 参数：t 为测试上下文；环境必须提供私有测试DSN文件。
// 返回值：MySQL连接及本测试的租户标识。
func importHTTPMySQLFixture(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := os.Getenv("PM_SNAPSHOT_TEST_DSN_FILE")
	if path == "" {
		t.Skip("set dedicated PM_SNAPSHOT_TEST_DSN_FILE for isolated import HTTP verification")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private regular test DSN file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read test DSN")
	}
	dsn := strings.TrimSpace(string(raw))
	parsed, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(parsed.DBName, "_test") || parsed.Net != "tcp" || !strings.HasPrefix(parsed.Addr, "127.0.0.1:") {
		t.Fatal("isolated loopback _test schema required")
	}
	if err := migration.Run(context.Background(), dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated database")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	tenant := ulid.Make().String()
	t.Cleanup(func() {
		for _, table := range []string{"pm_contract_import_binding", "pm_notification_outbox", "pm_delivery_effects_outbox", "pm_delivery_event", "prj_audit_outbox", "pm_service_item", "pm_project"} {
			if err := db.Table(table).Where("tenant_id=?", tenant).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
	})
	return db, tenant
}

// TestImportProjectBindingHTTPMySQL 贯通机器HTTP、应用拆解、真实事务和关闭恢复。
// 参数：t 为测试上下文；环境须显式提供私有隔离测试DSN文件。
// 返回值：无。
func TestImportProjectBindingHTTPMySQL(t *testing.T) {
	db, tenant := importHTTPMySQLFixture(t)
	service := &application.Service{Repo: store.NewRepository(db), Personnel: contractImportHTTPDirectory{}}
	verifier := &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: tenant, ApplicationCode: "contract_management"}}
	makeRouter := func(enabled bool) http.Handler {
		return httpapi.NewRouter(service, identity{}, nil, nil, httpapi.RouterOptions{ContractImport: &httpapi.ContractImportOptions{Enabled: enabled, BearerVerifier: verifier}})
	}
	enabled, disabled := makeRouter(true), makeRouter(false)
	command := domain.ContractImportBindingCommand{SchemaVersion: 1, CommandID: strings.Repeat("a", 32), TenantID: tenant, ContractID: "C-HTTP", ContractVersion: "1", OriginImportItemID: "http-item", SourceRevision: 1, PolicyDigest: strings.Repeat("b", 64), ReleaseVersion: 1, ImportMode: "CONTINUE_LIVE", ProjectMode: "CREATE_NEW", Relationship: "PRIMARY", TargetOwnerUserID: "owner", Contract: domain.ContractActivation{ContractID: "C-HTTP", ContractVersion: "1", ContractNumber: "HT-HTTP", Customer: "测试客户", CustomerID: "customer", EffectiveAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Services: []domain.ContractService{{SourceID: "S1", Site: "测试站点", Batch: "第一期", Category: "检测", TestMode: "STANDARD"}}}}
	call := func(router http.Handler, method, path string, input any) *httptest.ResponseRecorder {
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer trusted-test-token")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	created := call(enabled, http.MethodPost, "/internal/v1/contract-imports/project-bindings", command)
	var response struct {
		Data domain.ContractImportBindingReceipt `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	receipt := response.Data
	if created.Code != 200 || receipt.Status != "PROJECT_BOUND" || receipt.ProjectID == "" || receipt.ProjectVersion != 1 || receipt.CommandID != command.CommandID || receipt.TenantID != tenant || receipt.ContractID != command.ContractID || receipt.OriginImportItemID != command.OriginImportItemID || receipt.SchemaVersion != 1 || receipt.SourceRevision != 1 || receipt.ReleaseVersion != 1 || receipt.PolicyDigest != command.PolicyDigest {
		t.Fatalf("created=%d %s", created.Code, created.Body)
	}
	for _, request := range []struct {
		method, path string
		input        any
	}{{http.MethodPost, "/internal/v1/contract-imports/project-bindings", command}, {http.MethodGet, "/internal/v1/contract-imports/project-bindings/http-item", nil}} {
		replay := call(disabled, request.method, request.path, request.input)
		var result struct {
			Data domain.ContractImportBindingReceipt `json:"data"`
		}
		if err := json.Unmarshal(replay.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if replay.Code != 200 || result.Data != receipt {
			t.Fatalf("recovery=%d %s", replay.Code, replay.Body)
		}
	}
	for _, scenario := range []string{"changed-content", "disabled-new", "wrong-tenant", "none", "machine-owner"} {
		input := command
		router := enabled
		want := 409
		switch scenario {
		case "changed-content":
			input.Contract.Customer = "其他内容"
		case "disabled-new":
			input.CommandID = strings.Repeat("c", 32)
			router = disabled
		case "wrong-tenant":
			input.TenantID = "other"
			want = 403
		case "none":
			input.ProjectMode = "NONE"
			want = 422
		case "machine-owner":
			input.CommandID = strings.Repeat("d", 32)
			input.TargetOwnerUserID = "contract_management"
			want = 422
		}
		result := call(router, http.MethodPost, "/internal/v1/contract-imports/project-bindings", input)
		if result.Code != want {
			t.Fatalf("%s=%d %s", scenario, result.Code, result.Body)
		}
	}
	var projects, items int64
	if err := db.Table("pm_project").Where("tenant_id=?", tenant).Count(&projects).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("pm_service_item").Where("tenant_id=? AND status=?", tenant, "待确认").Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if projects != 1 || items != 1 {
		t.Fatalf("projects=%d pending services=%d", projects, items)
	}
	verifier.identity.TenantID = tenant + "-other"
	hidden := call(disabled, http.MethodGet, "/internal/v1/contract-imports/project-bindings/http-item", nil)
	if hidden.Code != 404 {
		t.Fatalf("cross tenant query=%d %s", hidden.Code, hidden.Body)
	}
}
