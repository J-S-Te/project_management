package mysql

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	driver "github.com/go-sql-driver/mysql"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type expenseManagementIdentity struct{ principal platform.Principal }

// Authenticate 提供隔离 HTTP 集成测试的明确人员身份，不复用真实会话。
// 参数：ctx/request 为测试请求上下文。
// 返回值：测试定义的身份；生产认证链路不受影响。
func (i expenseManagementIdentity) Authenticate(
	ctx context.Context, request *http.Request,
) (platform.Principal, error) {
	return i.principal, nil
}

// expenseImplementationMySQL 仅连接本场景显式指定的专用测试库并创建最小表结构。
// 参数：t 为测试上下文，DSN 通过 PM_EXPENSE_IMPLEMENTATION_TEST_DSN 显式注入。
// 返回值：隔离仓储；无 DSN 时跳过，非专用库或初始化失败时终止。
func expenseImplementationMySQL(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("PM_EXPENSE_IMPLEMENTATION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires dedicated PM_EXPENSE_IMPLEMENTATION_TEST_DSN")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || config.DBName != "pm_expense_implementation_test" {
		t.Fatal("requires dedicated pm_expense_implementation_test database")
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err = db.AutoMigrate(&projectRecord{}, &serviceItemRecord{}, &implPlanRecord{},
		&capabilityRecord{}, &deliveryEventRecord{}, &penetrationWorkPackageRecord{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Table("pm_expense_participant").AutoMigrate(&expenseParticipantRow{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("隔离夹具回滚失败：%v", err)
		}
	})
	if err = tx.Create(&projectRecord{ID: "p", TenantID: "t", Name: "隔离测试项目",
		Status: domain.ProjectStatusReporting, Version: 1}).Error; err != nil {
		t.Fatal(err)
	}
	r := NewRepository(tx)
	seedExpenseImplementation(t, r)
	return r
}

// TestExpenseImplementationDefaultOnMySQL 验收实施人员无需人工授权即可引用本服务项。
// 参数：t 为测试上下文；仅在专用 MySQL 上运行这一最小业务场景。
// 返回值：无；真实 SQL、默认名单接口、搜索和批验任一步不符时失败。
func TestExpenseImplementationDefaultOnMySQL(t *testing.T) {
	r := expenseImplementationMySQL(t)
	s := &application.Service{Repo: r, Personnel: expenseImplementationDirectory{}}
	identity := expenseManagementIdentity{platform.Principal{TenantID: "t", UserID: "manager",
		IdentityID: "manager", Roles: []string{"project_manager"},
		Permissions: map[string]bool{"project.expense_participant.manage": true},
		DataScopes:  []platform.DataScope{{ScopeType: "APPLICATION"}}}}
	router := httpapi.NewRouter(s, identity, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/p/expense-participants?page_size=20", nil))
	var result struct {
		Data struct {
			Items          []domain.ExpenseParticipant                 `json:"items"`
			Implementation domain.ExpenseImplementationParticipantPage `json:"implementation_participants"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
		len(result.Data.Items) != 0 || len(result.Data.Implementation.Items) != 1 ||
		result.Data.Implementation.Items[0].SubjectID != "implementer" {
		t.Fatalf("默认名单接口错误：%d %s", response.Code, response.Body.String())
	}
	page, err := s.SearchExpenseReferences(context.Background(), "t", application.ExpenseSearchInput{
		ActorSubjectID: "implementer", PageSize: 20,
	})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Eligible ||
		page.Items[0].ServiceItemID != "a" || page.Items[0].EligibilitySource != "IMPLEMENTATION_PERSONNEL" {
		t.Fatalf("实施人员引用未生效：%+v，%v", page, err)
	}
	validated, err := s.BatchValidateExpenseReferences(context.Background(), "t",
		application.ExpenseBatchInput{ActorSubjectID: "implementer", ClaimID: "qa-claim", DraftVersion: 1,
			References: []application.ExpenseReferenceInput{
				{ProjectID: "p", ServiceItemID: "a", OccurredDate: "2026-11-13"},
			}})
	if err != nil || len(validated.Items) != 1 || !validated.Items[0].Eligible {
		t.Fatalf("提交批验与搜索不一致：%+v，%v", validated, err)
	}
	t.Log("PASS: 当前计划人员显示为默认资格，搜索与提交批验可用，人工授权仍为空")
}
