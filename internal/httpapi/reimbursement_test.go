package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/J-S-Te/license-core"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/platform"
)

type expenseTokenVerifier struct {
	Identity platform.ServiceTokenIdentity
	Err      error
}

// VerifyClientCredentials 返回显式测试机器身份以测试HTTP边界。
// 参数：ctx/token 为认证上下文和输入令牌。
// 返回值：预配置身份或错误，不提供业务仓储成功替身。
func (v expenseTokenVerifier) VerifyClientCredentials(ctx context.Context, token string) (platform.ServiceTokenIdentity, error) {
	return v.Identity, v.Err
}

// TestExpenseMachineHTTPBoundary 验证普通身份、跨租户快照和任意角色字段不能跨越协议边界。
// 参数：t 为测试上下文。
// 返回值：无；拒绝路径未在业务仓储前终止时测试失败。
func TestExpenseMachineHTTPBoundary(t *testing.T) {
	valid := expenseTokenVerifier{Identity: platform.ServiceTokenIdentity{TenantID: "t", ApplicationCode: "reimbursement", EnvironmentCode: "test"}}
	router := NewRouter(&application.Service{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{Reimbursement: &ReimbursementOptions{Enabled: true, ReferenceVerifier: valid, SpendVerifier: valid}})
	cases := []struct {
		Path, Body, Token string
		Status            int
	}{
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u"}`, "", 401},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u","roles":["admin"]}`, "machine", 400},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u","allow_terminated":true}`, "machine", 400},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u","participant_eligible":true}`, "machine", 400},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u","tenant_id":"other"}`, "machine", 400},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u"} {}`, "machine", 400},
		{"/internal/v1/reimbursement/spend-snapshots", `{"tenant_id":"other","source_application":"reimbursement","source_environment":"test"}`, "machine", 403},
		{"/internal/v1/reimbursement/project-references/search", `{"actor_subject_id":"u"}`, "machine", 503},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodPost, tc.Path, strings.NewReader(tc.Body))
		request.Header.Set("Content-Type", "application/json")
		if tc.Token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.Token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != tc.Status {
			t.Fatalf("%s got %d: %s", tc.Path, response.Code, response.Body.String())
		}
	}
}

// TestExpenseCommercialClassification 验证精确POST查询为历史读取而写入及重建仍为变更。
// 参数：t 为测试上下文。
// 返回值：无；许可分类不符合边界时测试失败。
func TestExpenseCommercialClassification(t *testing.T) {
	for _, path := range []string{"/internal/v1/reimbursement/project-references/search", "/internal/v1/reimbursement/project-references/batch-validate"} {
		if commercialOperation(http.MethodPost, path) != core.READ_HISTORY {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"/internal/v1/reimbursement/spend-snapshots", "/internal/v1/reimbursement/spend-snapshots/rebuild"} {
		if commercialOperation(http.MethodPost, path) != core.MUTATE_BUSINESS {
			t.Fatal(path)
		}
	}
}
