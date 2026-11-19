package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

type approvalRouteRepo struct {
	repo
	filter platform.ScopeFilter
	query  domain.ApprovalQuery
	calls  int
}

type approvalItemLookupRepo struct {
	repo
	filter platform.ScopeFilter
	itemID string
}

// GetServiceItem 记录精确查询，并返回合成授权范围内的服务项。
// 参数：ctx 为请求，filter 为经过服务端确认的数据范围，id 为请求编号。
// 返回值：匹配时返回单条服务项，其余返回不可见。
func (r *approvalItemLookupRepo) GetServiceItem(
	_ context.Context, filter platform.ScopeFilter, id string,
) (domain.ServiceItem, error) {
	r.filter, r.itemID = filter, id
	if filter.TenantID != "tenant-1" || id != "SI-1" {
		return domain.ServiceItem{}, application.ErrNotFound
	}
	return domain.ServiceItem{ID: id, ProjectID: "PJ-1", Site: "现场"}, nil
}

// TestApprovalServiceItemExactReadScope 验证直达查询复用项目数据范围和旧列表形状。
// 参数：t 为真实 HTTP 路由测试上下文。
// 返回值：无；精确请求丢失数据范围或返回其他项目时失败。
func TestApprovalServiceItemExactReadScope(t *testing.T) {
	for _, test := range []struct {
		query string
		want  int
		reads bool
	}{
		{"?all=1&service_item_id=SI-1&project_id=PJ-1&tenant_id=other", 200, true},
		{"?all=1&service_item_id=SI-1&project_id=PJ-2", 404, true},
		{"?all=1&service_item_id=SI-1&service_item_id=SI-2", 422, false},
		{"?all=1&service_item_id=unknown", 404, true},
	} {
		repository := &approvalItemLookupRepo{}
		p := platform.Principal{TenantID: "tenant-1", IdentityID: "lead", UserID: "lead",
			Roles: []string{"team_lead"}, Permissions: map[string]bool{"project.read": true},
			DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
		handler := httpapi.NewRouter(&application.Service{Repo: repository}, identity{p: p},
			nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		response := perform(handler, http.MethodGet, "/api/v1/service-items"+test.query, "")
		if response.Code != test.want || (repository.itemID != "") != test.reads {
			t.Fatalf("query=%s status=%d stored id=%q body=%s",
				test.query, response.Code, repository.itemID, response.Body.String())
		}
		if test.reads && (!repository.filter.AssignedItemsOnly || repository.filter.TenantID != "tenant-1") {
			t.Fatalf("trusted data scope lost: %+v", repository.filter)
		}
		if test.want == 200 && (!strings.Contains(response.Body.String(), `"id":"SI-1"`) ||
			!strings.Contains(response.Body.String(), `"total":1`)) {
			t.Fatalf("unexpected page: %s", response.Body.String())
		}
	}
}

// ListApprovalTasks 记录 HTTP 边界传入的可信范围并返回合成待办。
// 参数：ctx 为请求，filter 为授权范围，q 为已经验证的查询。
// 返回值：一条待处理更正申请和总数，供真实服务权限计算使用。
func (r *approvalRouteRepo) ListApprovalTasks(ctx context.Context, filter platform.ScopeFilter, q domain.ApprovalQuery) ([]domain.ApprovalTask, int64, error) {
	r.filter, r.query = filter, q
	r.calls++
	return []domain.ApprovalTask{{ID: "request", Type: "REPORT_CORRECTION", RequesterUserID: "manager"}}, 1, nil
}

// TestApprovalTasksRouteValidatesIdentityAndPagination 验证真实路由授权、分页及不可伪造的用户范围。
// 参数：t 为测试上下文，数据库边界以捕获查询的仓储替身隔离。
// 返回值：无；无权请求、非法分页被放行或租户被覆盖时失败。
func TestApprovalTasksRouteValidatesIdentityAndPagination(t *testing.T) {
	for _, test := range []struct {
		name, query string
		read        bool
		want        int
	}{
		{"scoped list", "?tenant_id=other&user_id=other", true, 200},
		{"missing read permission", "", false, 403},
		{"invalid page", "?page=no", true, 422},
		{"oversize page", "?page_size=101", true, 422},
		{"invalid view", "?view=all", true, 422},
		{"unknown task type", "?request_id=DV-1&task_type=OTHER", true, 422},
		{"task type without request", "?task_type=FIELD_DEVIATION", true, 422},
		{"duplicated request id", "?request_id=DV-1&request_id=DV-2&task_type=FIELD_DEVIATION", true, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &approvalRouteRepo{}
			p := platform.Principal{TenantID: "tenant-1", IdentityID: "director", UserID: "director",
				Roles: []string{"technical_director"}, Permissions: map[string]bool{
					"project.read": test.read, "project.report.correction.approve": true,
				}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
			handler := httpapi.NewRouter(&application.Service{Repo: repository}, identity{p: p}, nil,
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			response := perform(handler, http.MethodGet, "/api/v1/approval-tasks"+test.query, "")
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
			if test.want != 200 {
				if repository.calls != 0 {
					t.Fatal("invalid request reached storage")
				}
				return
			}
			if repository.filter.TenantID != "tenant-1" || repository.query.UserID != "director" ||
				!repository.query.CanCorrection || repository.query.CanRollback {
				t.Fatalf("untrusted scope: filter=%+v query=%+v", repository.filter, repository.query)
			}
			if !strings.Contains(response.Body.String(), `"can_decide":true`) ||
				response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("response=%s", response.Body.String())
			}
		})
	}
}
