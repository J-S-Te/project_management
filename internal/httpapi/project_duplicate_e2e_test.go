package httpapi_test

// 同一合同 + 同一版本重复新建项目必须有明确业务提示，而不是 500「服务暂不可用」。
//
// 现场现象：业务管理员在项目列表重复选同一份已审批合同新建项目，连续三次都提示
// 「服务暂不可用」。根因是唯一键 uq_pm_project_contract_version 冲突（MySQL 1062）
// 未在应用层翻译，被 writeServiceError 兜底成 PM_INTERNAL_ERROR。

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

func TestDuplicateContractVersionReportsExecutableError(t *testing.T) {
	dsn := os.Getenv("PM_TEST_DSN")
	if dsn == "" {
		t.Skip("set PM_TEST_DSN to exercise duplicate project creation")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	ctx := context.Background()
	const tenant = "PM-DUP-PROJECT-TENANT"
	cleanup := func() {
		db.WithContext(ctx).Exec(`DELETE FROM pm_service_item WHERE tenant_id = ?`, tenant)
		db.WithContext(ctx).Exec(`DELETE FROM pm_project WHERE tenant_id = ?`, tenant)
	}
	cleanup()
	t.Cleanup(cleanup)

	repository := store.NewRepository(db)
	service := &application.Service{Repo: repository, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := httpapi.NewRouter(service, switchIdentityFor(tenant), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{"name":"走查重复项目","customer":"走查客户","contract":"HT-DUP-1","contract_id":"approved-dup-1","contract_version":"v1",
		"service_items":[{"source_id":"MANUAL-001","site":"杭州机房","category":"等保测评","test_mode":"STANDARD"}]}`
	status, response := e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/projects", body)
	if status != http.StatusCreated {
		t.Fatalf("首次创建应成功：HTTP %d %s", status, response)
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(response), &created); err != nil {
		t.Fatalf("解析创建响应失败: %v (%s)", err, response)
	}

	status, response = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/projects", body)
	t.Logf("重复创建 -> HTTP %d %s", status, response)
	if status != http.StatusConflict {
		t.Fatalf("重复创建应返回 409 业务冲突，实际 HTTP %d %s", status, response)
	}
	if !strings.Contains(response, "PM_DUPLICATE_PROJECT") {
		t.Fatalf("应给出可识别的错误码：%s", response)
	}
	// 提示里必须带上已存在项目编号与替代动作，否则用户仍然不知道该怎么办。
	if !strings.Contains(response, created.Data.ID) || !strings.Contains(response, "调整拆解") {
		t.Fatalf("提示应包含已存在项目编号与替代动作：%s", response)
	}
	// 换一个版本号可以正常创建：同一合同的后续版本是合法业务场景。
	nextVersion := strings.Replace(body, `"contract_version":"v1"`, `"contract_version":"v2"`, 1)
	status, response = e2eCall(handler, "business_admin", http.MethodPost, "/api/v1/projects", nextVersion)
	if status != http.StatusCreated {
		t.Fatalf("同合同新版本应可创建：HTTP %d %s", status, response)
	}
}
