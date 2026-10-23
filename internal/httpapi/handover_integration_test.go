package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/j-s-te/project-management/internal/handover"
	"github.com/j-s-te/project-management/internal/platform"
)

type handoverVerifierStub struct {
	identity platform.ServiceTokenIdentity
	err      error
}

func (stub handoverVerifierStub) VerifyClientCredentials(context.Context, string) (platform.ServiceTokenIdentity, error) {
	if stub.err != nil {
		return platform.ServiceTokenIdentity{}, stub.err
	}
	return stub.identity, nil
}

// handoverRepositoryStub 实现交接端口：未覆写的方法不会被调用（本用例只走冻结与校验路径）。
type handoverRepositoryStub struct {
	freezeTenant string
	freezeUser   string
	responsibles []handover.Responsibility
	receipts     map[string]handover.Receipt
}

func (stub *handoverRepositoryStub) CreateFreeze(_ context.Context, freeze handover.Freeze) (handover.Freeze, bool, error) {
	stub.freezeTenant, stub.freezeUser = freeze.TenantID, freeze.UserID
	return freeze, true, nil
}

func (stub *handoverRepositoryStub) ReleaseFreeze(context.Context, string, string, string, time.Time) error {
	return nil
}

func (stub *handoverRepositoryStub) ActiveFreeze(context.Context, string, string) (handover.Freeze, bool, error) {
	return handover.Freeze{}, false, nil
}

func (stub *handoverRepositoryStub) ListResponsibilities(context.Context, string, string, string, int) ([]handover.Responsibility, error) {
	return stub.responsibles, nil
}

func (stub *handoverRepositoryStub) TransferOwnership(context.Context, handover.TransferInput, time.Time) (handover.Receipt, error) {
	return handover.Receipt{Status: handover.ReceiptApplied, AppliedVersion: 2, EvidenceRef: "pm:project:P-1"}, nil
}

func (stub *handoverRepositoryStub) FindReceipt(context.Context, string, string) (handover.Receipt, bool, error) {
	return handover.Receipt{}, false, nil
}

func handoverTestRouter(t *testing.T, repository *handoverRepositoryStub, verifier platform.ClientCredentialsTokenVerifier) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service, err := handover.NewService(repository, func(time.Time) (string, error) { return "01PMHANDOVER00000000000001", nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{handover: service}
	router := gin.New()
	if verifier == nil {
		router.POST("/internal/v1/handover/freeze", handler.handoverFreeze)
		return router
	}
	internal := router.Group("/internal/v1")
	internal.Use(handler.authenticateHandoverIntegration(HandoverIntegrationOptions{Enabled: true, BearerVerifier: verifier}))
	internal.POST("/handover/freeze", handler.handoverFreeze)
	internal.POST("/handover/transfers", handler.handoverTransfer)
	return router
}

func handoverCall(t *testing.T, router *gin.Engine, method, path, body, bearer string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	payload := map[string]any{}
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response.Code, payload
}

// 机器令牌缺失/无效必须拒绝，且不得触达交接服务。
func TestProjectHandoverRequiresBearer(t *testing.T) {
	repository := &handoverRepositoryStub{}
	router := handoverTestRouter(t, repository, handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "tenant-1"}})

	status, payload := handoverCall(t, router, http.MethodPost, "/internal/v1/handover/freeze", `{}`, "")
	if status != http.StatusUnauthorized || payload["code"] != "PM_HANDOVER_BEARER_REQUIRED" {
		t.Fatalf("missing bearer → %d %v", status, payload)
	}
	invalid := handoverTestRouter(t, repository, handoverVerifierStub{err: errors.New("bad token")})
	status, payload = handoverCall(t, invalid, http.MethodPost, "/internal/v1/handover/freeze", `{}`, "token")
	if status != http.StatusUnauthorized || payload["code"] != "PM_HANDOVER_BEARER_INVALID" {
		t.Fatalf("invalid bearer → %d %v", status, payload)
	}
	if repository.freezeUser != "" {
		t.Fatal("unauthenticated request must not reach the service")
	}
}

// 租户来自令牌：请求体试图覆盖必须 403。
func TestProjectHandoverBindsTenantFromToken(t *testing.T) {
	repository := &handoverRepositoryStub{}
	router := handoverTestRouter(t, repository, handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "tenant-1"}})

	status, payload := handoverCall(t, router, http.MethodPost, "/internal/v1/handover/freeze",
		`{"tenant_id":"tenant-attacker","batch_id":"b1","request_id":"r1","user_id":"u1"}`, "machine-token")
	if status != http.StatusForbidden || payload["code"] != "PM_HANDOVER_TENANT_MISMATCH" {
		t.Fatalf("tenant override → %d %v", status, payload)
	}

	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/freeze",
		`{"batch_id":"b1","request_id":"r1","user_id":"u1"}`, "machine-token")
	if status != http.StatusOK {
		t.Fatalf("freeze → %d %v", status, payload)
	}
	if repository.freezeTenant != "tenant-1" || repository.freezeUser != "u1" {
		t.Fatalf("service saw tenant=%q user=%q", repository.freezeTenant, repository.freezeUser)
	}
	if data, ok := payload["data"].(map[string]any); !ok || data["system"] != "project" || data["frozen"] != true {
		t.Fatalf("freeze payload = %v", payload)
	}
}

// 参数不完整 → 422；正常转移 → 200 且回执字段齐全。
func TestProjectHandoverTransferValidationAndReceipt(t *testing.T) {
	repository := &handoverRepositoryStub{}
	router := handoverTestRouter(t, repository, handoverVerifierStub{identity: platform.ServiceTokenIdentity{TenantID: "tenant-1"}})

	status, payload := handoverCall(t, router, http.MethodPost, "/internal/v1/handover/transfers", `{"item_id":"i1"}`, "machine-token")
	if status != http.StatusUnprocessableEntity || payload["code"] != "PM_HANDOVER_VALIDATION" {
		t.Fatalf("incomplete transfer → %d %v", status, payload)
	}

	status, payload = handoverCall(t, router, http.MethodPost, "/internal/v1/handover/transfers",
		`{"batch_id":"b1","item_id":"i1","resource_type":"project","resource_id":"P-1","from_user_id":"u1","to_user_id":"u2","expected_version":1,"idempotency_key":"handover:b1:i1"}`, "machine-token")
	if status != http.StatusOK {
		t.Fatalf("transfer → %d %v", status, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["status"] != handover.ReceiptApplied || data["evidence_ref"] != "pm:project:P-1" {
		t.Fatalf("transfer payload = %v", payload)
	}
}
