package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/oklog/ulid/v2"
)

type identity struct {
	p   platform.Principal
	err error
}

type integrationVerifier struct {
	err      error
	token    string
	identity platform.ServiceTokenIdentity
}

func (v *integrationVerifier) VerifyClientCredentials(_ context.Context, token string) (platform.ServiceTokenIdentity, error) {
	v.token = token
	if v.identity.TenantID == "" {
		v.identity = platform.ServiceTokenIdentity{TenantID: "tenant-contract", ApplicationCode: "contract_management", EnvironmentCode: "test"}
	}
	return v.identity, v.err
}

func (i identity) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return i.p, i.err
}

type repo struct {
	projects       []domain.Project
	items          []domain.ServiceItem
	rules          []domain.Rule
	events         []domain.DeliveryEvent
	capabilities   []domain.Capability
	dashboard      domain.Dashboard
	dashboardScope platform.ScopeFilter
}

func (r *repo) FindProjectByContractVersion(_ context.Context, filter platform.ScopeFilter, contract, version string) (domain.Project, error) {
	for _, p := range r.projects {
		if p.TenantID == filter.TenantID && p.Contract == contract && p.ContractVersion == version {
			return p, nil
		}
	}
	return domain.Project{}, application.ErrNotFound
}
func (r *repo) ActivateContract(_ context.Context, p domain.Project, items []domain.ServiceItem, event domain.DeliveryEvent) error {
	r.projects = append(r.projects, p)
	r.items = append(r.items, items...)
	r.events = append(r.events, event)
	return nil
}
func (r *repo) SyncContractStampStatus(_ context.Context, p domain.Project, uploaded bool, event domain.DeliveryEvent) error {
	for index := range r.projects {
		if r.projects[index].ID == p.ID {
			if uploaded {
				r.projects[index].Health = "正常"
			} else {
				r.projects[index].Health = "关注"
			}
		}
	}
	r.events = append(r.events, event)
	return nil
}
func (r *repo) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}
func (r *repo) ListDeliveryEvents(_ context.Context, _ platform.ScopeFilter, project string) ([]domain.DeliveryEvent, error) {
	return r.events, nil
}
func (r *repo) FindProjectForDeviation(_ context.Context, _ platform.ScopeFilter, _ string) (string, error) {
	if len(r.projects) == 0 {
		return "", application.ErrNotFound
	}
	return r.projects[0].ID, nil
}
func (r *repo) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	r.capabilities = append(r.capabilities, item)
	return item, nil
}
func (r *repo) ListCapabilities(_ context.Context, tenant, typ string) ([]domain.Capability, error) {
	return r.capabilities, nil
}
func (r *repo) FindCapabilities(_ context.Context, tenant, at string, ids []string) ([]domain.Capability, error) {
	return r.capabilities, nil
}

