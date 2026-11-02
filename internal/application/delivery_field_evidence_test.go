package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// BIZ-PROJECT-06：现场完成的缺项判定必须基于该服务项的完整事件流（服务端权威口径），
// 并且"缺项说明"与判定结果必须一致。本文件的用例覆盖：判定口径、门禁四象限、
// payload 快照、越权/跨租户、事件超限与"无法核验"两条 fail-closed 路径。

type fieldEvidenceRepository struct {
	capabilityRepository
	item   domain.ServiceItem
	events []domain.DeliveryEvent
	// err 模拟仓储层失败（含事件超限），用于验证 fail-closed。
	err     error
	filters []platform.ScopeFilter
	applied []domain.DeliveryEvent
	lookups []string
}

func (r *fieldEvidenceRepository) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.filters = append(r.filters, filter)
	r.lookups = append(r.lookups, id)
	// 真实仓储按 tenant + 数据范围过滤：桩同样按租户过滤，跨租户读取必须表现为不存在。
	if filter.TenantID != "tenant-1" || r.item.ID != id {
		return domain.ServiceItem{}, ErrNotFound
	}
	return r.item, nil
}

func (r *fieldEvidenceRepository) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.applied = append(r.applied, event)
	return nil
}

func (r *fieldEvidenceRepository) ListServiceItemDeliveryEvents(_ context.Context, filter platform.ScopeFilter, itemID string) ([]domain.DeliveryEvent, error) {
	r.filters = append(r.filters, filter)
	if r.err != nil {
		return nil, r.err
	}
	events := make([]domain.DeliveryEvent, 0, len(r.events))
	for _, event := range r.events {
		// 跨租户事件不参与判定：仓储按 tenant_id 过滤，桩也必须真实过滤，
		// 否则"跨租户证据串味"这类缺陷在单测里看不出来。
		if event.ServiceItemID != itemID || (event.TenantID != "" && event.TenantID != filter.TenantID) {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

// fieldEvidenceRepositoryWithoutStream 模拟不具备按服务项读取事件流能力的仓储（历史实现）。
type fieldEvidenceRepositoryWithoutStream struct {
	capabilityRepository
	item   domain.ServiceItem
	events []domain.DeliveryEvent
}

func (r *fieldEvidenceRepositoryWithoutStream) GetServiceItem(_ context.Context, filter platform.ScopeFilter, id string) (domain.ServiceItem, error) {
	r.lastFilter = filter
	if filter.TenantID != "tenant-1" || r.item.ID != id {
		return domain.ServiceItem{}, ErrNotFound
	}
	return r.item, nil
}

func (r *fieldEvidenceRepositoryWithoutStream) ApplyDeliveryEvent(_ context.Context, event domain.DeliveryEvent) error {
	r.events = append(r.events, event)
	return nil
}

func fieldEvidencePrincipal(userID string, permissions ...string) platform.Principal {
	granted := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		granted[permission] = true
	}
	return platform.Principal{
		TenantID: "tenant-1", IdentityID: userID, UserID: userID,
		Permissions: granted,
		DataScopes:  []platform.DataScope{{RoleCode: "project-regression", ScopeType: "APPLICATION"}},
	}
}

func fieldEvidenceEvent(itemID, eventType string, payload map[string]any) domain.DeliveryEvent {
	if payload == nil {
		payload = map[string]any{}
	}
	return domain.DeliveryEvent{
		ID: "EV-" + eventType, TenantID: "tenant-1", ProjectID: "PJ-EVIDENCE-1",
		ServiceItemID: itemID, Type: eventType, ActorUserID: "engineer-1", Payload: payload,
		CreatedAt: time.Now().UTC(),
	}
}

// completeFieldEvidence 是三类证据齐全的事件流。
func completeFieldEvidence(itemID string) []domain.DeliveryEvent {
	return []domain.DeliveryEvent{
		fieldEvidenceEvent(itemID, EventFieldCheckedIn, map[string]any{"latitude": 30.2741, "longitude": 120.1551, "accuracy_meters": 12.5}),
		fieldEvidenceEvent(itemID, EventFieldRecordSubmitted, map[string]any{"evidence_files": []domain.ReportArtifactInput{{FileID: "FILE-RECORD"}}}),
		fieldEvidenceEvent(itemID, EventFieldSignatureCaptured, map[string]any{"signature_file": domain.ReportArtifactInput{FileID: "FILE-SIGN"}}),
	}
}

func TestServiceItemFieldEvidenceComputesGapsFromServiceItemStream(t *testing.T) {
	repository := &fieldEvidenceRepository{item: domain.ServiceItem{ID: "SI-EVIDENCE-1", ProjectID: "PJ-EVIDENCE-1", Status: "实施中"}}
	repository.events = completeFieldEvidence("SI-EVIDENCE-1")
	// 另一个服务项的事件不能算进本项证据，也不能因为不相干事件的存在而改变判定。
	repository.events = append(repository.events, fieldEvidenceEvent("SI-OTHER", EventFieldCheckedIn, nil))
	service := Service{Repo: repository}
	manager := fieldEvidencePrincipal("project-manager-1", "project.read")

	evidence, err := service.ServiceItemFieldEvidence(context.Background(), manager, "SI-EVIDENCE-1")
	if err != nil {
		t.Fatalf("field evidence: %v", err)
	}
	if !evidence.EvidenceComplete || evidence.State != FieldEvidenceStateComplete || len(evidence.Gaps) != 0 || len(evidence.Missing) != 0 {
		t.Fatalf("complete evidence = %+v", evidence)
	}
	if len(evidence.Evidence) != 3 || evidence.ScannedEvents != 3 {
		t.Fatalf("evidence entries = %+v scanned=%d", evidence.Evidence, evidence.ScannedEvents)
	}
	fileRefs := map[string]string{}
	for _, entry := range evidence.Evidence {
		fileRefs[entry.Type] = entry.FileRef
	}
	if fileRefs[EventFieldRecordSubmitted] != "FILE-RECORD" || fileRefs[EventFieldSignatureCaptured] != "FILE-SIGN" {
		t.Fatalf("evidence file refs = %+v", fileRefs)
	}

	// 只提交签到：缺项必须由服务端按类型精确给出，而不是"窗口内没看到"。
	partial := &fieldEvidenceRepository{item: repository.item}
	partial.events = completeFieldEvidence("SI-EVIDENCE-1")[:1]
	items, err := (&Service{Repo: partial}).ServiceItemFieldEvidence(context.Background(), manager, "SI-EVIDENCE-1")
	if err != nil {
		t.Fatalf("partial field evidence: %v", err)
	}
	if items.EvidenceComplete || items.State != FieldEvidenceStateIncomplete {
		t.Fatalf("partial evidence = %+v", items)
	}
	if strings.Join(items.Gaps, ",") != EventFieldRecordSubmitted+","+EventFieldSignatureCaptured {
		t.Fatalf("gaps = %+v", items.Gaps)
	}
	if strings.Join(items.Missing, "、") != "现场记录、电子签名" {
		t.Fatalf("missing labels = %+v", items.Missing)
	}
}

func TestFieldEvidenceAuthorizationIsReadPermissionAndScopeBound(t *testing.T) {
	repository := &fieldEvidenceRepository{item: domain.ServiceItem{ID: "SI-EVIDENCE-2", ProjectID: "PJ-EVIDENCE-2", Status: "实施中"}}
	service := Service{Repo: repository}

	// 无 project.read：直接 403，不泄露服务项是否存在。
	if _, err := service.ServiceItemFieldEvidence(context.Background(), fieldEvidencePrincipal("engineer-1", "project.field.execute"), "SI-EVIDENCE-2"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing read permission error = %v, want forbidden", err)
	}
	// 完成权限不等同于读取证据权限之外的资源授权：服务项不存在/不在数据范围按 not found 处理。
	if _, err := service.ServiceItemFieldEvidence(context.Background(), fieldEvidencePrincipal("engineer-1", "project.read"), "SI-UNKNOWN"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out-of-scope item error = %v, want not found", err)
	}
	// 读接口必须把 principal 的数据范围传下去：跨租户不能读到别人的证据。
	scope := platform.Principal{
		TenantID: "tenant-2", IdentityID: "other", UserID: "other",
		Permissions: map[string]bool{"project.read": true},
		DataScopes:  []platform.DataScope{{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-2"}},
	}
	if _, err := service.ServiceItemFieldEvidence(context.Background(), scope, "SI-EVIDENCE-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant read error = %v, want not found", err)
	}
	lastFilter := repository.filters[len(repository.filters)-1]
	if lastFilter.TenantID != "tenant-2" {
		t.Fatalf("evidence query must carry the principal tenant, got %+v", lastFilter)
	}
}

func TestCompleteServiceItemFieldGateQuadrants(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		events    []domain.DeliveryEvent
		reason    string
		wantError string
	}{
		{name: "证据不全且无说明", events: completeFieldEvidence("SI-GATE-1")[:1], reason: "", wantError: "现场证据不完整，缺少 现场记录、电子签名"},
		{name: "证据不全但有说明", events: completeFieldEvidence("SI-GATE-1")[:1], reason: "客户禁止留存签名"},
		{name: "证据齐全且无说明", events: completeFieldEvidence("SI-GATE-1"), reason: ""},
		{name: "证据齐全却写说明", events: completeFieldEvidence("SI-GATE-1"), reason: "客户禁止留存现场照片", wantError: "现场证据已齐全"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &fieldEvidenceRepository{item: domain.ServiceItem{ID: "SI-GATE-1", ProjectID: "PJ-GATE-1", Status: "实施中"}, events: testCase.events}
			service := Service{Repo: repository}
			manager := fieldEvidencePrincipal("project-manager-1", "project.field.complete")

			err := service.CompleteServiceItemField(context.Background(), manager, "SI-GATE-1", domain.FieldCompletionInput{IncompleteReason: testCase.reason})
			if testCase.wantError != "" {
				if !errors.Is(err, ErrValidation) || !strings.Contains(UserMessage(err), testCase.wantError) {
					t.Fatalf("error = %v, want validation containing %q", err, testCase.wantError)
				}
				if len(repository.applied) != 0 {
					t.Fatalf("rejected completion must not write events: %+v", repository.applied)
				}
				return
			}
			if err != nil {
				t.Fatalf("completion rejected: %v", err)
			}
			if len(repository.applied) != 1 || repository.applied[0].Type != EventFieldCompleted {
				t.Fatalf("completion events = %+v", repository.applied)
			}
		})
	}
}

func TestCompleteServiceItemFieldGateAdvisoryOnlyRecords(t *testing.T) {
	// advisory 是存量升级的过渡档：四象限一律放行，但 payload 仍要留下判定快照。
	for _, testCase := range []struct{ name, reason string }{
		{name: "证据不全且无说明", reason: ""},
		{name: "证据齐全却写说明", reason: "客户禁止留存现场照片"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &fieldEvidenceRepository{item: domain.ServiceItem{ID: "SI-ADVISORY-1", ProjectID: "PJ-ADVISORY-1", Status: "实施中"}, events: completeFieldEvidence("SI-ADVISORY-1")}
			service := Service{Repo: repository, FieldEvidenceGate: FieldEvidenceGatePolicy{Mode: FieldIncompleteReasonGateAdvisory}}
			manager := fieldEvidencePrincipal("project-manager-1", "project.field.complete")

			if err := service.CompleteServiceItemField(context.Background(), manager, "SI-ADVISORY-1", domain.FieldCompletionInput{IncompleteReason: testCase.reason}); err != nil {
				t.Fatalf("advisory completion rejected: %v", err)
			}
			if len(repository.applied) != 1 {
				t.Fatalf("advisory completion events = %+v", repository.applied)
			}
			payload := repository.applied[0].Payload
			if _, ok := payload["evidence_complete"]; !ok {
				t.Fatalf("advisory payload must still carry the evidence snapshot: %+v", payload)
			}
		})
	}
}

