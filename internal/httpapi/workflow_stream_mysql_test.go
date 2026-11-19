package httpapi_test

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	dbmysql "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"github.com/j-s-te/project-management/internal/migration"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/migrations"
	"github.com/oklog/ulid/v2"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestWorkflowStreamPlanAdvanceMySQL 通过真实 MySQL 与 HTTP 验证经理操作后的领导订阅。
// 参数：t 为测试句柄，数据库由 PM_WORKFLOW_TEST_DSN 指向本机隔离容器。
// 返回值：无。
func TestWorkflowStreamPlanAdvanceMySQL(t *testing.T) {
	db := openWorkflowTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	project, item := seedWorkflowPlan(t, db)
	repository := dbmysql.NewRepository(db)
	handler := httpapi.NewRouter(&application.Service{Repo: repository}, switchIdentity{}, nil, slog.Default())
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+workflowStreamPath+"?project_id="+project, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-E2E-Role", "technical_director")
	res, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("open stream: HTTP %d", res.StatusCode)
	}
	reader := bufio.NewReader(res.Body)
	initial := readWorkflowFrame(t, reader, "ready")

	outside := platform.ScopeFilter{TenantID: e2eTenant, ProjectIDs: []string{"not-visible"}}
	outsideBefore, err := repository.WorkflowVersion(ctx, outside, project)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/service-items/" + item + "/implementation-plan"
	payload := `{"expected_version":1,"planned_start":"2026-11-20T09:00:00Z","planned_end":"2026-11-21T18:00:00Z",
		"site_plan":"隔离实施计划","personnel":[{"resource_type":"PERSON","resource_id":"` + item + `"}]}`
	statusCode, result := e2eCall(handler, "project_manager", http.MethodPost, path, payload)
	observed := time.Now()
	if statusCode != http.StatusOK {
		t.Fatalf("publish plan: HTTP %d %s", statusCode, result)
	}
	if changed := readWorkflowFrame(t, reader, "workflow-changed"); changed == initial {
		t.Fatal("committed plan did not change the workflow revision")
	}
	t.Logf("manager HTTP response to leader SSE frame: %s", time.Since(observed).Round(time.Millisecond))

	if detail := e2eStep(t, handler, "technical_director", http.MethodGet, "/api/v1/projects/"+project, ""); !strings.Contains(detail, `"status":"待实施"`) {
		t.Fatalf("leader project status stayed stale: %s", detail)
	}
	outsideAfter, err := repository.WorkflowVersion(ctx, outside, project)
	if err != nil || outsideBefore != outsideAfter {
		t.Fatalf("outside scope observed the change: %v", err)
	}
	scope := platform.ScopeFilter{TenantID: e2eTenant, AllowAll: true}
	beforeReject, err := repository.WorkflowVersion(ctx, scope, project)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e2eCall(handler, "project_manager", http.MethodPost, path, payload); code != http.StatusConflict {
		t.Fatalf("stale duplicate command got HTTP %d, want 409", code)
	}
	afterReject, err := repository.WorkflowVersion(ctx, scope, project)
	if err != nil || beforeReject != afterReject {
		t.Fatalf("rejected command advanced a workflow version: %v", err)
	}
}

