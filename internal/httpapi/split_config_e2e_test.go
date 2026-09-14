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

// 检测类别域的「必检能力码（默认）」必须真的驱动能力校验：拆解时落到服务项，
// 分配工程师时（调用方未显式给码）按它比对，而不是只当页面上的说明文字。
func TestDetectionCategoryRequiredCodesDriveCapabilityCheck(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise detection category codes")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const tenant = "PM-SPLIT-CODES-TENANT"
	cleanup := func() {
		for _, statement := range []string{
			`DELETE FROM pm_capability WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_service_item WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_project WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_delivery_event WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_detection_category WHERE tenant_id = '` + tenant + `'`,
			`DELETE FROM pm_split_policy WHERE tenant_id = '` + tenant + `'`,
		} {
			db.WithContext(ctx).Exec(statement)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// 缺规则处理按默认（待人工确认），但默认进入状态设为待分配，便于直接进入分配环节。
	status, body := e2eCall(handler, "admin", http.MethodPut, "/api/v1/split-policy",
		`{"dimension_primary":"batch","dimension_secondary":"category","default_status":"待分配","generate_requirement_summary":true,"missing_rule_action":"HUMAN_CONFIRM","scope_change_detection":false,"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("保存默认分组规则失败: HTTP %d %s", status, body)
	}
	// 给等保测评配置必检能力码（覆盖初始域里留空的那条）。
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/detection-categories",
		`{"category":"等保测评","system_standard":"等保 2.0","required_qualifications":"等级保护测评师（中级+）","required_codes":"DJCP, ISO27001","special_method":"NO","enabled":true}`)
	if status < 200 || status > 299 {
		t.Fatalf("保存检测类别失败: HTTP %d %s", status, body)
	}
	// 未配置必检能力码的类别不得凭空带出能力码。
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/detection-categories",
		`{"category":"漏洞扫描","special_method":"NO","enabled":true}`)
	if status < 200 || status > 299 {
		t.Fatalf("保存检测类别失败: HTTP %d %s", status, body)
	}

	activation := `{"contract_id":"HT-CODES-1","contract_version":"v1","contract_name":"能力码走查合同","customer":"走查客户","effective_at":"2026-09-14T00:00:00Z",
		"services":[
			{"source_id":"C1","site":"杭州机房","batch":"B1","category":"等保测评"},
			{"source_id":"C2","site":"杭州机房","batch":"B2","category":"漏洞扫描"}
		]}`
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/contracts/activate", activation)
	if status != http.StatusCreated {
		t.Fatalf("合同激活失败: HTTP %d %s", status, body)
	}
	type itemRow struct {
		ID            string
		Category      string
		RequiredCodes []byte
	}
	var rows []itemRow
	if err := db.WithContext(ctx).Raw(
		`SELECT id, category, required_codes FROM pm_service_item WHERE tenant_id = ? ORDER BY id`, tenant).
		Scan(&rows).Error; err != nil {
		t.Fatalf("读取服务项失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 条服务项，实际 %d", len(rows))
	}
	var codes []string
	if err := json.Unmarshal(rows[0].RequiredCodes, &codes); err != nil {
		t.Fatalf("解析必检能力码失败: %v (%s)", err, string(rows[0].RequiredCodes))
	}
	t.Logf("等保测评服务项必检能力码 = %v；漏洞扫描 = %s", codes, string(rows[1].RequiredCodes))
	if len(codes) != 2 || codes[0] != "DJCP" || codes[1] != "ISO27001" {
		t.Fatalf("检测类别的必检能力码应落到服务项，实际 %v", codes)
	}
	if string(rows[1].RequiredCodes) == "" || string(rows[1].RequiredCodes) == "null" {
		// 未配置能力码的类别必须落空数组，不能凭空生成。
		empty := []string{}
		_ = json.Unmarshal(rows[1].RequiredCodes, &empty)
		if len(empty) != 0 {
			t.Fatalf("未配置能力码的类别不应带出能力码，实际 %v", empty)
		}
	}

	// 两名工程师：一人具备 DJCP + ISO27001，一人只有 OTHER。
	now := "2026-09-14 00:00:00"
	for _, seed := range []struct{ id, name, codesJSON string }{
		{"ENG-OK", "合格工程师", `["DJCP","ISO27001"]`},
		{"ENG-BAD", "缺证工程师", `["OTHER"]`},
	} {
		statement := `INSERT INTO pm_capability (id, tenant_id, resource_type, resource_id, resource_name, capability_codes, valid_from, valid_until, status, usage_scope, updated_at, updated_by)
			VALUES ('CAP-` + seed.id + `', '` + tenant + `', 'PERSON', '` + seed.id + `', '` + seed.name + `', JSON_ARRAY(` +
			strings.Trim(seed.codesJSON, "[]") + `), DATE_SUB(NOW(3), INTERVAL 1 DAY), DATE_ADD(NOW(3), INTERVAL 1 YEAR), 'ACTIVE', 'ANY', NOW(3), 'seed')`
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatalf("seed 能力失败: %v", err)
		}
	}
	_ = now

	itemID := rows[0].ID
	status, body = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/service-items/"+itemID+"/team-assignment",
		`{"team_lead_id":"PM-SPLIT-LEAD"}`)
	if status < 200 || status > 299 {
		t.Fatalf("分配团队负责人失败: HTTP %d %s", status, body)
	}
	// 调用方不传 required_codes：必须回落到服务项上的必检能力码，缺证工程师判冲突。
	status, body = e2eCall(handler, "team_lead", http.MethodPost, "/api/v1/service-items/"+itemID+"/execution-assignment",
		`{"project_manager_id":"PM-SPLIT-PM","engineer_ids":["ENG-BAD"]}`)
	if status < 200 || status > 299 {
		t.Fatalf("执行分配失败: HTTP %d %s", status, body)
	}
	var conflictStatus string
	if err := db.WithContext(ctx).Raw(`SELECT conflict_status FROM pm_service_item WHERE tenant_id = ? AND id = ?`, tenant, itemID).
		Scan(&conflictStatus).Error; err != nil {
		t.Fatalf("读取校验结论失败: %v", err)
	}
	if conflictStatus != "CONFLICT" {
		t.Fatalf("缺证工程师应判能力冲突，实际 %q（必检能力码没有生效？）", conflictStatus)
	}
	status, body = e2eCall(handler, "team_lead", http.MethodPost, "/api/v1/service-items/"+itemID+"/execution-assignment",
		`{"project_manager_id":"PM-SPLIT-PM","engineer_ids":["ENG-OK"]}`)
	if status < 200 || status > 299 {
		t.Fatalf("执行分配失败: HTTP %d %s", status, body)
	}
	if err := db.WithContext(ctx).Raw(`SELECT conflict_status FROM pm_service_item WHERE tenant_id = ? AND id = ?`, tenant, itemID).
		Scan(&conflictStatus).Error; err != nil {
		t.Fatalf("读取校验结论失败: %v", err)
	}
	if conflictStatus != "PASSED" {
		t.Fatalf("持证工程师应校验通过，实际 %q", conflictStatus)
	}

	// 批量导入：合法行写入、非法行跳过并给出行号原因。
	status, body = e2eCall(handler, "admin", http.MethodPost, "/api/v1/detection-categories/import",
		`{"items":[{"category":"导入类别A","system_standard":"ISO 27001","required_codes":"A1，A2","special_method":"MARKABLE","enabled":true},{"category":"","special_method":"NO","enabled":true},{"category":"导入类别B","special_method":"不存在的取值","enabled":true}]}`)
	if status != http.StatusOK {
		t.Fatalf("导入检测类别失败: HTTP %d %s", status, body)
	}
	var imported struct {
		Data struct {
			Imported int      `json:"imported"`
			Skipped  int      `json:"skipped"`
			Errors   []string `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &imported); err != nil {
		t.Fatalf("解析导入结果失败: %v (%s)", err, body)
	}
	t.Logf("导入结果：成功 %d，跳过 %d，原因 %v", imported.Data.Imported, imported.Data.Skipped, imported.Data.Errors)
	if imported.Data.Imported != 1 || imported.Data.Skipped != 2 {
		t.Fatalf("导入应有 1 行成功、2 行跳过，实际 %+v", imported.Data)
	}
	var storedCodes string
	if err := db.WithContext(ctx).Raw(`SELECT required_codes FROM pm_detection_category WHERE tenant_id = ? AND category = '导入类别A'`, tenant).
		Scan(&storedCodes).Error; err != nil {
		t.Fatalf("读取导入结果失败: %v", err)
	}
	if storedCodes != "A1,A2" {
		t.Fatalf("中文逗号分隔的能力码应被规范化，实际 %q", storedCodes)
	}
}