func TestCompleteServiceItemFieldPayloadCarriesGapSnapshot(t *testing.T) {
	repository := &fieldEvidenceRepository{item: domain.ServiceItem{ID: "SI-SNAPSHOT-1", ProjectID: "PJ-SNAPSHOT-1", Status: "实施中"}, events: completeFieldEvidence("SI-SNAPSHOT-1")[:2]}
	service := Service{Repo: repository}
	manager := fieldEvidencePrincipal("project-manager-1", "project.field.complete")

	if err := service.CompleteServiceItemField(context.Background(), manager, "SI-SNAPSHOT-1", domain.FieldCompletionInput{IncompleteReason: "  电子签名设备故障，客户同意后续补签  "}); err != nil {
		t.Fatalf("complete field: %v", err)
	}
	if len(repository.applied) != 1 {
		t.Fatalf("completion events = %+v", repository.applied)
	}
	payload := repository.applied[0].Payload
	if payload["incomplete_reason"] != "电子签名设备故障，客户同意后续补签" {
		t.Fatalf("incomplete reason = %#v", payload["incomplete_reason"])
	}
	if payload["evidence_complete"] != false || payload["evidence_state"] != FieldEvidenceStateIncomplete {
		t.Fatalf("evidence snapshot = %#v / %#v", payload["evidence_complete"], payload["evidence_state"])
	}
	gaps, ok := payload["gaps"].([]string)
	if !ok || len(gaps) != 1 || gaps[0] != EventFieldSignatureCaptured {
		t.Fatalf("gaps snapshot = %#v", payload["gaps"])
	}
	missing, ok := payload["missing_evidence"].([]string)
	if !ok || len(missing) != 1 || missing[0] != "电子签名" {
		t.Fatalf("missing snapshot = %#v", payload["missing_evidence"])
	}
}