func (r *repo) ListProjects(context.Context, platform.ScopeFilter, string, string) ([]domain.Project, error) {
	return r.projects, nil
}
func (r *repo) GetProject(_ context.Context, _ platform.ScopeFilter, id string) (domain.Project, error) {
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
func (r *repo) ListServiceItems(context.Context, platform.ScopeFilter, string) ([]domain.ServiceItem, error) {
	return r.items, nil
}
func (r *repo) GetServiceItem(_ context.Context, _ platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	return domain.ServiceItem{}, application.ErrNotFound
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
func (r *repo) Dashboard(_ context.Context, filter platform.ScopeFilter) (domain.Dashboard, error) {
	r.dashboardScope = filter
	if r.dashboard.StatusCounts == nil {
		r.dashboard.ProjectCount = len(r.projects)
		r.dashboard.StatusCounts = map[string]int{}
	}
	return r.dashboard, nil
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
	id := identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", DisplayName: "测试用户", Roles: []string{"admin"}, Permissions: permissions, DataScopes: []platform.DataScope{{RoleCode: "admin", ScopeType: "APPLICATION"}}, AuthorizationRevision: 1, CatalogVersion: "2"}}
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
func TestContractActivationCreatesProjectAndGroupedServiceItems(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	handler := httpapi.NewRouter(service, identity{p: platform.Principal{TenantID: "tenant-1", IdentityID: "contract_management", UserID: "contract_management", Permissions: map[string]bool{"project.contract.import": true}, DataScopes: []platform.DataScope{{RoleCode: "system_integration", ScopeType: "APPLICATION"}}}}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"contract_id":"HT-1","contract_version":"v1","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","name":"等保测评","site":"上海","batch":"B1","category":"等保","system":"核心系统","requirement":"三级","test_mode":"STANDARD"},{"source_id":"S2","name":"渗透测试","site":"上海","batch":"B1","category":"渗透测试","system":"门户","requirement":"黑盒","test_mode":"PENETRATION"},{"source_id":"S3","name":"等保复测","site":"上海","batch":"B1","category":"等保","system":"门户","requirement":"二级","test_mode":"STANDARD"}]}`
	response := perform(handler, http.MethodPost, "/api/v1/contracts/activate", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"services":2`) {
		t.Fatalf("body=%s", response.Body.String())
	}
	if len(repository.projects) != 1 || repository.projects[0].Status != "待拆解确认" || repository.projects[0].Health != "关注" {
		t.Fatalf("projects=%+v", repository.projects)
	}
	if len(repository.items) != 2 || repository.items[0].Status != "待确认" || repository.items[1].Status != "待确认" {
		t.Fatalf("items=%+v", repository.items)
	}
	if len(repository.events) != 1 || repository.events[0].Payload["stamped_contract_uploaded"] != false {
		t.Fatalf("events=%+v", repository.events)
	}

	stampedBody := `{"contract_id":"HT-1","contract_version":"v1","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","stamped_contract_uploaded":true,"services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	response = perform(handler, http.MethodPost, "/api/v1/contracts/activate", stampedBody)
	if response.Code != http.StatusCreated || repository.projects[0].Health != "正常" || len(repository.events) != 2 || repository.events[1].Type != application.EventContractStampStatus {
		t.Fatalf("status=%d projects=%+v events=%+v", response.Code, repository.projects, repository.events)
	}
}

func TestContractIntegrationAcceptsInternalRequestWithoutBrowserSession(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	// H4 后：内部投递必须携带可验证的机器令牌（浏览器会话依旧不需要）。
	verifier := &integrationVerifier{}
	handler := httpapi.NewRouter(service, identity{err: platform.ErrUnauthenticated}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	body := `{"contract_id":"HT-2","contract_version":"4","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	deliveryID, tenantID := ulid.Make().String(), "tenant-contract"
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	request.Header.Set("X-Contract-Delivery-ID", deliveryID)
	request.Header.Set("X-Contract-Tenant-ID", tenantID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(repository.projects) != 1 || repository.projects[0].TenantID != tenantID {
		t.Fatalf("projects=%+v", repository.projects)
	}
}

func TestContractIntegrationRejectsMissingRoutingHeaders(t *testing.T) {
	// H4 后：先通过机器令牌校验，再按缺失路由头拒绝 400。
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: &integrationVerifier{}},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestContractIntegrationRequiresVerifiedKeycloakMachineTokenWhenEnabled(t *testing.T) {
	service := &application.Service{Repo: &repo{}}
	verifier := &integrationVerifier{}
	handler := httpapi.NewRouter(service, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	body := `{"contract_id":"HT-2","contract_version":"4","contract_name":"年度测评","customer":"示例客户","effective_at":"2026-08-10T00:00:00Z","services":[{"source_id":"S1","site":"上海","batch":"B1","category":"等保","system":"核心系统","test_mode":"STANDARD"}]}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	request.Header.Set("Authorization", "Bearer verified-machine-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || verifier.token != "verified-machine-token" {
		t.Fatalf("verified bearer status=%d token=%q body=%s", response.Code, verifier.token, response.Body.String())
	}
}

func TestContractIntegrationFailsClosedWhenBearerVerifierIsUnavailable(t *testing.T) {
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestContractIntegrationRejectsInvalidKeycloakMachineToken(t *testing.T) {
	verifier := &integrationVerifier{err: errors.New("invalid signature")}
	handler := httpapi.NewRouter(&application.Service{Repo: &repo{}}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.RouterOptions{
		ContractIntegration: &httpapi.ContractIntegrationOptions{Enabled: true, BearerVerifier: verifier},
	})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/contracts/activate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer invalid-machine-token")
	request.Header.Set("X-Contract-Delivery-ID", ulid.Make().String())
	request.Header.Set("X-Contract-Tenant-ID", "tenant-contract")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDashboardIntegrationReturnsTenantScopedProjectMetrics(t *testing.T) {
	repository := &repo{dashboard: domain.Dashboard{
		ProjectCount:     6,
		InFlightProjects: 4,
		RiskProjects:     2,
		ServiceItems:     18,
		StatusCounts:     map[string]int{"实施中": 4, "已完成": 2},
	}}
	handler := httpapi.NewRouter(
		&application.Service{Repo: repository},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.RouterOptions{DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        true,
			BearerVerifier: &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant-dashboard", ApplicationCode: "data_analysis", EnvironmentCode: "test"}},
		}},
	)

	request := httptest.NewRequest(http.MethodGet, "/internal/v1/dashboard", nil)
	request.Header.Set("Authorization", "Bearer dashboard-machine-token")
	request.Header.Set("X-DA-Tenant-ID", "spoofed-tenant")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.dashboardScope.TenantID != "tenant-dashboard" || !repository.dashboardScope.AllowAll {
		t.Fatalf("dashboard scope=%+v", repository.dashboardScope)
	}
	var payload struct {
		Data domain.Dashboard `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ProjectCount != 6 || payload.Data.InFlightProjects != 4 || payload.Data.RiskProjects != 2 || payload.Data.ServiceItems != 18 || payload.Data.StatusCounts["实施中"] != 4 {
		t.Fatalf("dashboard=%+v", payload.Data)
	}
}

func TestDashboardIntegrationUsesVerifiedTenantWithoutRoutingHeader(t *testing.T) {
	handler := httpapi.NewRouter(
		&application.Service{Repo: &repo{}},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpapi.RouterOptions{DashboardIntegration: &httpapi.DashboardIntegrationOptions{
			Enabled:        true,
			BearerVerifier: &integrationVerifier{identity: platform.ServiceTokenIdentity{TenantID: "tenant-dashboard", ApplicationCode: "data_analysis", EnvironmentCode: "test"}},
		}},
	)
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/dashboard", nil)
	request.Header.Set("Authorization", "Bearer dashboard-machine-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFieldCheckInRejectsInvalidGPS(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.field.execute": true}, nil), http.MethodPost, "/api/v1/service-items/SI-1/check-in", `{"latitude":120,"longitude":31,"occurred_at":"2026-08-10T00:00:00Z"}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
func TestMissingPermissionIsForbidden(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodPost, "/api/v1/projects", `{"name":"越权","customer":"客户","contract":"HT-1"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
}
func TestPermissionWithoutDataScopeIsForbidden(t *testing.T) {
	repository := &repo{}
	service := &application.Service{Repo: repository}
	principal := platform.Principal{TenantID: "tenant-1", IdentityID: "user-1", UserID: "user-1", Permissions: map[string]bool{"project.read": true}, AuthorizationRevision: 1}
	handler := httpapi.NewRouter(service, identity{p: principal}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	response := perform(handler, http.MethodGet, "/api/v1/projects", "")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "PM_SCOPE_FORBIDDEN") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMeReturnsStableIdentityAndDataScopes(t *testing.T) {
	response := perform(router(t, map[string]bool{"project.read": true}, nil), http.MethodGet, "/api/v1/auth/me", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"identity_id", "person_id", "data_scopes", "authorization_revision", "catalog_version"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("response missing %s: %s", expected, response.Body.String())
		}
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

func TestRequestIDReplacesInvalidClientValue(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "untrusted request id")
	response := httptest.NewRecorder()
	httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(response, request)

	if _, err := ulid.ParseStrict(response.Header().Get("X-Request-ID")); err != nil {
		t.Fatalf("generated X-Request-ID is not a ULID: %v", err)
	}
}

func TestHealthReportsWhetherPlatformAuditIsEnabled(t *testing.T) {
	for name, reporter := range map[string]platform.AuditReporter{
		"disabled": nil,
		"enabled":  &audit{},
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(httpapi.NewRouter(nil, nil, reporter, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/healthz", "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d", response.Code)
			}
			var payload struct {
				Data map[string]string `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if got := payload.Data["audit"]; got != name {
				t.Fatalf("audit=%q, want %q", got, name)
			}
		})
	}
}

func TestReadinessFailsWhenRequiredAuditIsDisabled(t *testing.T) {
	t.Setenv("PLATFORM_AUDIT_REQUIRED", "true")
	response := perform(httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), http.MethodGet, "/readyz", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d; body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data["audit"] != "disabled" || payload.Data["status"] != "not_ready" {
		t.Fatalf("readiness=%v", payload.Data)
	}
}

func TestRequestIDPreservesValidClientULID(t *testing.T) {
	id := ulid.Make().String()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", id)
	response := httptest.NewRecorder()
	httpapi.NewRouter(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != id {
		t.Fatalf("X-Request-ID = %q, want %q", got, id)
	}
}