// openWorkflowTestDB 仅连接明确命名的本机隔离库并应用仓库正式迁移。
// 参数：t 为测试句柄。
// 返回值：测试专用 MySQL 连接。
func openWorkflowTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PM_WORKFLOW_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_WORKFLOW_TEST_DSN to a disposable local workflow_isolated database")
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "workflow_isolated" {
		t.Fatal("workflow tests require a local disposable workflow_isolated database")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("workflow tests require IPv4 loopback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err = migration.Run(ctx, dsn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedWorkflowPlan 建立经理已经受指派且可提交实施计划的单服务项项目。
// 参数：t 为测试句柄，db 为已经迁移的隔离数据库。
// 返回值：项目及服务项标识。
func seedWorkflowPlan(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	project, item := ulid.Make().String(), ulid.Make().String()
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO pm_project (id,tenant_id,name,customer,contract,contract_version,supplement_status,services,status,created_at,updated_at)
 VALUES (?,?,'实时同步验证','隔离客户',?,'v1','NONE',1,'待分配',NOW(3),NOW(3))`, []any{project, e2eTenant, project}},
		{`INSERT INTO pm_service_item (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,conflict_status,project_manager_id,created_at,updated_at)
 VALUES (?,?,?,'S1','测试','STANDARD','待分配','NONE','PASSED','project_manager',NOW(3),NOW(3))`, []any{item, e2eTenant, project}},
		{`INSERT INTO pm_capability (id,tenant_id,resource_type,resource_id,resource_name,capability_codes,status,usage_scope,
 user_id,identity_status,identity_checked_at,updated_at,updated_by)
 VALUES (?,?,'PERSON',?,'隔离工程师',JSON_ARRAY('TPC-TEST'),'ACTIVE','ANY','engineer','ACTIVE',NOW(3),NOW(3),'seed')`, []any{item, e2eTenant, item}},
	}
	for _, seed := range seeds {
		if err := db.Exec(seed.query, seed.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	return project, item
}

// TestWorkflowVersionPublishesOnlyVisibleFacts 验证受指派角色可见的项目汇总和附属状态。
// 参数：t 为隔离 MySQL 测试上下文。
// 返回值：无。
func TestWorkflowVersionPublishesOnlyVisibleFacts(t *testing.T) {
	db := openWorkflowTestDB(t)
	project, assigned := seedWorkflowPlan(t, db)
	other := ulid.Make().String()
	runWorkflowFixtureSQL(t, db, `INSERT INTO pm_service_item
 (id,tenant_id,project_id,source_service_id,requirement,test_mode,status,report_status,created_at,updated_at)
 VALUES (?,?,?,'S2','隔离','STANDARD','现场实施完成','RECTIFYING',NOW(3),NOW(3))`, other, e2eTenant, project)
	runWorkflowFixtureSQL(t, db, `UPDATE pm_service_item SET status='现场实施完成',report_status='ISSUED'
 WHERE tenant_id=? AND id=?`, e2eTenant, assigned)
	repository := dbmysql.NewRepository(db)
	filter := platform.ScopeFilter{TenantID: e2eTenant, UserID: "project_manager", AssignedItemsOnly: true}
	version := func() string {
		t.Helper()
		value, err := repository.WorkflowVersion(t.Context(), filter, project)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := version()
	runWorkflowFixtureSQL(t, db, "UPDATE pm_service_item SET version=version+1 WHERE id=?", other)
	if version() != before {
		t.Fatal("unassigned item revision leaked into the assigned scope")
	}
	runWorkflowFixtureSQL(t, db, "UPDATE pm_service_item SET report_status='COMPILING' WHERE id=?", other)
	if version() == before {
		t.Fatal("authorized project report stage change did not invalidate the assigned scope")
	}
	runWorkflowFixtureSQL(t, db, `INSERT INTO pm_penetration_work_package
 (id,tenant_id,project_id,parent_service_item_id,auth_scope,test_scope,rollback_plan,created_at,updated_at,updated_by)
 VALUES (?,?,?,?,'','','',NOW(3),NOW(3),'seed')`, ulid.Make().String(), e2eTenant, project, assigned)
	before = version()
	runWorkflowFixtureSQL(t, db, `UPDATE pm_penetration_work_package SET version=version+1
 WHERE tenant_id=? AND parent_service_item_id=?`, e2eTenant, assigned)
	if version() == before {
		t.Fatal("visible embedded workflow revision did not invalidate the parent view")
	}
}

// runWorkflowFixtureSQL 执行隔离夹具的单条 SQL。
// 参数：t、db 为测试上下文与隔离数据库，query、args 为固定测试操作及参数。
// 返回值：无；失败会中止测试。
func runWorkflowFixtureSQL(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatal(err)
	}
}
