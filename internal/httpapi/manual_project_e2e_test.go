package httpapi_test

// 手动创建项目必须停在「待拆解确认」，不能被拆解规则直接放行到「待分配」。
//
// 现场现象：业务管理员在「项目列表」新建项目后，项目状态直接是「待分配」，从未出现在
// 「服务项拆解确认」里。根因链：
//  1. CreateProjectWithServiceItems 对手动清单同样套用 splitRuleItemStatus：只要租户存在
//     启用中的拆解规则且服务项的 批次/站点/类别 未命中规则范围，就返回「待分配」；
//  2. 「服务项拆解确认」页只列 待确认/待复核 的项，被自动放行的项再也进不去确认流程；
//  3. 确认拆解才是把特殊方法项置为 tech_review_status=PENDING 的唯一入口（ConfirmServiceItems），
//     于是自动放行的渗透测试项既不能复核（复核要求 PENDING/REJECTED）、也不能发布实施计划
//     （要求 APPROVED）——项目永久卡死。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	store "github.com/j-s-te/project-management/internal/infrastructure/mysql"
	"io"
	"log/slog"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestManualProjectCreationWaitsForDecompositionConfirmation(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise manual project creation")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const tenant = "PM-MANUAL-DECOMP-TENANT"
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_split_rule WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_service_item WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_project WHERE tenant_id = '` + tenant + `'`,
		} {
			db.WithContext(ctx).Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Contracts: approvedContractVerifier{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 租户里有一条启用中的拆解规则，适用范围与手动清单的任何字段都不相干
	//（现场就是这种配置：规则写着「销售团队」，而服务项只有批次/站点/类别）。
	status, body := e2eCall(handler, "admin", http.MethodPost, "/api/v1/rules",
		`{"kind":"split-rules","name":"走查拆解规则","scope":"销售团队","enabled":true}`)
	if status < 200 || status > 299 {
		t.Fatalf("建拆解规则失败: HTTP %d %s", status, body)
	}

	// 业务管理员手动创建项目：一条常规项 + 一条渗透测试（特殊方法）项。
	payload := `{"name":"走查手动项目","contract_id":"approved-manual-1","service_items":[` +
		`{"source_id":"SVC-1","site":"杭州机房"},` +
		`{"source_id":"SVC-2","site":"杭州机房"}]}`
	status, body = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/projects", payload)
	if status < 200 || status > 299 {
		t.Fatalf("创建项目失败: HTTP %d %s", status, body)
	}
	var created struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v (%s)", err, body)
	}

	type row struct {
		ID, Status, TestMode, TechReviewStatus string
	}
	var rows []row
	if err := db.WithContext(ctx).Raw(
		`SELECT id, status, test_mode, tech_review_status FROM pm_service_item WHERE tenant_id = ? ORDER BY id`, tenant).
		Scan(&rows).Error; err != nil {
		t.Fatalf("读取服务项失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应写入 2 条服务项，实际 %d", len(rows))
	}
	for _, r := range rows {
		t.Logf("服务项 %s：状态=%s 测试模式=%s 技术复核=%q", r.ID, r.Status, r.TestMode, r.TechReviewStatus)
	}
	t.Logf("项目 %s 状态=%s", created.Data.ID, created.Data.Status)

	// 手动清单必须先进入拆解确认：项目与全部服务项都停在待确认。
	for _, r := range rows {
		if r.Status != "待确认" {
			t.Fatalf("服务项 %s 状态=%q，期望「待确认」（手动创建必须走拆解确认）", r.ID, r.Status)
		}
	}
	if created.Data.Status != "待拆解确认" {
		t.Fatalf("项目状态=%q，期望「待拆解确认」", created.Data.Status)
	}

	// 拆解确认页面的数据源：确认后项才会离开该列表。
	status, body = e2eCall(handler, "business_admin", http.MethodGet, "/api/v1/service-items", "")
	if status != http.StatusOK {
		t.Fatalf("读取服务项列表失败: HTTP %d %s", status, body)
	}
	for _, r := range rows {
		if !containsString(body, r.ID) {
			t.Fatalf("服务项 %s 不在拆解确认数据源里: %s", r.ID, body)
		}
	}

	// 特殊方法项的复核入口不能被绕过：确认拆解后进入技术总监复核窗口。
	status, body = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/service-items/confirm",
		`{"ids":["`+rows[1].ID+`"]}`)
	if status < 200 || status > 299 {
		t.Fatalf("确认拆解失败: HTTP %d %s", status, body)
	}
	var techReview string
	if err := db.WithContext(ctx).Raw(`SELECT tech_review_status FROM pm_service_item WHERE tenant_id = ? AND id = ?`, tenant, rows[1].ID).
		Scan(&techReview).Error; err != nil {
		t.Fatalf("读取复核状态失败: %v", err)
	}
	if techReview != "PENDING" {
		t.Fatalf("渗透测试项确认拆解后 tech_review_status=%q，期望 PENDING（否则技术总监无法复核）", techReview)
	}
	// 复核 → 通过 → 才能发布实施计划；这条链路必须可达。
	status, body = e2eCall(handler, "technical_director", http.MethodPost,
		"/api/v1/service-items/"+rows[1].ID+"/special-method-review", `{"decision":"APPROVED","comment":"走查通过"}`)
	if status < 200 || status > 299 {
		t.Fatalf("技术总监复核失败: HTTP %d %s", status, body)
	}
}

// containsString 判断响应体里是否出现该标识（列表接口未分页时按子串断言足够）。
func containsString(haystack, needle string) bool {
	return needle != "" && strings.Contains(haystack, needle)
}