func TestCompleteServiceItemFieldFailsClosedWhenEvidenceWindowExceeded(t *testing.T) {
	repository := &fieldEvidenceRepository{
		item: domain.ServiceItem{ID: "SI-OVERFLOW-1", ProjectID: "PJ-OVERFLOW-1", Status: "实施中"},
		err:  ErrFieldEvidenceWindowExceeded,
	}
	service := Service{Repo: repository}
	manager := fieldEvidencePrincipal("project-manager-1", "project.field.complete")

	if _, err := service.ServiceItemFieldEvidence(context.Background(), fieldEvidencePrincipal("reader-1", "project.read"), "SI-OVERFLOW-1"); !errors.Is(err, ErrFieldEvidenceWindowExceeded) {
		t.Fatalf("evidence read error = %v, want window exceeded", err)
	}
	err := service.CompleteServiceItemField(context.Background(), manager, "SI-OVERFLOW-1", domain.FieldCompletionInput{})
	if !errors.Is(err, ErrFieldEvidenceWindowExceeded) {
		t.Fatalf("completion error = %v, want window exceeded (enforce must fail closed)", err)
	}
	if len(repository.applied) != 0 {
		t.Fatalf("overflow must not write a completion event: %+v", repository.applied)
	}

	// advisory 档只记录不拒绝，但绝不能留下 evidence_complete=true 的假结论。
	advisory := &fieldEvidenceRepository{item: repository.item, err: ErrFieldEvidenceWindowExceeded}
	if err := (&Service{Repo: advisory, FieldEvidenceGate: FieldEvidenceGatePolicy{Mode: FieldIncompleteReasonGateAdvisory}}).
		CompleteServiceItemField(context.Background(), manager, "SI-OVERFLOW-1", domain.FieldCompletionInput{}); err != nil {
		t.Fatalf("advisory overflow rejected: %v", err)
	}
	if len(advisory.applied) != 1 {
		t.Fatalf("advisory overflow events = %+v", advisory.applied)
	}
	payload := advisory.applied[0].Payload
	if payload["evidence_state"] != FieldEvidenceStateUnknown {
		t.Fatalf("advisory overflow evidence state = %#v", payload["evidence_state"])
	}
	if _, exists := payload["evidence_complete"]; exists {
		t.Fatalf("unknown evidence must not claim completeness: %+v", payload)
	}
}

