package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/authz"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

// taskAssignmentPrincipal 从实际发布清单构造岗位授权，避免测试自行补发待验证权限。
// 参数：t 为测试上下文，roleCode 为当前操作人的项目岗位编码。
// 返回值：含清单权限和应用数据范围的主体；清单无该岗位时终止测试。
func taskAssignmentPrincipal(t *testing.T, roleCode string) platform.Principal {
	t.Helper()
	var manifest struct {
		Version string `json:"catalog_version"`
		Roles   []struct {
			Code        string   `json:"code"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(authz.PermissionManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, role := range manifest.Roles {
		if role.Code != roleCode {
			continue
		}
		permissions := make(map[string]bool, len(role.Permissions))
		for _, permission := range role.Permissions {
			permissions[permission] = true
		}
		return platform.Principal{
			TenantID: "tenant-1", IdentityID: roleCode, UserID: roleCode,
			Roles: []string{roleCode}, Permissions: permissions,
			DataScopes:            []platform.DataScope{{RoleCode: roleCode, ScopeType: "APPLICATION"}},
			AuthorizationRevision: 1, CatalogVersion: manifest.Version,
		}
	}
	t.Fatalf("项目权限清单缺少岗位 %q", roleCode)
	return platform.Principal{}
}

// taskAssignmentHandler 装配真实分配路由与业务服务，并隔离外部人员目录和持久化。
// 参数：p 为来自权限清单的操作主体。
// 返回值：HTTP 路由和记录业务事件的仓储替身，不连接数据库或真实平台。
func taskAssignmentHandler(p platform.Principal) (http.Handler, *repo) {
	repository := &repo{items: []domain.ServiceItem{{
		ID: "SI-1", ProjectID: "PJ-1", TenantID: "tenant-1", Version: 3,
		Status: "待分配", TeamLeadID: "team_lead", ProjectManagerID: "project_manager",
		EngineerIDs: []string{"engineer"}, RequiredCodes: []string{"CORE"},
	}}}
	for _, userID := range []string{"team_lead", "project_manager", "engineer"} {
		repository.capabilities = append(repository.capabilities, domain.Capability{
			ResourceType: "PERSON", ResourceID: "P-" + userID, UserID: userID,
			Status: "ACTIVE", IdentityStatus: domain.IdentityStatusActive, Codes: []string{"CORE"},
		})
	}
	service := &application.Service{Repo: repository, Personnel: e2ePersonnelDirectory{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.NewRouter(service, identity{p: p}, nil, logger), repository
}

// TestTechnicalDirectorTaskAssignmentNavigation 验证技术总监能从服务端导航进入任务分配。
// 参数：t 为测试上下文。
// 返回值：无；缺少任务分配入口时测试失败。
func TestTechnicalDirectorTaskAssignmentNavigation(t *testing.T) {
	body := navigationBodyForRole(t, "technical_director")
	if !strings.Contains(body, `"allocation"`) {
		t.Fatalf("技术总监缺少任务分配入口：%s", body)
	}
}

// TestTaskAssignmentRoutesUsePublishedRolePermissions 验证清单授权可驱动分配及撤销接口。
// 参数：t 为测试上下文，各岗位独立调用真实路由与业务服务。
// 返回值：无；授权结果不符或被拒请求写入业务事件时测试失败。
func TestTaskAssignmentRoutesUsePublishedRolePermissions(t *testing.T) {
	commands := []struct {
		path, body, event string
	}{
		{"team-assignment", `{"team_lead_id":"team_lead","expected_version":3}`,
			application.EventTeamAssigned},
		{"execution-assignment",
			`{"project_manager_id":"project_manager","engineer_ids":["engineer"],"expected_version":3}`,
			application.EventExecutionTeamAssigned},
		{"team-assignment/revoke", `{"reason":"调整人员安排","expected_version":3}`,
			application.EventTeamAssignmentRevoked},
		{"execution-assignment/revoke", `{"reason":"调整人员安排","expected_version":3}`,
			application.EventExecutionAssignmentRevoked},
	}
	for _, tc := range []struct {
		role string
		want [4]int
	}{
		{"technical_director", [4]int{200, 200, 200, 200}},
		{"admin", [4]int{200, 200, 200, 200}},
		{"system_admin", [4]int{200, 200, 200, 200}},
		{"business_admin", [4]int{200, 403, 200, 403}},
		{"team_lead", [4]int{403, 200, 403, 200}},
		{"project_manager", [4]int{403, 403, 403, 403}},
		{"engineer", [4]int{403, 403, 403, 403}},
	} {
		for index, command := range commands {
			t.Run(tc.role+"/"+command.path, func(t *testing.T) {
				p := taskAssignmentPrincipal(t, tc.role)
				handler, repository := taskAssignmentHandler(p)
				path := "/api/v1/service-items/SI-1/" + command.path
				response := perform(handler, http.MethodPost, path, command.body)
				if response.Code != tc.want[index] {
					t.Fatalf("HTTP %d，期望 %d：%s", response.Code, tc.want[index], response.Body)
				}
				if response.Code == http.StatusForbidden {
					if len(repository.events) != 0 {
						t.Fatal("未授权请求写入了分配事件")
					}
					return
				}
				if len(repository.events) != 1 {
					t.Fatalf("分配事件数量=%d，期望 1", len(repository.events))
				}
				event := repository.events[0]
				if event.Type != command.event || event.ActorUserID != p.UserID ||
					event.TenantID != p.TenantID || event.ServiceItemID != "SI-1" {
					t.Fatalf("分配事件未保留真实命令及操作主体：%+v", event)
				}
				if command.event == application.EventExecutionTeamAssigned &&
					!strings.Contains(response.Body.String(), `"passed":true`) {
					t.Fatalf("执行团队未通过真实业务能力校验：%s", response.Body)
				}
			})
		}
	}
}

// TestTechnicalDirectorTaskAssignmentKeepsGuards 验证技术总监仍受范围、版本及输入约束。
// 参数：t 为测试上下文；各用例独立构造有效技术总监并改变单一前置条件。
// 返回值：无；错误请求被受理或产生业务事件时测试失败。
func TestTechnicalDirectorTaskAssignmentKeepsGuards(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"无数据范围", "team-assignment", `{"team_lead_id":"team_lead","expected_version":3}`, 403},
		{"缺少精确权限", "team-assignment", `{"team_lead_id":"team_lead","expected_version":3}`, 403},
		{"过期版本", "team-assignment", `{"team_lead_id":"team_lead","expected_version":2}`, 409},
		{"服务项不存在", "team-assignment", `{"team_lead_id":"team_lead","expected_version":3}`, 404},
		{"缺少撤销原因", "team-assignment/revoke", `{"expected_version":3}`, 422},
		{"无有效资质", "team-assignment", `{"team_lead_id":"unknown","expected_version":3}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := taskAssignmentPrincipal(t, "technical_director")
			if tc.name == "无数据范围" {
				p.DataScopes = nil
			}
			if tc.name == "缺少精确权限" {
				delete(p.Permissions, "project.team.assign")
			}
			handler, repository := taskAssignmentHandler(p)
			if tc.name == "服务项不存在" {
				repository.items = nil
			}
			response := perform(handler, http.MethodPost,
				"/api/v1/service-items/SI-1/"+tc.path, tc.body)
			if response.Code != tc.want || len(repository.events) != 0 {
				t.Fatalf("HTTP %d，期望 %d；事件数=%d：%s",
					response.Code, tc.want, len(repository.events), response.Body)
			}
		})
	}
}
