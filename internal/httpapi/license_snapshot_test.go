package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	runtime "github.com/J-S-Te/license-core/runtime"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type signedProjectFixture struct {
	t                        *testing.T
	now, expiry              time.Time
	path                     string
	binding                  runtime.Binding
	platformKeys, vendorKeys map[string]ed25519.PublicKey
	rt                       *runtime.FileRuntime
}

func newSignedProjectFixture(t *testing.T, component string) *signedProjectFixture {
	t.Helper()
	pp, ps, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	vp, vs, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0).UTC()
	f := &signedProjectFixture{t: t, now: now, expiry: now.Add(time.Minute), path: filepath.Join(t.TempDir(), "state.json"), binding: runtime.Binding{InstanceID: "test-instance", Environment: "production", Application: "project_management", ServiceID: component}, platformKeys: map[string]ed25519.PublicKey{"platform": pp}, vendorKeys: map[string]ed25519.PublicKey{"vendor": vp}}
	f.reopen()
	license, err := core.Sign(core.License{ProtocolVersion: 1, Issuer: core.Issuer, ID: "license", Version: 1, CustomerID: "customer", ProductID: core.Product, Environment: f.binding.Environment, InstanceID: f.binding.InstanceID, IssuedAt: now.Unix() - 1000, NotBefore: now.Unix() - 100, Applications: []core.Application{{Code: f.binding.Application, NotBefore: now.Unix() - 100, ExpiresAt: f.expiry.Unix(), Kind: "FULL"}}}, "vendor", vs)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.SignSnapshot(runtime.PlatformSnapshot{Protocol: 1, InstanceID: f.binding.InstanceID, Environment: f.binding.Environment, Application: f.binding.Application, ServiceID: f.binding.ServiceID, Revision: 1, EnforcementState: runtime.Pending, MigrationEligible: true, CurrentLicenseJWS: license, HighestLicenseVersion: 1, SnapshotIssuedAt: now.Unix()}, "platform", ps)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rt.ApplySnapshot(context.Background(), snapshot, now); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *signedProjectFixture) reopen() {
	f.t.Helper()
	rt, err := runtime.NewFileRuntime(f.path, f.binding, f.platformKeys, f.vendorKeys)
	if err != nil {
		f.t.Fatal(err)
	}
	f.rt = rt
}
func (f *signedProjectFixture) Check(ctx context.Context, op core.Operation) error {
	return f.rt.Evaluate(ctx, op, f.now)
}

type projectLicenseRepo struct {
	application.Repository
	application.DeliveryRepository
	reads, claims, completes, retries, enqueues int
	afterClaim, afterComplete                   func()
	job                                         *domain.DeliveryEffectsItem
}

func (r *projectLicenseRepo) ListProjects(context.Context, platform.ScopeFilter, string, string, int, int) ([]domain.Project, int, error) {
	r.reads++
	return []domain.Project{}, 0, nil
}
func (r *projectLicenseRepo) ListRules(context.Context, string, string) ([]domain.Rule, error) {
	r.reads++
	return nil, nil
}
func (r *projectLicenseRepo) ListCapabilities(context.Context, string, string) ([]domain.Capability, error) {
	r.reads++
	return nil, nil
}
func (r *projectLicenseRepo) ListSlaOverdue(context.Context, platform.ScopeFilter) ([]domain.SlaOverdueItem, error) {
	r.reads++
	return nil, nil
}
func (r *projectLicenseRepo) EnqueueNotification(context.Context, string, domain.NotificationMessage) (bool, error) {
	r.enqueues++
	return true, nil
}
func (r *projectLicenseRepo) ClaimDeliveryEffects(context.Context, string, string, int, time.Time) ([]domain.DeliveryEffectsItem, error) {
	r.claims++
	if r.afterClaim != nil {
		r.afterClaim()
	}
	if r.job == nil {
		return nil, nil
	}
	job := *r.job
	r.job = nil
	return []domain.DeliveryEffectsItem{job}, nil
}
func (r *projectLicenseRepo) CompleteDeliveryEffects(context.Context, string, uint64, string, []domain.DeliveryEvent, []domain.NotificationMessage, time.Time) error {
	r.completes++
	if r.afterComplete != nil {
		r.afterComplete()
	}
	return nil
}
func (r *projectLicenseRepo) RetryDeliveryEffects(context.Context, string, uint64, string, time.Time, time.Time, string) error {
	r.retries++
	return nil
}

type projectLicenseIdentity struct{ principal *platform.Principal }

func (i projectLicenseIdentity) Authenticate(context.Context, *http.Request) (platform.Principal, error) {
	return *i.principal, nil
}

