package httpapi_test

// 合同拆解规则配置 v2（原型 PG-CFG-01）的行为基线：
// 三块配置的读写 + 合同激活时按配置分组/定状态/补体系要求/缺规则标记 + 范围变更检测。
//
// 与旧实现最关键的差异：分组规则**未命中**不再自动放行到「待分配」，而是按原型的
// 「分组规则缺失时」口径默认标记「待人工确认」并通知业务管理员。

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestSplitRuleConfigEndToEnd(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise the split rule configuration")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const tenant = "PM-SPLIT-CONFIG-TENANT"
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_split_override WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_split_policy WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_detection_category WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_service_item WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_project WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_delivery_event WHERE tenant_id = '` + tenant + `'`,
		} {
			db.WithContext(ctx).Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// ---- ① 默认分组规则：未配置时返回原型默认值 ----
	status, body := e2eCall(handler, "admin", http.MethodGet, "/api/v1/split-policy", "")
	if status != http.StatusOK {
		t.Fatalf("读取默认分组规则失败: HTTP %d %s", status, body)
	}
	var policy struct {
		Data struct {
			DimensionPrimary   string `json:"dimension_primary"`
			DimensionSecondary string `json:"dimension_secondary"`
			DefaultStatus      string `json:"default_status"`
			MissingRuleAction  string `json:"missing_rule_action"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &policy); err != nil {
		t.Fatalf("解析默认分组规则失败: %v (%s)", err, body)
	}
	if policy.Data.DimensionPrimary != "batch" || policy.Data.DimensionSecondary != "category" ||
		policy.Data.DefaultStatus != "待确认" || policy.Data.MissingRuleAction != "HUMAN_CONFIRM" {
		t.Fatalf("默认分组规则应来自原型默认值，实际 %+v", policy.Data)
	}

	// 保存一份自定义规则：默认进入状态=待分配（跳过确认），缺规则=按默认规则生成。
	status, body = e2eCall(handler, "admin", http.MethodPut, "/api/v1/split-policy",
		`{"dimension_primary":"batch","dimension_secondary":"category","default_status":"待分配","generate_requirement_summary":true,"requirement_summary_locked":false,"missing_rule_action":"DEFAULT_RULE","scope_change_detection":true,"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("保存默认分组规则失败: HTTP %d %s", status, body)
	}
	// 维度配置非法必须被拒绝：分组维度 1 与 2 相同会让分组退化。
	status, body = e2eCall(handler, "admin", http.MethodPut, "/api/v1/split-policy",
		`{"dimension_primary":"batch","dimension_secondary":"batch","default_status":"待确认","missing_rule_action":"HUMAN_CONFIRM"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("非法分组维度应 422，实际 HTTP %d %s", status, body)
	}

	// ---- ② 检测类别域：首次读取落原型初始域 ----
	status, body = e2eCall(handler, "admin", http.MethodGet, "/api/v1/detection-categories", "")
	if status != http.StatusOK {
		t.Fatalf("读取检测类别域失败: HTTP %d %s", status, body)
	}
	var categories struct {
		Data struct {
			Items []struct {
				Category       string `json:"category"`
				SystemStandard string `json:"system_standard"`
				SpecialMethod  string `json:"special_method"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &categories); err != nil {
		t.Fatalf("解析检测类别域失败: %v (%s)", err, body)
	}
	if categories.Data.Total < 17 {
		t.Fatalf("检测类别域初始应有原型的 17 类，实际 %d", categories.Data.Total)
	}
	byCategory := map[string]string{}
	for _, item := range categories.Data.Items {
		byCategory[item.Category] = item.SpecialMethod
	}
	if byCategory["渗透测试"] != "REQUIRED" || byCategory["等保测评"] != "NO" {
		t.Fatalf("特殊方法口径不符：%+v", byCategory)
	}

	// ---- ③ 合同激活：按配置分组 + 定状态 + 补体系要求 ----
	activation := `{"contract_id":"HT-SPLIT-1","contract_version":"v1","contract_name":"拆解走查合同","customer":"走查客户","effective_at":"2026-09-14T00:00:00Z",
		"services":[
			{"source_id":"L1","site":"杭州机房","batch":"B1","category":"等保测评","requirement":"条款A"},
			{"source_id":"L2","site":"杭州机房","batch":"B1","category":"等保测评","requirement":"条款B"},
			{"source_id":"L3","site":"上海机房","batch":"B2","category":"渗透测试"}
		]}`
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/contracts/activate", activation)
	if status != http.StatusCreated {
		t.Fatalf("合同激活失败: HTTP %d %s", status, body)
	}
	type itemRow struct {
		ID             string
		Batch          string
		Category       string
		SystemStandard string
		Status         string
		TestMode       string
		Special        string
		TechReview     string
	}
	var rows []itemRow
	if err := db.WithContext(ctx).Raw(
		`SELECT id, batch, category, system_standard, status, test_mode, special, tech_review_status AS tech_review
		 FROM pm_service_item WHERE tenant_id = ? ORDER BY id`, tenant).Scan(&rows).Error; err != nil {
		t.Fatalf("读取服务项失败: %v", err)
	}
	for _, row := range rows {
		t.Logf("服务项 %s：批次=%s 类别=%s 体系要求=%q 状态=%s 模式=%s 特殊=%s 复核=%q",
			row.ID, row.Batch, row.Category, row.SystemStandard, row.Status, row.TestMode, row.Special, row.TechReview)
	}
	// 默认维度「批次 + 检测类别」：B1 + 等保测评 的两行合并为 1 条，B2 + 渗透测试 另 1 条。
	if len(rows) != 2 {
		t.Fatalf("按批次 + 检测类别分组应得到 2 条服务项，实际 %d 条", len(rows))
	}
	if rows[0].SystemStandard != "等保 2.0" {
		t.Fatalf("体系要求应取检测类别域默认值，实际 %q", rows[0].SystemStandard)
	}
	// 本租户把默认进入状态配成了「待分配」：常规项直接待分配，特殊方法项仍需复核窗口。
	if rows[0].Status != "待分配" || rows[0].TestMode != "STANDARD" {
		t.Fatalf("常规项应按配置直接待分配，实际 %+v", rows[0])
	}
	if rows[1].TestMode != "PENETRATION" || rows[1].Special != "是" || rows[1].TechReview != "PENDING" {
		t.Fatalf("渗透测试类别必为特殊方法并进复核窗口，实际 %+v", rows[1])
	}

	// ---- ④ 覆盖规则：改分组维度后同一合同的合并结果应随之变化 ----
	// 默认维度「批次 + 检测类别」会把同一批次的跨场所清单行合并；覆盖成「场所 + 批次 + 检测类别」
	// 三维后，跨场所的行必须拆开——这正是原型覆盖规则示例的口径。
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/split-overrides",
		`{"name":"走查按场所分组","match_conditions":{"customer_contains":"走查客户"},"override_settings":{"dimension_primary":"site","dimension_secondary":"batch","dimension_tertiary":"category"},"priority":10,"enabled":true}`)
	if status < 200 || status > 299 {
		t.Fatalf("新建覆盖规则失败: HTTP %d %s", status, body)
	}
	// 空匹配条件/空覆盖设置都必须被拒绝（否则会退化成无意义的全局覆盖）。
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/split-overrides",
		`{"name":"空规则","match_conditions":{},"override_settings":{"default_status":"待确认"},"priority":10,"enabled":true}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("空匹配条件应 422，实际 HTTP %d %s", status, body)
	}
	crossSite := `{"contract_id":"HT-SPLIT-2","contract_version":"v1","contract_name":"跨场所合同","customer":"走查客户","effective_at":"2026-09-14T00:00:00Z",
		"services":[
			{"source_id":"M1","site":"杭州机房","batch":"B1","category":"等保测评"},
			{"source_id":"M2","site":"上海机房","batch":"B1","category":"等保测评"}
		]}`
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/contracts/activate", crossSite)
	if status != http.StatusCreated {
		t.Fatalf("第二次合同激活失败: HTTP %d %s", status, body)
	}
	var overrideRows int64
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM pm_service_item WHERE tenant_id = ? AND project_id IN (SELECT id FROM pm_project WHERE tenant_id = ? AND contract = 'HT-SPLIT-2')`,
		tenant, tenant).Scan(&overrideRows).Error; err != nil {
		t.Fatalf("统计服务项失败: %v", err)
	}
	if overrideRows != 2 {
		t.Fatalf("三维覆盖规则（场所+批次+检测类别）应把跨场所清单拆成 2 条，实际 %d 条", overrideRows)
	}
	// 停用覆盖规则后同样的合同应按默认两维重新合并为 1 条：证明覆盖确实在生效。
	status, body = e2eCall(handler, "admin", http.MethodGet, "/api/v1/split-overrides", "")
	if status != http.StatusOK || !strings.Contains(body, "走查按场所分组") {
		t.Fatalf("读取覆盖规则失败: HTTP %d %s", status, body)
	}
	var overrides struct {
		Data struct {
			Items []struct {
				ID int64 `json:"id"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &overrides); err != nil || len(overrides.Data.Items) == 0 {
		t.Fatalf("解析覆盖规则失败: %v (%s)", err, body)
	}
	status, body = e2eCall(handler, "admin", http.MethodDelete,
		fmt.Sprintf("/api/v1/split-overrides/%d", overrides.Data.Items[0].ID), "")
	if status < 200 || status > 299 {
		t.Fatalf("删除覆盖规则失败: HTTP %d %s", status, body)
	}
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/contracts/activate",
		strings.Replace(crossSite, "HT-SPLIT-2", "HT-SPLIT-2B", 1))
	if status != http.StatusCreated {
		t.Fatalf("第三次合同激活失败: HTTP %d %s", status, body)
	}
	var defaultRows int64
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM pm_service_item WHERE tenant_id = ? AND project_id IN (SELECT id FROM pm_project WHERE tenant_id = ? AND contract = 'HT-SPLIT-2B')`,
		tenant, tenant).Scan(&defaultRows).Error; err != nil {
		t.Fatalf("统计服务项失败: %v", err)
	}
	if defaultRows != 1 {
		t.Fatalf("默认两维（批次 + 检测类别）应把跨场所清单合并为 1 条，实际 %d 条", defaultRows)
	}

	// ---- ⑤ 缺规则：域外类别在 HUMAN_CONFIRM 口径下必须停在待确认 ----
	status, body = e2eCall(handler, "admin", http.MethodPut, "/api/v1/split-policy",
		`{"dimension_primary":"batch","dimension_secondary":"category","default_status":"待分配","generate_requirement_summary":true,"requirement_summary_locked":false,"missing_rule_action":"HUMAN_CONFIRM","scope_change_detection":true,"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("回写缺规则处理失败: HTTP %d %s", status, body)
	}
	missingActivation := strings.Replace(activation, "HT-SPLIT-1", "HT-SPLIT-3", 1)
	missingActivation = strings.Replace(missingActivation, "等保测评", "量子加密检测", 1)
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/contracts/activate", missingActivation)
	if status != http.StatusCreated {
		t.Fatalf("第三次合同激活失败: HTTP %d %s", status, body)
	}
	var missingStatus string
	if err := db.WithContext(ctx).Raw(
		`SELECT status FROM pm_service_item WHERE tenant_id = ? AND category = '量子加密检测'`, tenant).Scan(&missingStatus).Error; err != nil {
		t.Fatalf("读取缺规则服务项失败: %v", err)
	}
	if missingStatus != "待确认" {
		t.Fatalf("域外类别应标记待人工确认，实际 %q（旧实现会错误地自动放行到待分配）", missingStatus)
	}

	// ---- ⑥ 范围变更检测：改掉批次后确认拆解必须先进补充协议 ----
	var projectID string
	if err := db.WithContext(ctx).Raw(`SELECT id FROM pm_project WHERE tenant_id = ? AND contract = 'HT-SPLIT-3'`, tenant).Scan(&projectID).Error; err != nil {
		t.Fatalf("读取项目失败: %v", err)
	}
	var ids []string
	if err := db.WithContext(ctx).Raw(`SELECT id FROM pm_service_item WHERE tenant_id = ? AND project_id = ? ORDER BY id`, tenant, projectID).Scan(&ids).Error; err != nil || len(ids) == 0 {
		t.Fatalf("读取服务项 ID 失败: %v", err)
	}
	confirmBody := fmt.Sprintf(`{"ids":["%s"]}`, strings.Join(ids, `","`))
	// 先把范围改掉：模拟合同清单与拆解结果不一致（批次被改）。
	db.WithContext(ctx).Exec(`UPDATE pm_service_item SET batch = 'B-改' WHERE tenant_id = ? AND project_id = ?`, tenant, projectID)
	status, body = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/service-items/confirm", confirmBody)
	if status != http.StatusConflict {
		t.Fatalf("范围不一致时确认拆解应被拦下（409 前置条件），实际 HTTP %d %s", status, body)
	}
	var supplement string
	if err := db.WithContext(ctx).Raw(`SELECT supplement_status FROM pm_project WHERE tenant_id = ? AND id = ?`, tenant, projectID).Scan(&supplement).Error; err != nil {
		t.Fatalf("读取补充协议状态失败: %v", err)
	}
	if supplement != "REQUIRED" {
		t.Fatalf("范围变更应进入补充协议处理中，实际 %q", supplement)
	}
	// 已在补充协议分支（合同已回写）后允许确认，避免拆解永久无法收口。
	status, body = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/service-items/confirm", confirmBody)
	if status < 200 || status > 299 {
		t.Fatalf("合同回写后确认拆解应放行: HTTP %d %s", status, body)
	}
}
