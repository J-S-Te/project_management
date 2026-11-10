package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// TestExpenseDirectorHTTPUsesExactProjectRolesAndSalesOrganization 验证真实目录客户端发送精确角色并按各自组织收敛。
// 参数：t为测试上下文。
// 返回值：无；角色参数、组织来源或目录隐私边界不符时失败。
func TestExpenseDirectorHTTPUsesExactProjectRolesAndSalesOrganization(t *testing.T) {
	roleCalls := map[string]int{}
	var callsMutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/token" {
			if r.FormValue("scope") != "owner_directory.read" {
				t.Errorf("wrong scope")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 3600, "scope": "owner_directory.read"})
			return
		}
		if r.URL.Path != "/api/v1/internal/owner-directory" || r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(401)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		result := platform.OwnerDirectoryPage{Page: page, PageSize: size, Items: []platform.OwnerDirectoryUser{}}
		if user := r.URL.Query().Get("user_id"); user != "" {
			if user == "pm" && (len(r.URL.Query()["role_code"]) != 1 || r.URL.Query().Get("role_code") != "project_manager") {
				t.Errorf("manager role not checked")
				w.WriteHeader(400)
				return
			}
			result.Items = []platform.OwnerDirectoryUser{{UserID: user, Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "sales-org"}}}}
		} else {
			roles := r.URL.Query()["role_code"]
			if len(roles) != 1 {
				t.Errorf("role query not exact: %v", roles)
				w.WriteHeader(400)
				return
			}
			callsMutex.Lock()
			roleCalls[roles[0]]++
			callsMutex.Unlock()
			switch roles[0] {
			case "technical_director":
				result.Items = []platform.OwnerDirectoryUser{{UserID: "tech", DisplayName: "private-directory-name", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "technical-org"}}}}
			case "sales_director":
				result.Items = []platform.OwnerDirectoryUser{{UserID: "wrong-sales", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "technical-org"}}}, {UserID: "sales", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "sales-org"}}}}
			default:
				t.Errorf("unexpected role %q", roles[0])
				w.WriteHeader(400)
				return
			}
		}
		result.Total = int64(len(result.Items))
		json.NewEncoder(w).Encode(map[string]any{"code": "OK", "data": result})
	}))
	defer server.Close()
	service := Service{Personnel: platform.NewOwnerDirectory(server.URL, "", "client", "secret", "")}
	project := domain.Project{ID: "p", TenantID: "t", OwnerOrgID: "technical-org", OwnerIdentityID: "actor", ManagerIdentityID: "pm", Version: 1}
	reference := domain.ExpenseReference(project, nil, "actor", nil, time.Now().UTC())
	result, err := service.resolveExpenseApprovers(context.Background(), project, reference, "actor", "sales-org")
	if err != nil {
		t.Fatal(err)
	}
	callsMutex.Lock()
	defer callsMutex.Unlock()
	if len(result.RequiredApproverRefs) != 3 || roleCalls["technical_director"] != 1 || roleCalls["sales_director"] != 1 {
		t.Fatal(result.RequiredApproverRefs, roleCalls)
	}
	for _, target := range result.RequiredApproverRefs {
		if target.SubjectID == "wrong-sales" {
			t.Fatal("technical org used for sales")
		}
	}
	raw, _ := json.Marshal(result.RequiredApproverRefs)
	if strings.Contains(string(raw), "private-directory-name") {
		t.Fatal("directory display name leaked into role proof")
	}
}
