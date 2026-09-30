package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	gormlogger "gorm.io/gorm/logger"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// AUD-2026-028：分页与计数必须下推到 SQL。本文件用 DryRun + SQL 捕获日志断言
// 查询形状（COUNT / LIMIT / OFFSET / 排序 / 派生状态 JOIN）；筛选与分页的**语义
// 等价**（结果集、total、排序、派生状态逐行一致）由 project_page_equivalence_
// integration_test.go 用真实 MySQL（PM_TEST_DSN）验证。

type sqlCapture struct{ queries []string }

// captureLogger 把 DryRun 生成的每条 SQL 记录下来，用于形状断言。
type captureLogger struct{ capture *sqlCapture }

func (l captureLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }
func (l captureLogger) Info(context.Context, string, ...any)             {}
func (l captureLogger) Warn(context.Context, string, ...any)             {}
func (l captureLogger) Error(context.Context, string, ...any)            {}
func (l captureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if err != nil {
		return
	}
	sql, _ := fc()
	l.capture.queries = append(l.capture.queries, sql)
}

func captureDB(t *testing.T) (*gorm.DB, *sqlCapture) {
	t.Helper()
	capture := &sqlCapture{}
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		DSN: "project:secret@tcp(127.0.0.1:3306)/project_management", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: captureLogger{capture: capture}})
	if err != nil {
		t.Fatal(err)
	}
	return db, capture
}

func TestListProjectsPushesCountAndPaginationIntoSQL(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListProjects(context.Background(), filter, "", "", 3, 25); err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected exactly one COUNT and one page query, got %d: %v", len(capture.queries), capture.queries)
	}
	countSQL, pageSQL := capture.queries[0], capture.queries[1]
	if !strings.Contains(countSQL, "count(") {
		t.Fatalf("first query must be a COUNT push-down: %s", countSQL)
	}
	if strings.Contains(countSQL, "LIMIT") || strings.Contains(countSQL, "OFFSET") {
		t.Fatalf("count query must not be paginated: %s", countSQL)
	}
	for _, expected := range []string{"ORDER BY id DESC", "LIMIT", "OFFSET"} {
		if !strings.Contains(pageSQL, expected) {
			t.Fatalf("page query missing %q: %s", expected, pageSQL)
		}
	}
	if !strings.Contains(pageSQL, "LIMIT 25") || !strings.Contains(pageSQL, "OFFSET 50") {
		t.Fatalf("page query must compute (page-1)*page_size offset: %s", pageSQL)
	}
}

// 状态过滤走派生状态：SQL 必须带服务项聚合 JOIN 与与 Go 侧同源的等级 CASE，
// 而不是读可能滞后的存储状态列。
func TestListProjectsStatusFilterUsesDerivedStatusPredicateInSQL(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListProjects(context.Background(), filter, "", "已完成", 1, 25); err != nil {
		t.Fatalf("list projects by status: %v", err)
	}
	joined := strings.Join(capture.queries, "\n")
	for _, expected := range []string{
		"LEFT JOIN",
		"pm_service_item AS aggregated",
		"GROUP BY aggregated.project_id",
		"TRIM(aggregated.status) IN ('待确认', '待复核') THEN 0",
		"UPPER(TRIM(COALESCE(aggregated.report_status, ''))) IN ('COMPILING', 'REVIEWED', 'ISSUED') THEN 7",
		"IN ('ARCHIVED') THEN 8",
		"ELT(",
		"WHEN UPPER(TRIM(COALESCE(pm_project.supplement_status, ''))) = 'REQUIRED'",
		"terminated_items = derived_status.total_items",
		domain.ProjectStatusTerminated,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("derived status SQL missing %q:\n%s", expected, joined)
		}
	}
	if strings.Contains(joined, "pm_project.status = ") && !strings.Contains(joined, "CASE") {
		t.Fatalf("status filter must not read the stale stored column:\n%s", joined)
	}
}

// 不分页（pageSize<=0）时不下推 LIMIT/OFFSET，行为与历史全量口径一致。
func TestListProjectsWithoutPaginationKeepsFullQuery(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListProjects(context.Background(), filter, "", "", 0, 0); err != nil {
		t.Fatalf("list projects unpaged: %v", err)
	}
	if len(capture.queries) != 2 {
		t.Fatalf("expected COUNT + full list, got %d: %v", len(capture.queries), capture.queries)
	}
	if strings.Contains(capture.queries[1], "LIMIT") || strings.Contains(capture.queries[1], "OFFSET") {
		t.Fatalf("unpaged query must not carry LIMIT/OFFSET: %s", capture.queries[1])
	}
}

func TestListServiceItemsPushesCountAndPaginationIntoSQL(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListServiceItems(context.Background(), filter, "PJ-1", 2, 50); err != nil {
		t.Fatalf("list service items: %v", err)
	}
	// 空页夹具下只有 COUNT + 本页查询，不应再触发任何全表补查。
	if len(capture.queries) != 2 {
		t.Fatalf("expected COUNT + page query only, got %d: %v", len(capture.queries), capture.queries)
	}
	countSQL, pageSQL := capture.queries[0], capture.queries[1]
	if !strings.Contains(countSQL, "count(") || strings.Contains(countSQL, "LIMIT") {
		t.Fatalf("count query must be a bare push-down COUNT: %s", countSQL)
	}
	for _, expected := range []string{"pm_service_item.tenant_id =", "pm_service_item.project_id IN", "ORDER BY id", "LIMIT 50", "OFFSET 50"} {
		if !strings.Contains(pageSQL, expected) {
			t.Fatalf("service item page query missing %q: %s", expected, pageSQL)
		}
	}
}

// 不分页（pageSize<=0）时保持全量口径：不下推 LIMIT，随后按本页 ID 集合补查计划/包。
func TestListServiceItemsWithoutPaginationKeepsFullQueryAndLookups(t *testing.T) {
	db, capture := captureDB(t)
	repository := NewRepository(db)
	filter := platform.ScopeFilter{TenantID: "tenant-1", AllowAll: true}

	if _, _, err := repository.ListServiceItems(context.Background(), filter, "", 0, 0); err != nil {
		t.Fatalf("list service items unpaged: %v", err)
	}
	if strings.Contains(capture.queries[1], "LIMIT") || strings.Contains(capture.queries[1], "OFFSET") {
		t.Fatalf("unpaged query must not carry LIMIT/OFFSET: %s", capture.queries[1])
	}
}