func TestCompleteServiceItemFieldFailsClosedWithoutEvidenceStream(t *testing.T) {
	// 仓储不具备按服务项读取事件流的能力（历史实现/装配缺项）：无法核验就不允许完成。
	repository := &fieldEvidenceRepositoryWithoutStream{item: domain.ServiceItem{ID: "SI-NOSTREAM-1", ProjectID: "PJ-NOSTREAM-1", Status: "实施中"}}
	service := Service{Repo: repository}
	manager := fieldEvidencePrincipal("project-manager-1", "project.field.complete")

	if _, err := service.ServiceItemFieldEvidence(context.Background(), fieldEvidencePrincipal("reader-1", "project.read"), "SI-NOSTREAM-1"); !errors.Is(err, ErrFieldEvidenceUnavailable) {
		t.Fatalf("evidence read error = %v, want unavailable", err)
	}
	if err := service.CompleteServiceItemField(context.Background(), manager, "SI-NOSTREAM-1", domain.FieldCompletionInput{IncompleteReason: "客户禁止留存"}); !errors.Is(err, ErrFieldEvidenceUnavailable) {
		t.Fatalf("completion error = %v, want unavailable", err)
	}
	if len(repository.events) != 0 {
		t.Fatalf("unverifiable completion must not write events: %+v", repository.events)
	}
}

func TestFieldEvidenceGatePolicyDefaultsToEnforce(t *testing.T) {
	for _, testCase := range []struct{ mode, want string }{
		{mode: "", want: FieldIncompleteReasonGateEnforce},
		{mode: "enforce", want: FieldIncompleteReasonGateEnforce},
		{mode: " advisory ", want: FieldIncompleteReasonGateAdvisory},
		{mode: "unknown", want: FieldIncompleteReasonGateEnforce},
	} {
		if got := (FieldEvidenceGatePolicy{Mode: testCase.mode}).normalize(); got != testCase.want {
			t.Fatalf("normalize(%q) = %q, want %q", testCase.mode, got, testCase.want)
		}
	}
}
