package platform

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOwnerDirectoryClientUsesScopeAndParsesEnvelope(t *testing.T) {
	var query string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/oauth2/token":
			clientID, secret, ok := r.BasicAuth()
			if !ok || clientID != "client" || secret != "secret" || r.FormValue("grant_type") != "client_credentials" || r.FormValue("scope") != ownerDirectoryScope {
				t.Fatalf("invalid token request")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"Bearer","expires_in":3600,"scope":"owner_directory.read"}`))}, nil
		case "/api/v1/internal/owner-directory":
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q", got)
			}
			query = r.URL.RawQuery
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"OK","data":{"items":[{"user_id":"user-1","display_name":"张三","organizations":[{"organization_id":"org-1","organization_name":"总部","is_primary":true}]}],"page":1,"page_size":50,"total":1}}`))}, nil
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		return nil, nil
	})

	directory := &ownerDirectoryClient{
		service:  newServiceClient("http://platform.test", "client", "secret"),
		endpoint: "http://platform.test/api/v1/internal/owner-directory",
		scope:    ownerDirectoryScope,
	}
	directory.service.client.Transport = transport
	page, err := directory.List(context.Background(), OwnerDirectoryQuery{
		Keyword: "张三", RoleCodes: []string{" team_lead ", "team_lead", "", "project_manager"}, Page: 1, PageSize: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != "user-1" || page.Items[0].DisplayName != "张三" {
		t.Fatalf("unexpected page: %#v", page)
	}
	if len(page.Items[0].Organizations) != 1 || page.Items[0].Organizations[0].OrganizationID != "org-1" {
		t.Fatalf("unexpected organizations: %#v", page.Items[0].Organizations)
	}
	if !strings.Contains(query, "keyword=%E5%BC%A0%E4%B8%89") {
		t.Fatalf("keyword query = %q", query)
	}
	// 单页上限 50 必须生效，避免一次拉取过多平台人员。
	if !strings.Contains(query, "page_size=50") {
		t.Fatalf("page_size query = %q", query)
	}
	// 角色过滤以重复参数发出，且空白值与重复值不进入查询串。
	parsed, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query %q: %v", query, err)
	}
	if got := strings.Join(parsed["role_code"], ","); got != "team_lead,project_manager" {
		t.Fatalf("role_code query = %q, want %q", got, "team_lead,project_manager")
	}
}

func TestNewOwnerDirectoryReturnsNilWithoutCredentials(t *testing.T) {
	if NewOwnerDirectory("http://platform.test", "", "", "", "") != nil {
		t.Fatal("owner directory must stay disabled without credentials")
	}
	// endpoint 为空时回退到平台基址上的内部目录路径。
	directory := NewOwnerDirectory("http://platform.test", "", "client", "secret", "")
	if directory == nil {
		t.Fatal("owner directory must be enabled when credentials are present")
	}
	if got := directory.(*ownerDirectoryClient).endpoint; got != "http://platform.test"+ownerDirectoryPath {
		t.Fatalf("fallback endpoint = %q", got)
	}
}
