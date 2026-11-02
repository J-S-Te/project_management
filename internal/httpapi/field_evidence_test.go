package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/application"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/httpapi"
	"github.com/j-s-te/project-management/internal/platform"
)

// BIZ-PROJECT-06：缺项判定下推服务端后，前端只消费 GET /service-items/:id/field-evidence，
// 完成动作由服务端门禁按证据与说明的一致性把关。

func fieldEvidenceRouter(t *testing.T, repository *repo, p platform.Principal) http.Handler {
	t.Helper()
	service := &application.Service{Repo: repository, Contracts: approvedContractVerifier{}}
	return httpapi.NewRouter(service, identity{p: p}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func fieldEvidencePrincipal(userID string, permissions map[string]bool, scopes []platform.DataScope) platform.Principal {
	return platform.Principal{
		TenantID: "tenant-1", IdentityID: userID, UserID: userID, Roles: []string{"project-manager"},
		Permissions: permissions, DataScopes: scopes, AuthorizationRevision: 1, CatalogVersion: "2",
	}
}

func fieldEvidenceEvent(itemID, eventType string, payload map[string]any) domain.DeliveryEvent {
	if payload == nil {
		payload = map[string]any{}
	}
	return domain.DeliveryEvent{
		ID: "EV-" + eventType, TenantID: "tenant-1", ProjectID: "PJ-FIELD-1", ServiceItemID: itemID,
		Type: eventType, ActorUserID: "engineer-1", Payload: payload, CreatedAt: time.Now().UTC(),
	}
}

func TestServiceItemFieldEvidenceEndpointServesServerSideGaps(t *testing.T) {
	repository := &repo{
		items: []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}},
		events: []domain.DeliveryEvent{
			fieldEvidenceEvent("SI-1", application.EventFieldCheckedIn, map[string]any{"latitude": 30.2741, "longitude": 120.1551, "accuracy_meters": 12.5}),
			fieldEvidenceEvent("SI-1", application.EventFieldRecordSubmitted, map[string]any{"evidence_files": []any{map[string]any{"file_id": "FILE-RECORD-1"}}}),
			// 另一个服务项的事件不得参与判定。
			fieldEvidenceEvent("SI-OTHER", application.EventFieldSignatureCaptured, nil),
		},
	}
	handler := fieldEvidenceRouter(t, repository, fieldEvidencePrincipal("reader-1",
		map[string]bool{"project.read": true},
		[]platform.DataScope{{RoleCode: "project-manager", ScopeType: "APPLICATION"}}))

	response := perform(handler, http.MethodGet, "/api/v1/service-items/SI-1/field-evidence", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data application.ServiceItemFieldEvidence `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.ServiceItemID != "SI-1" || body.Data.EvidenceComplete || body.Data.State != application.FieldEvidenceStateIncomplete {
		t.Fatalf("evidence = %+v", body.Data)
	}
	if len(body.Data.Gaps) != 1 || body.Data.Gaps[0] != application.EventFieldSignatureCaptured {
		t.Fatalf("gaps = %+v", body.Data.Gaps)
	}
	if len(body.Data.Missing) != 1 || body.Data.Missing[0] != "电子签名" {
		t.Fatalf("missing labels = %+v", body.Data.Missing)
	}
	if len(body.Data.Evidence) != 2 {
		t.Fatalf("evidence details = %+v", body.Data.Evidence)
	}
	for _, entry := range body.Data.Evidence {
		if entry.Type == application.EventFieldRecordSubmitted && entry.FileRef != "FILE-RECORD-1" {
			t.Fatalf("record evidence file ref = %q", entry.FileRef)
		}
	}
	if body.Data.GatePolicy != application.FieldIncompleteReasonGateEnforce {
		t.Fatalf("gate policy = %q, want enforce by default", body.Data.GatePolicy)
	}
}

func TestServiceItemFieldEvidenceRequiresReadPermissionAndDataScope(t *testing.T) {
	repository := &repo{items: []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}}}
	path := "/api/v1/service-items/SI-1/field-evidence"

	denied := perform(fieldEvidenceRouter(t, repository, fieldEvidencePrincipal("engineer-1",
		map[string]bool{"project.field.execute": true},
		[]platform.DataScope{{RoleCode: "engineer", ScopeType: "APPLICATION"}})), http.MethodGet, path, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing project.read status=%d body=%s", denied.Code, denied.Body.String())
	}

	// 有权限但没有任何数据范围：越权/跨范围读取必须 403，而不是返回空判定。
	noScope := perform(fieldEvidenceRouter(t, repository, fieldEvidencePrincipal("engineer-1",
		map[string]bool{"project.read": true}, nil)), http.MethodGet, path, "")
	if noScope.Code != http.StatusForbidden {
		t.Fatalf("empty data scope status=%d body=%s", noScope.Code, noScope.Body.String())
	}

	// 跨租户：事件不在本租户，判定不得串味。
	crossTenant := &repo{
		items:  []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}},
		events: []domain.DeliveryEvent{{ID: "EV-X", TenantID: "tenant-2", ProjectID: "PJ-FIELD-1", ServiceItemID: "SI-1", Type: application.EventFieldCheckedIn, Payload: map[string]any{}, CreatedAt: time.Now().UTC()}},
	}
	scoped := perform(fieldEvidenceRouter(t, crossTenant, fieldEvidencePrincipal("reader-1",
		map[string]bool{"project.read": true},
		[]platform.DataScope{{RoleCode: "project-manager", ScopeType: "APPLICATION"}})), http.MethodGet, path, "")
	if scoped.Code != http.StatusOK {
		t.Fatalf("cross-tenant read status=%d body=%s", scoped.Code, scoped.Body.String())
	}
	var body struct {
		Data application.ServiceItemFieldEvidence `json:"data"`
	}
	if err := json.Unmarshal(scoped.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Evidence) != 0 || body.Data.EvidenceComplete || len(body.Data.Gaps) != 3 {
		t.Fatalf("cross-tenant evidence leaked into judgement: %+v", body.Data)
	}
}

func TestCompleteServiceItemFieldRejectsInconsistentGapReasonOverHTTP(t *testing.T) {
	items := []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}}
	complete := []domain.DeliveryEvent{
		fieldEvidenceEvent("SI-1", application.EventFieldCheckedIn, nil),
		fieldEvidenceEvent("SI-1", application.EventFieldRecordSubmitted, nil),
		fieldEvidenceEvent("SI-1", application.EventFieldSignatureCaptured, nil),
	}
	permissions := map[string]bool{"project.field.complete": true, "project.read": true}
	scopes := []platform.DataScope{{RoleCode: "project-manager", ScopeType: "APPLICATION"}}

	completeRepository := &repo{items: items, events: complete}
	completeHandler := fieldEvidenceRouter(t, completeRepository, fieldEvidencePrincipal("manager-1", permissions, scopes))
	// 证据齐全却写缺项说明：422，且不得写入完成事件。
	conflict := perform(completeHandler, http.MethodPost, "/api/v1/service-items/SI-1/field-complete",
		`{"incomplete_reason":"客户禁止留存现场照片"}`)
	if conflict.Code != http.StatusUnprocessableEntity || !strings.Contains(conflict.Body.String(), "现场证据已齐全") {
		t.Fatalf("complete evidence with reason status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if len(completeRepository.events) != len(complete) {
		t.Fatalf("rejected completion wrote events: %+v", completeRepository.events)
	}
	if allowed := perform(completeHandler, http.MethodPost, "/api/v1/service-items/SI-1/field-complete", "{}"); allowed.Code != http.StatusOK {
		t.Fatalf("complete without reason status=%d body=%s", allowed.Code, allowed.Body.String())
	}

	gappedRepository := &repo{items: items, events: complete[:1]}
	gappedHandler := fieldEvidenceRouter(t, gappedRepository, fieldEvidencePrincipal("manager-1", permissions, scopes))
	missingReason := perform(gappedHandler, http.MethodPost, "/api/v1/service-items/SI-1/field-complete", "{}")
	if missingReason.Code != http.StatusUnprocessableEntity || !strings.Contains(missingReason.Body.String(), "现场证据不完整") {
		t.Fatalf("incomplete evidence without reason status=%d body=%s", missingReason.Code, missingReason.Body.String())
	}
	withReason := perform(gappedHandler, http.MethodPost, "/api/v1/service-items/SI-1/field-complete",
		`{"incomplete_reason":"客户现场禁止拍照，已由客户书面确认"}`)
	if withReason.Code != http.StatusOK {
		t.Fatalf("incomplete evidence with reason status=%d body=%s", withReason.Code, withReason.Body.String())
	}
	last := gappedRepository.events[len(gappedRepository.events)-1]
	if last.Type != application.EventFieldCompleted || last.Payload["evidence_complete"] != false || last.Payload["missing_evidence"] == nil {
		t.Fatalf("completion payload = %+v", last.Payload)
	}
}

func TestServiceItemFieldEvidenceWindowExceededFailsClosedOverHTTP(t *testing.T) {
	repository := &repo{
		items:          []domain.ServiceItem{{ID: "SI-1", ProjectID: "PJ-FIELD-1", Status: "实施中"}},
		eventWindowErr: application.ErrFieldEvidenceWindowExceeded,
	}
	handler := fieldEvidenceRouter(t, repository, fieldEvidencePrincipal("manager-1",
		map[string]bool{"project.field.complete": true, "project.read": true},
		[]platform.DataScope{{RoleCode: "project-manager", ScopeType: "APPLICATION"}}))

	read := perform(handler, http.MethodGet, "/api/v1/service-items/SI-1/field-evidence", "")
	if read.Code != http.StatusInternalServerError || !strings.Contains(read.Body.String(), "PM_FIELD_EVIDENCE_WINDOW_EXCEEDED") {
		t.Fatalf("evidence read status=%d body=%s", read.Code, read.Body.String())
	}
	completion := perform(handler, http.MethodPost, "/api/v1/service-items/SI-1/field-complete", "{}")
	if completion.Code != http.StatusInternalServerError || !strings.Contains(completion.Body.String(), "PM_FIELD_EVIDENCE_WINDOW_EXCEEDED") {
		t.Fatalf("completion status=%d body=%s", completion.Code, completion.Body.String())
	}
	if len(repository.events) != 0 {
		t.Fatalf("unverifiable completion must not write events: %+v", repository.events)
	}
}
