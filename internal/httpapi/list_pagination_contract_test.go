package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

// 列表分页契约（P0 回退后）：项目/服务项/设备三个列表端点在省略 page_size 时
// 必须返回全部行（前端与下拉数据源依赖的既有语义），分页只在显式传正数 page_size
// 时生效。本文件用 httptest + 假仓储固定该 HTTP 契约（不连数据库）：
//   - 省略（或 0/负数）page_size → 返回全部行，envelope 回显 page=1、
//     page_size=len(items)、total=len(items)、truncated=false；
//   - 显式正数 page_size → 分页生效，page_size 上限 200，page 上限 100000（仅分页时校验）；
//   - page 非数字/非正数、page_size 非数字 → 422，不静默回退；
//   - all=1/true → 最多 500 行，忽略分页参数与之互斥（同时传即 422）；
//   - PageEnvelope.truncated 只在 all 模式且 total > len(items) 时为 true。

var boundedListPaths = []string{"/api/v1/projects", "/api/v1/service-items", "/api/v1/equipment"}

func seedProjects(count int) []domain.Project {
	projects := make([]domain.Project, 0, count)
	for index := 0; index < count; index++ {
		projects = append(projects, domain.Project{ID: "PJ-" + padIndex(index), TenantID: "tenant-1"})
	}
	return projects
}

func seedServiceItems(count int) []domain.ServiceItem {
	items := make([]domain.ServiceItem, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, domain.ServiceItem{ID: "SI-" + padIndex(index), ProjectID: "PJ-1", TenantID: "tenant-1", Status: "待分配"})
	}
	return items
}

func seedCapabilities(count int) []domain.Capability {
	items := make([]domain.Capability, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, domain.Capability{ResourceID: "EQ-" + padIndex(index), ResourceType: "EQUIPMENT", TenantID: "tenant-1"})
	}
	return items
}

func padIndex(index int) string {
	digits := []byte("0000")
	value := index
	for position := len(digits) - 1; position >= 0 && value > 0; position-- {
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits)
}

func boundedListHandler(t *testing.T, repository *repo) http.Handler {
	t.Helper()
	service := &application.Service{Repo: repository}
	principal := platform.Principal{
		TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", DisplayName: "测试用户",
		Roles: []string{"admin"},
		Permissions: map[string]bool{
			"project.read": true, "project.device.read": true,
		},
		DataScopes:            []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}},
		AuthorizationRevision: 1, CatalogVersion: "2",
	}
	return httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type listPageBody struct {
	Items     []json.RawMessage `json:"items"`
	Total     int               `json:"total"`
	Page      int               `json:"page"`
	PageSize  int               `json:"page_size"`
	Truncated bool              `json:"truncated"`
}

