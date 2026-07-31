package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
	"github.com/j-s-te/project-management/internal/workflows"
	"go.temporal.io/sdk/client"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type identity struct {
	p   platform.Principal
	err error
}

func (i identity) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return i.p, i.err
}

type repo struct {
	projects []domain.Project
	items    []domain.ServiceItem
	rules    []domain.Rule
}

func (r *repo) ListProjects(context.Context, string, string, string) ([]domain.Project, error) {
	return r.projects, nil
}
func (r *repo) GetProject(_ context.Context, _ string, id string) (domain.Project, error) {
	for _, p := range r.projects {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Project{}, application.ErrNotFound
}
func (r *repo) CreateProject(_ context.Context, p domain.Project) error {
	r.projects = append(r.projects, p)
	return nil
}
func (r *repo) ListServiceItems(context.Context, string, string) ([]domain.ServiceItem, error) {
	return r.items, nil
}
func (r *repo) ConfirmServiceItems(_ context.Context, _ string, ids []string, _ string) ([]domain.ServiceItem, error) {
	return r.items, nil
}
func (r *repo) ListRules(context.Context, string, string) ([]domain.Rule, error) { return r.rules, nil }
func (r *repo) CreateRule(_ context.Context, item domain.Rule) (domain.Rule, error) {
	item.ID = 1
	r.rules = append(r.rules, item)
	return item, nil
}
func (r *repo) SetRuleEnabled(_ context.Context, _ string, id int64, enabled bool, _ string) (domain.Rule, error) {
	return domain.Rule{ID: id, Enabled: enabled}, nil
}
func (r *repo) Dashboard(context.Context, string) (domain.Dashboard, error) {
	return domain.Dashboard{ProjectCount: len(r.projects), StatusCounts: map[string]int{}}, nil
}

type executor struct{ items []domain.ServiceItem }

func (e executor) ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error) {
	return run{items: e.items}, nil
}

type run struct{ items []domain.ServiceItem }

func (run) GetID() string    { return "workflow-1" }
func (run) GetRunID() string { return "run-1" }
func (r run) Get(_ context.Context, value any) error {
	result := value.(*workflows.ConfirmServiceItemsResult)
	result.Items = r.items
	return nil
}
func (r run) GetWithOptions(ctx context.Context, value any, _ client.WorkflowRunGetOptions) error {
	return r.Get(ctx, value)
}

type audit struct{ events []platform.AuditEvent }

func (a *audit) Report(_ context.Context, e platform.AuditEvent) error {
	a.events = append(a.events, e)
	return nil
}

func router(t *testing.T, permissions map[string]bool, reporter platform.AuditReporter) http.Handler {
	t.Helper()
	repository := &repo{items: []domain.ServiceItem{{ID: "SI-1", Status: "待分配"}}}
	service := &application.Service{Repo: repository, Temporal: executor{items: repository.items}, TaskQueue: "test"}
	id := identity{p: platform.Principal{TenantID: "tenant-1", UserID: "user-1", DisplayName: "测试用户", Roles: []string{"admin"}, Permissions: permissions}}
	return httpapi.NewRouter(service, id, reporter, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
func perform(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProjectCreationAndRead(t *testing.T) {
	handler := router(t, map[string]bool{"project.create": true, "project.read": true}, nil)
	response := perform(handler, http.MethodPost, "/api/v1/projects", `{"name":"新项目","customer":"示例客户","contract":"HT-1"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Data domain.Project `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.ID == "" {
		t.Fatal("project id missing")
	}
	response = perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
}
func TestServiceItemConfirmationUsesWorkflow(t *testing.T) {
	response := perform(router(t, map[string]bool{"service_item.confirm": true}, nil), http.MethodPost, "/api/v1/service-items/confirm", `{"ids":["SI-1"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "SI-1") {
		t.Fatalf("body=%s", response.Body.String())
	}
}
func TestMissingPermissionIsForbidden(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodPost, "/api/v1/projects", `{"name":"越权","customer":"客户","contract":"HT-1"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}
func TestWriteIsReportedToAudit(t *testing.T) {
	reporter := &audit{}
	response := perform(router(t, map[string]bool{"project.create": true}, reporter), http.MethodPost, "/api/v1/projects", `{"name":"审计项目","customer":"客户","contract":"HT-1"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d", response.Code)
	}
	if len(reporter.events) != 1 || reporter.events[0].ActorID != "user-1" {
		t.Fatalf("events=%+v", reporter.events)
	}
}
func TestUnauthenticated(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	handler := httpapi.NewRouter(service, identity{err: platform.ErrUnauthenticated}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}
