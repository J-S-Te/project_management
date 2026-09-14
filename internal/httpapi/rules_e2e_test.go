package httpapi_test

// 规则创建必须成功并返回数据库生成的主键。
//
// 回归背景：多种规则（拆解/预警/自动化/字段级权限/SLA/检测标准/能力编码）新建时都必须返回
// 404「资源不存在」，而行其实已经写入——根因是回读用了客户端未提供的 ID（0），
// 把创建成功报成失败；用户据此重复点击会插入重复规则。

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
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

// ruleCreateCases 是各种配置的合法创建载荷与目标表。
var ruleCreateCases = []struct {
	kind  string
	name  string
	body  string
	table string
}{
	{"split-rules", "走查拆解规则", `{"kind":"split-rules","name":"走查拆解规则","scope":"浙江","enabled":true}`, "pm_split_rule"},
	{"warning-rules", "走查预警规则", `{"kind":"warning-rules","name":"走查预警规则","check_type":"超期","threshold":"3","enabled":true}`, "pm_warning_rule"},
	{"automations", "走查自动化", `{"kind":"automations","name":"走查自动化","trigger":"DEVIATION_REPORTED","target":"technical_director","enabled":true}`, "pm_automation"},
	{"permissions", "走查字段权限", `{"kind":"permissions","name":"走查字段权限","role_code":"engineer","field_name":"customer","access_level":"view","enabled":true}`, "pm_field_permission"},
	{"sla", "走查 SLA", `{"kind":"sla","name":"走查 SLA","status":"实施中","deadline_hours":24,"remind_hours":4,"enabled":true}`, "pm_sla"},
	{"standards", "走查标准", `{"kind":"standards","name":"走查标准","scope":"GB/T 28448","enabled":true}`, "pm_standard"},
	{"capability-codes", "CISP-PTE", `{"kind":"capability-codes","name":"CISP-PTE","scope":" cisp-pte ","check_type":" person ","enabled":true}`, "pm_capability_code"},
}

func TestRulesCreateReturnsGeneratedID(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to reproduce the rule creation defect")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	const tenant = "PM-RULE-TENANT"
	for _, table := range []string{"pm_split_rule", "pm_warning_rule", "pm_automation", "pm_field_permission", "pm_sla", "pm_standard", "pm_capability_code"} {
		db.Exec("DELETE FROM " + table + " WHERE tenant_id = '" + tenant + "'")
	}
	t.Cleanup(func() {
		for _, table := range []string{"pm_split_rule", "pm_warning_rule", "pm_automation", "pm_field_permission", "pm_sla", "pm_standard", "pm_capability_code"} {
			db.Exec("DELETE FROM " + table + " WHERE tenant_id = '" + tenant + "'")
		}
	})

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	cases := ruleCreateCases
	for _, testCase := range cases {
		status, response := e2eCall(handler, "admin", http.MethodPost, "/api/v1/rules", testCase.body)
		var rows int64
		if err := db.Raw("SELECT COUNT(*) FROM "+testCase.table+" WHERE tenant_id = ?", tenant).Scan(&rows).Error; err != nil {
			t.Fatalf("count %s: %v", testCase.table, err)
		}
		var payload struct {
			Data struct {
				ID   int64  `json:"id"`
				Kind string `json:"kind"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(response), &payload); err != nil {
			t.Fatalf("%s: 解析响应失败: %v (%s)", testCase.kind, err, response)
		}
		t.Logf("%-15s -> HTTP %d | 库中行数=%d | 返回 id=%d", testCase.kind, status, rows, payload.Data.ID)
		if status < 200 || status > 299 {
			t.Fatalf("%s: 新建规则必须成功，实际 HTTP %d %s", testCase.kind, status, response)
		}
		if rows != 1 {
			t.Fatalf("%s: 应恰好写入一行，实际 %d", testCase.kind, rows)
		}
		// 返回的 id 必须是数据库生成的真实主键：此前返回 0 并在回读时报「资源不存在」。
		var storedID int64
		if err := db.Raw("SELECT id FROM "+testCase.table+" WHERE tenant_id = ?", tenant).Scan(&storedID).Error; err != nil {
			t.Fatalf("read id %s: %v", testCase.table, err)
		}
		if payload.Data.ID == 0 || payload.Data.ID != storedID {
			t.Fatalf("%s: 返回 id=%d 与库中 id=%d 不一致", testCase.kind, payload.Data.ID, storedID)
		}
		if payload.Data.Kind != testCase.kind {
			t.Fatalf("%s: 返回 kind=%q", testCase.kind, payload.Data.Kind)
		}
	}
}

// 规则配置必须可以删除：以前只有新建/编辑/启停，配置错了或重复堆积无法清理。
// 删除按 kind 定位配置表 + 租户 + 主键，删完行必须真的从表里消失，重复删除返回 404。
func TestRulesDeleteRemovesRowEndToEnd(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise rule deletion")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	const tenant = "PM-RULE-DELETE-TENANT"
	cleanup := func() {
		for _, testCase := range ruleCreateCases {
			db.Exec("DELETE FROM " + testCase.table + " WHERE tenant_id = '" + tenant + "'")
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, testCase := range ruleCreateCases {
		status, response := e2eCall(handler, "admin", http.MethodPost, "/api/v1/rules", testCase.body)
		if status < 200 || status > 299 {
			t.Fatalf("%s: 建规则失败 HTTP %d %s", testCase.kind, status, response)
		}
		var created struct {
			Data struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(response), &created); err != nil {
			t.Fatalf("%s: 解析创建响应失败: %v (%s)", testCase.kind, err, response)
		}
		if created.Data.ID == 0 {
			t.Fatalf("%s: 创建未返回主键: %s", testCase.kind, response)
		}

		path := "/api/v1/rules/" + strconv.FormatInt(created.Data.ID, 10) + "?kind=" + testCase.kind
		status, response = e2eCall(handler, "admin", http.MethodDelete, path, "")
		if status < 200 || status > 299 {
			t.Fatalf("%s: 删除失败 HTTP %d %s", testCase.kind, status, response)
		}

		var rows int64
		if err := db.Raw("SELECT COUNT(*) FROM "+testCase.table+" WHERE tenant_id = ?", tenant).Scan(&rows).Error; err != nil {
			t.Fatalf("count %s: %v", testCase.table, err)
		}
		t.Logf("%-15s id=%-4d -> HTTP %d | 删除后库中行数=%d", testCase.kind, created.Data.ID, status, rows)
		if rows != 0 {
			t.Fatalf("%s: 删除后库里仍有 %d 行", testCase.kind, rows)
		}

		// 重复删除：必须 404，不能把已经不存在的行报成成功（否则前端会以为删掉了别的行）。
		status, response = e2eCall(handler, "admin", http.MethodDelete, path, "")
		if status != http.StatusNotFound {
			t.Fatalf("%s: 重复删除应 404，实际 HTTP %d %s", testCase.kind, status, response)
		}
	}

	// 列表不再返回任何已删除的规则。
	status, response := e2eCall(handler, "admin", http.MethodGet, "/api/v1/rules", "")
	if status != http.StatusOK {
		t.Fatalf("列表 HTTP %d %s", status, response)
	}
	for _, testCase := range ruleCreateCases {
		if strings.Contains(response, testCase.name) {
			t.Fatalf("删除后列表仍返回 %s: %s", testCase.name, response)
		}
	}
}