// 使用签名本地状态及显式认证、仓储夹具测试真实注册路由器，
// 不访问 OIDC、SQL 或生产环境。
func TestSignedMigrationExpiryProjectRegisteredHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("OIDC_REDIRECT_URI", "https://project.example.test/auth/callback")
	t.Setenv("OIDC_POST_LOGOUT_REDIRECT_URI", "")
	t.Setenv("APP_CORS_ALLOWED_ORIGINS", "")
	for _, role := range []string{"admin", "business_admin", "project_manager"} {
		t.Run(role, func(t *testing.T) {
			f := newSignedProjectFixture(t, "project-api")
			repo := &projectLicenseRepo{}
			service := &application.Service{Repo: repo}
			actor := platform.Principal{TenantID: "tenant", UserID: "actor", IdentityID: "actor", Roles: []string{role}, Permissions: map[string]bool{"project.create": true, "project.read": true, "service_item.confirm": true, "project.rollback.request": true, "project.rollback.approve": true, "project.contract.import": true, "project.resource.read": true}, DataScopes: []platform.DataScope{{ScopeType: "APPLICATION"}}}
			router := NewRouter(service, projectLicenseIdentity{&actor}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{CommercialLicense: f, ContractIntegration: &ContractIntegrationOptions{Enabled: true}})
			request := func(method, path string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(method, path, strings.NewReader("["))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", "https://project.example.test")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			if w := request("POST", "/api/v1/projects"); w.Code != 400 {
				t.Fatalf("active create validation: %d %s", w.Code, w.Body.String())
			}
			for _, stage := range []string{"boundary", "offline", "reopened"} {
				if stage == "boundary" {
					f.now = f.expiry
				} else {
					f.now = f.expiry.Add(time.Hour)
				}
				if stage == "reopened" {
					f.reopen()
				}
				before := repo.reads
				for _, path := range []string{"/api/v1/projects", "/api/v1/service-items/confirm", "/api/v1/service-items/existing/rollback-requests", "/api/v1/service-items/existing/rollback-requests/request/decision", "/api/v1/contracts/activate", "/internal/v1/contracts/activate"} {
					if w := request("POST", path); w.Code != 403 || !strings.Contains(w.Body.String(), "COMMERCIAL_LICENSE_DENIED") {
						t.Errorf("%s %s: %d %s", stage, path, w.Code, w.Body.String())
					}
				}
				if repo.reads != before {
					t.Fatal("expired HTTP writes touched repository")
				}
				for _, path := range []string{"/api/v1/projects", "/api/v1/capabilities/export", "/api/v1/auth/me", "/healthz"} {
					if w := request("GET", path); w.Code != 200 {
						t.Errorf("%s history/export %s: %d %s", stage, path, w.Code, w.Body.String())
					}
				}
			}
			actor.Permissions = nil
			before := repo.reads
			for _, path := range []string{"/api/v1/projects", "/api/v1/capabilities/export"} {
				if w := request("GET", path); w.Code != 403 {
					t.Errorf("permission bypass: %s %d", path, w.Code)
				}
			}
			if before != repo.reads {
				t.Fatal("unpermitted history/export reached repository")
			}
		})
	}
}

type expiryDuringProjectDirectory struct{ f *signedProjectFixture }

func (d expiryDuringProjectDirectory) List(_ context.Context, query platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	d.f.now = d.f.expiry
	return platform.OwnerDirectoryPage{Items: []platform.OwnerDirectoryUser{{UserID: "recipient"}}, Page: query.Page, PageSize: query.PageSize, Total: 1}, nil
}

func TestSignedProjectWorkerExpiryBeforeClaimAndCommit(t *testing.T) {
	for _, stage := range []string{"already_expired", "expires_during_directory", "expires_before_next_claim"} {
		t.Run(stage, func(t *testing.T) {
			f := newSignedProjectFixture(t, "project-sla-notifier")
			repo := &projectLicenseRepo{job: &domain.DeliveryEffectsItem{ID: 1, TenantID: "tenant", Effects: domain.DeliveryEffects{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Event: domain.DeliveryEvent{ID: "event", CreatedAt: f.now}, Targets: []string{"technical_director"}}}}
			service := &application.Service{Repo: repo, CheckBusinessLicense: func(ctx context.Context) error { return f.Check(ctx, core.MUTATE_BUSINESS) }}
			switch stage {
			case "already_expired":
				f.now = f.expiry
				f.reopen()
			case "expires_during_directory":
				service.Personnel = expiryDuringProjectDirectory{f}
			case "expires_before_next_claim":
				repo.job.Effects.Targets = nil
				repo.afterComplete = func() { f.now = f.expiry }
			}
			completed, err := service.DispatchDeliveryEffects(context.Background(), "tenant", "worker", 50, f.now)
			if !errors.Is(err, core.ErrDenied) {
				t.Fatalf("expected absolute expiry denial, got completed=%d err=%v", completed, err)
			}
			wantClaims, wantComplete := 0, 0
			if stage != "already_expired" {
				wantClaims = 1
			}
			if stage == "expires_before_next_claim" {
				wantComplete = 1
			}
			if repo.claims != wantClaims || repo.completes != wantComplete || repo.retries != 0 || completed != wantComplete {
				t.Fatalf("side effects: claims=%d completes=%d retries=%d completed=%d", repo.claims, repo.completes, repo.retries, completed)
			}
			reads := repo.reads
			if _, err := service.ScanSlaNotifications(context.Background(), "tenant", f.now); !errors.Is(err, core.ErrDenied) || repo.reads != reads || repo.enqueues != 0 {
				t.Fatalf("expired SLA scan ran: reads=%d enqueues=%d err=%v", repo.reads, repo.enqueues, err)
			}
		})
	}
}