func decodeListPage(t *testing.T, response *httptest.ResponseRecorder) listPageBody {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		Data listPageBody `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, response.Body.String())
	}
	return body.Data
}

// 省略 page_size 必须返回全部行（P0 回退）：25 行的集合不能被静默截断成 20 行。
func TestListEndpointsOmittedPageSizeReturnsEverything(t *testing.T) {
	handler := boundedListHandler(t, &repo{
		projects: seedProjects(25), items: seedServiceItems(25), capabilities: seedCapabilities(25),
	})
	for _, path := range boundedListPaths {
		t.Run(path, func(t *testing.T) {
			page := decodeListPage(t, perform(handler, http.MethodGet, path, ""))
			if len(page.Items) != 25 {
				t.Fatalf("omitted page_size must return every row (25 rows), got %d", len(page.Items))
			}
			if page.Page != 1 || page.PageSize != 25 || page.Total != 25 || page.Truncated {
				t.Fatalf("unexpected full-list envelope: %+v", page)
			}

			// 只传 page 不传 page_size 仍然是"返回全部"：page 在未分页时无意义，
			// 不能被当成"默认 20 行的第二页"（后者只会返回 5 行，等价于静默截断）。
			page = decodeListPage(t, perform(handler, http.MethodGet, path+"?page=2", ""))
			if len(page.Items) != 25 || page.Page != 1 || page.PageSize != 25 || page.Total != 25 {
				t.Fatalf("page without page_size must stay unpaged: %+v", page)
			}

			// page_size<=0 是既有的"不分页"信号，同样返回全部行；超大 page 在不分页时
			// 也没有意义（不回退成默认单页，也不 422）。
			for _, query := range []string{"page_size=0", "page_size=-5", "page=100001"} {
				page = decodeListPage(t, perform(handler, http.MethodGet, path+"?"+query, ""))
				if len(page.Items) != 25 || page.PageSize != 25 || page.Total != 25 {
					t.Fatalf("%s must be unpaged: %+v", query, page)
				}
			}
		})
	}
}

// 分页参数非法时明确 422，而不是静默回退到"第一页"或"返回全部"。
func TestListEndpointsRejectOutOfContractPageParams(t *testing.T) {
	handler := boundedListHandler(t, &repo{
		projects: seedProjects(25), items: seedServiceItems(25), capabilities: seedCapabilities(25),
	})
	invalidQueries := []string{
		"page_size=abc",
		"page=0",
		"page=-1",
		"page=1.5",
		// page 上界只在显式分页时校验：不分页时 page 无意义（见下一条用例）。
		"page=100001&page_size=20",
		"all=1&page=2",
		"all=1&page_size=20",
		"all=0",
	}
	for _, path := range boundedListPaths {
		for _, query := range invalidQueries {
			t.Run(path+"?"+query, func(t *testing.T) {
				response := perform(handler, http.MethodGet, path+"?"+query, "")
				if response.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422; body=%s", response.Code, response.Body.String())
				}
			})
		}
	}

	// 上界本身仍然合法：page_size 收敛到 200，page 允许 100000。
	for _, path := range boundedListPaths {
		t.Run(path+" upper bounds accepted", func(t *testing.T) {
			page := decodeListPage(t, perform(handler, http.MethodGet, path+"?page=100000&page_size=100000", ""))
			if page.Page != 100000 || page.PageSize != 200 || len(page.Items) != 0 || page.Total != 25 {
				t.Fatalf("upper-bound envelope: %+v", page)
			}
		})
	}
}

// all=1/true 是工作区快照入口：最多 500 行，超出部分用 truncated 显式告知调用方。
func TestListEndpointsSnapshotModeCapsRowsAndFlagsTruncation(t *testing.T) {
	handler := boundedListHandler(t, &repo{
		projects: seedProjects(600), items: seedServiceItems(600), capabilities: seedCapabilities(600),
	})
	for _, path := range boundedListPaths {
		t.Run(path, func(t *testing.T) {
			page := decodeListPage(t, perform(handler, http.MethodGet, path+"?all=1", ""))
			if len(page.Items) != 500 {
				t.Fatalf("snapshot must be capped at 500 rows, got %d", len(page.Items))
			}
			if page.Total != 600 || page.Page != 1 || page.PageSize != 500 {
				t.Fatalf("unexpected snapshot envelope: %+v", page)
			}
			if !page.Truncated {
				t.Fatalf("snapshot of 500 rows out of 600 must be flagged truncated: %+v", page)
			}

			// all=true（大小写不敏感）与 all=1 等价。
			caseInsensitive := decodeListPage(t, perform(handler, http.MethodGet, path+"?all=TRUE", ""))
			if len(caseInsensitive.Items) != 500 || !caseInsensitive.Truncated {
				t.Fatalf("all=TRUE must behave like all=1: %+v", caseInsensitive)
			}
		})
	}
}

// 快照未超上限时 truncated 必须为 false；total 仍然是过滤后的真实总数。
func TestListEndpointsSnapshotModeWithoutTruncation(t *testing.T) {
	handler := boundedListHandler(t, &repo{
		projects: seedProjects(3), items: seedServiceItems(3), capabilities: seedCapabilities(3),
	})
	for _, path := range boundedListPaths {
		t.Run(path, func(t *testing.T) {
			page := decodeListPage(t, perform(handler, http.MethodGet, path+"?all=1", ""))
			if len(page.Items) != 3 || page.Total != 3 || page.PageSize != 500 {
				t.Fatalf("unexpected complete snapshot envelope: %+v", page)
			}
			if page.Truncated {
				t.Fatalf("complete snapshot must not be truncated: %+v", page)
			}
		})
	}
}

// 普通分页下"本页少于 total"是正常现象，绝不能被标成 truncated。
func TestListEndpointsRegularPagingIsNeverTruncated(t *testing.T) {
	handler := boundedListHandler(t, &repo{
		projects: seedProjects(25), items: seedServiceItems(25), capabilities: seedCapabilities(25),
	})
	for _, path := range boundedListPaths {
		t.Run(path, func(t *testing.T) {
			page := decodeListPage(t, perform(handler, http.MethodGet, path+"?page=1&page_size=10", ""))
			if len(page.Items) != 10 || page.Total != 25 || page.Page != 1 || page.PageSize != 10 {
				t.Fatalf("unexpected paged envelope: %+v", page)
			}
			if page.Truncated {
				t.Fatalf("regular paging must not be truncated: %+v", page)
			}
		})
	}
}
