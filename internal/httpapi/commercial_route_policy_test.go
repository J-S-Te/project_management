package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/J-S-Te/license-core"
	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/handover"
	"github.com/j-s-te/project-management/internal/platform"
)

type recordingCommercialGate struct{ operation core.Operation }

func (gate *recordingCommercialGate) Check(ctx context.Context, operation core.Operation) error {
	gate.operation = operation
	return expiredCommercialGate{}.Check(ctx, operation)
}

func TestExpiredProjectRegisteredHistoryKeepsMachineAndPermissionGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	actor := platform.Principal{TenantID: "tenant-1", UserID: "viewer"}
	service, err := handover.NewService(&handoverRepositoryStub{}, func(time.Time) (string, error) { return "handover-id", nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(&application.Service{}, projectLicenseIdentity{&actor}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), RouterOptions{
		CommercialLicense: expiredCommercialGate{}, HandoverService: service,
		HandoverIntegration: &HandoverIntegrationOptions{Enabled: true, BearerVerifier: handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "tenant-1"}}},
	})
	for _, tc := range []struct {
		method, path, bearer, origin, code string
		status                             int
	}{
		{"GET", "/internal/v1/handover/responsibilities?user_id=user-1", "", "", "PM_HANDOVER_BEARER_REQUIRED", 401},
		{"GET", "/internal/v1/handover/responsibilities?tenant_id=other&user_id=user-1", "token", "", "PM_HANDOVER_TENANT_MISMATCH", 403},
		{"GET", "/internal/v1/handover/responsibilities?tenant_id=tenant-1&user_id=user-1", "token", "", "", 200},
		{"GET", "/internal/v1/handover/transfers/item-1?idempotency_key=key", "", "", "PM_HANDOVER_BEARER_REQUIRED", 401},
		{"GET", "/internal/v1/handover/transfers/item-1?tenant_id=other&idempotency_key=key", "token", "", "PM_HANDOVER_TENANT_MISMATCH", 403},
		{"GET", "/internal/v1/handover/transfers/item-1?tenant_id=tenant-1&idempotency_key=key", "token", "", "PM_HANDOVER_NOT_FOUND", 404},
		{"GET", "/api/v1/audit-outbox/failed", "", "", "PM_FORBIDDEN", 403},
		{"POST", "/api/v1/audit-outbox/failed/row-1/retry", "", "http://example.com", "PM_FORBIDDEN", 403},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)
			if w.Code != tc.status || (tc.code != "" && !strings.Contains(w.Body.String(), tc.code)) {
				t.Fatalf("status=%d body=%s; want status=%d code=%s", w.Code, w.Body.String(), tc.status, tc.code)
			}
		})
	}
}

func TestExpiredProjectExactHandoverAndAuditPolicies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		method, route, path string
		operation           core.Operation
		status              int
	}{
		{"GET", "/internal/v1/handover/responsibilities", "/internal/v1/handover/responsibilities", core.READ_HISTORY, 204},
		{"GET", "/internal/v1/handover/transfers/:item_id", "/internal/v1/handover/transfers/item-1", core.READ_HISTORY, 204},
		{"GET", "/api/v1/audit-outbox/failed", "/api/v1/audit-outbox/failed", core.ESSENTIAL_SERVICE, 204},
		{"GET", "/api/v1/projects/:id/supplement-contracts", "/api/v1/projects/PJ-1/supplement-contracts", core.READ_HISTORY, 204},
		{"POST", "/api/v1/audit-outbox/failed/:id/retry", "/api/v1/audit-outbox/failed/row-1/retry", core.ESSENTIAL_SERVICE, 204},
		{"POST", "/internal/v1/handover/freeze", "/internal/v1/handover/freeze", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/transfers", "/internal/v1/handover/transfers", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/complete", "/internal/v1/handover/complete", core.MUTATE_BUSINESS, 403},
		{"POST", "/internal/v1/handover/responsibilities", "/internal/v1/handover/responsibilities", core.MUTATE_BUSINESS, 403},
		{"POST", "/api/v1/audit-outbox/failed/:id/delete", "/api/v1/audit-outbox/failed/row-1/delete", core.MUTATE_BUSINESS, 403},
		{"GET", "/api/v1/audit-outbox/new-computation", "/api/v1/audit-outbox/new-computation", core.MUTATE_BUSINESS, 403},
	} {
		t.Run(tc.method+tc.route, func(t *testing.T) {
			gate := &recordingCommercialGate{}
			router := gin.New()
			router.Use(commercialLicenseMiddleware(gate))
			router.Handle(tc.method, tc.route, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status || gate.operation != tc.operation {
				t.Fatalf("status=%d operation=%s; want status=%d operation=%s", w.Code, gate.operation, tc.status, tc.operation)
			}
		})
	}
}
