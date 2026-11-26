package httpapi_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

type approvalApplicantDirectory struct {
	unavailable bool
}

// List 只为原申请人提供用于显示的目录资料。
// 参数：query 为读取真实待办之后计算的目录查询。
// 返回值：合成的在职申请人及一条应忽略的目录数据，或仿真目录暂不可用。
func (d approvalApplicantDirectory) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if d.unavailable {
		return platform.OwnerDirectoryPage{}, errors.New("测试目录不可用")
	}
	items := []platform.OwnerDirectoryUser{{UserID: "other", DisplayName: "无关人员"}}
	if query.UserID == "manager" {
		items = append(items, platform.OwnerDirectoryUser{UserID: "manager", DisplayName: "申请人张敏"})
	}
	return platform.OwnerDirectoryPage{Items: items}, nil
}

// TestApprovalTasksDisplayActualApplicant 验证列表与详情传回原申请人姓名，不丢失授权坐标。
// 参数：t 为 HTTP 测试上下文。
// 返回值：无；目录错误阻断审批或姓名对应到其他账户时失败。
func TestApprovalTasksDisplayActualApplicant(t *testing.T) {
	p := platform.Principal{TenantID: "tenant-1", IdentityID: "director", UserID: "director",
		DisplayName: "审批人李明", Permissions: map[string]bool{
			"project.read": true, "project.report.correction.approve": true,
		}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
	for _, tc := range []struct {
		query       string
		unavailable bool
	}{
		{"", false}, {"?request_id=request", false}, {"", true},
	} {
		service := &application.Service{Repo: &approvalRouteRepo{},
			Personnel: approvalApplicantDirectory{unavailable: tc.unavailable}}
		router := httpapi.NewRouter(service, identity{p: p}, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		res := perform(router, http.MethodGet, "/api/v1/approval-tasks"+tc.query, "")
		body := res.Body.String()
		if res.Code != http.StatusOK || !strings.Contains(body, `"requester_user_id":"manager"`) ||
			!strings.Contains(body, `"can_decide":true`) ||
			strings.Contains(body, "审批人李明") || strings.Contains(body, "无关人员") {
			t.Fatalf("审批响应改变了请求身份或权限：%d %s", res.Code, body)
		}
		if strings.Contains(body, `"requester_display_name":"申请人张敏"`) == tc.unavailable {
			t.Fatalf("原申请人姓名显示错误，目录不可用=%t：%s", tc.unavailable, body)
		}
	}
}
