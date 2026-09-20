package httpapi_test

// 字段级权限「角色多选」在真实路由 + 真实 MySQL 下的行为基线。
//
// 界面契约：角色下拉的选项来自 /api/v1/role-catalog（服务端角色目录）；因为服务端按
// role_code 与主体角色精确比对（field_permission.go 的 principalHasRole），一次多选 N 个
// 角色必须落成 N 条规则，每条只含一个规范角色码，并分别对各自角色生效。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"io"
	"log/slog"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestFieldPermissionRoleMultiSelectEndToEnd(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise the field-permission role multi-select")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const tenant = "PM-FIELD-PERM-TENANT"
	// customer 是白名单内的可隐藏项目字段（maskableProjectFields）：
	// 用报表金额这类不在白名单里的字段名做走查，规则即使写入也永远不会生效。
	const hiddenField = "customer"
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_field_permission WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_service_item WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_project WHERE tenant_id = '` + tenant + `'`,
		} {
			db.WithContext(ctx).Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_project (id, tenant_id, name, customer, contract, contract_version, supplement_status, services, status, created_at, updated_at)
		VALUES ('PJ-FPERM-001', '` + tenant + `', '字段权限走查项目', '走查客户原名', 'C-FPERM-001', 'v1', 'NONE', 1, '待拆解确认', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed 项目失败: %v", err)
	}
	if err := db.WithContext(ctx).Exec(`INSERT INTO pm_service_item (id, tenant_id, project_id, source_service_id, requirement, test_mode, team_lead_id, project_manager_id, engineer_ids, status, report_status, conflict_status, created_at, updated_at)
		VALUES ('SI-FPERM-001', '` + tenant + `', 'PJ-FPERM-001', 'S1', 'r', 'STANDARD', 'team_lead', 'project_manager', JSON_ARRAY('engineer'), '待确认', 'NONE', 'UNCHECKED', NOW(3), NOW(3))`).Error; err != nil {
		t.Fatalf("seed 服务项失败: %v", err)
	}

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	customerFor := func(t *testing.T, role string) string {
		t.Helper()
		status, body := e2eCall(handler, role, http.MethodGet, "/api/v1/projects", "")
		if status != http.StatusOK {
			t.Fatalf("角色 %s 读取项目失败: HTTP %d %s", role, status, body)
		}
		var page struct {
			Data struct {
				Items []struct {
					ID       string `json:"id"`
					Customer string `json:"customer"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatalf("解析项目列表失败: %v (%s)", err, body)
		}
		for _, item := range page.Data.Items {
			if item.ID == "PJ-FPERM-001" {
				return item.Customer
			}
		}
		t.Fatalf("项目列表里没有走查项目: %s", body)
		return ""
	}

	// 1) 角色目录：配置弹窗的角色下拉只允许选目录内的角色码。
	status, body := e2eCall(handler, "admin", http.MethodGet, "/api/v1/role-catalog", "")
	if status != http.StatusOK {
		t.Fatalf("角色目录必须可读: HTTP %d %s", status, body)
	}
	var catalog struct {
		Data struct {
			Roles []struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"roles"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &catalog); err != nil {
		t.Fatalf("解析角色目录失败: %v (%s)", err, body)
	}
	known := map[string]string{}
	for _, role := range catalog.Data.Roles {
		known[role.Code] = role.Name
	}
	for _, code := range []string{"project_manager", "team_lead", "engineer", "quality_manager"} {
		if known[code] == "" {
			t.Fatalf("角色目录缺少 %q: %s", code, body)
		}
	}
	t.Logf("角色目录 %d 项，例如 project_manager=%s", len(known), known["project_manager"])

	// 2) 多选三个角色（界面在提交时逐个展开），角色码精确落库。
	selected := []string{"project_manager", "team_lead", "engineer"}
	for _, roleCode := range selected {
		payload := `{"kind":"permissions","name":"走查字段权限-多选","role_code":"` + roleCode + `","field_name":"` + hiddenField + `","access_level":"hidden","enabled":true}`
		status, response := e2eCall(handler, "admin", http.MethodPost, "/api/v1/rules", payload)
		if status < 200 || status > 299 {
			t.Fatalf("角色 %s 建规则失败: HTTP %d %s", roleCode, status, response)
		}
	}
	var rows []string
	if err := db.WithContext(ctx).Raw("SELECT role_code FROM pm_field_permission WHERE tenant_id = ? ORDER BY role_code", tenant).Scan(&rows).Error; err != nil {
		t.Fatalf("读取字段级权限规则失败: %v", err)
	}
	if len(rows) != len(selected) {
		t.Fatalf("多选 %d 个角色应落 %d 条规则，实际 %d 条：%v", len(selected), len(selected), len(rows), rows)
	}
	for index, roleCode := range []string{"engineer", "project_manager", "team_lead"} {
		if rows[index] != roleCode {
			t.Fatalf("第 %d 条角色码 = %q，期望 %q（全部：%v）", index+1, rows[index], roleCode, rows)
		}
	}

	// 3) 逐个角色验证生效面：选中的角色字段被脱敏，未选中的角色照常可见。
	for _, roleCode := range selected {
		if got := customerFor(t, roleCode); got != "***" {
			t.Fatalf("角色 %s 应命中 customer 隐藏规则，实际读到 %q", roleCode, got)
		}
	}
	if got := customerFor(t, "quality_manager"); got != "走查客户原名" {
		t.Fatalf("未选中的角色不应命中规则，实际读到 %q", got)
	}
}
