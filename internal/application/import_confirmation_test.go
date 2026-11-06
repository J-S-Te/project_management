package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

// confirmingImportStore 在内存中模拟 pm_import_operation 的「单事务 + 唯一键」语义：
// 全部行写入成功才提交并登记；任一行失败整批丢弃且不登记；同键复用它直接回读首次记录。
type confirmingImportStore struct {
	capabilityRepository
	records             map[string]ImportOperation
	capabilities        []domain.Capability
	equipmentCreates    []domain.Capability
	categories          []domain.DetectionCategory
	failCapability      func(domain.Capability) error
	failEquipmentCreate func(domain.Capability) error
	failCategory        func(domain.DetectionCategory) error
}

func (s *confirmingImportStore) RunImportConfirmation(_ context.Context, op ImportOperation, write func(ImportRowWriter) (ImportConfirmationResult, error)) (ImportOperation, bool, error) {
	if s.records == nil {
		s.records = map[string]ImportOperation{}
	}
	key := op.TenantID + "\x00" + op.IdempotencyKey
	if record, ok := s.records[key]; ok {
		return record, true, nil
	}
	writer := &confirmingImportWriter{store: s}
	result, err := write(writer)
	if err != nil {
		return ImportOperation{}, false, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ImportOperation{}, false, err
	}
	op.ResponseJSON = string(payload)
	s.records[key] = op
	s.capabilities = append(s.capabilities, writer.capabilities...)
	s.equipmentCreates = append(s.equipmentCreates, writer.equipmentCreates...)
	s.categories = append(s.categories, writer.categories...)
	return op, false, nil
}

type confirmingImportWriter struct {
	store            *confirmingImportStore
	capabilities     []domain.Capability
	equipmentCreates []domain.Capability
	categories       []domain.DetectionCategory
}

func (w *confirmingImportWriter) UpsertCapability(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	if w.store.failCapability != nil {
		if err := w.store.failCapability(item); err != nil {
			return item, err
		}
	}
	w.capabilities = append(w.capabilities, item)
	return item, nil
}

func (w *confirmingImportWriter) CreateEquipmentForImport(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	if w.store.failEquipmentCreate != nil {
		if err := w.store.failEquipmentCreate(item); err != nil {
			return item, err
		}
	}
	w.equipmentCreates = append(w.equipmentCreates, item)
	return item, nil
}

func (w *confirmingImportWriter) SaveDetectionCategory(_ context.Context, _ string, item domain.DetectionCategory, _ string) (domain.DetectionCategory, error) {
	if w.store.failCategory != nil {
		if err := w.store.failCategory(item); err != nil {
			return item, err
		}
	}
	w.categories = append(w.categories, item)
	return item, nil
}

func capabilityImportPrincipal() platform.Principal {
	return principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"})
}

func capabilityConfirmRow(rowNo int, userID string) CapabilityImportPreviewRow {
	return CapabilityImportPreviewRow{
		Action: "CREATE", RowNo: rowNo, Status: "READY",
		Capability: domain.Capability{ResourceType: "PERSON", ResourceID: "P-000" + string(rune('0'+rowNo)), ResourceName: "张三" + userID, UserID: userID, Codes: []string{"QUAL-1"}, Status: "ACTIVE"},
	}
}

func TestImportIdempotencyKeyValidation(t *testing.T) {
	for _, test := range []struct {
		raw    string
		wantOK bool
	}{
		{"", false},
		{"   ", false},
		{"k", true},
		{"  pm-import:1_a.b  ", true},
		{"bad key", false},
		{"bad/key", false},
		{"bad?key", false},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
		{"含中文", false},
	} {
		key, err := NormalizeImportIdempotencyKey(test.raw)
		if test.wantOK {
			if err != nil || key != strings.TrimSpace(test.raw) {
				t.Fatalf("key %q rejected: %v", test.raw, err)
			}
			continue
		}
		if !errors.Is(err, ErrValidation) || key != "" {
			t.Fatalf("key %q must be rejected as validation error, got %q %v", test.raw, key, err)
		}
	}
}

func TestImportRequestHashCoversFileRowsAndResourceType(t *testing.T) {
	content := ImportContentHash([]byte("人员姓名,组织名称\n张三,检测部\n"))
	base := ImportRequestHash(ImportOperationCapability, content, "PERSON", []int{3, 2})
	if len(base) != 64 || strings.Trim(base, "0123456789abcdef") != "" {
		t.Fatalf("request hash must be a sha256 hex digest, got %q", base)
	}
	if base != ImportRequestHash(ImportOperationCapability, content, "person", []int{2, 3}) {
		t.Fatal("selected row order and resource type case must not change the request hash")
	}
	for _, other := range []string{
		ImportRequestHash(ImportOperationEquipment, content, "PERSON", []int{2, 3}),
		ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("changed")), "PERSON", []int{2, 3}),
		ImportRequestHash(ImportOperationCapability, content, "EQUIPMENT", []int{2, 3}),
		ImportRequestHash(ImportOperationCapability, content, "PERSON", []int{2, 4}),
		ImportRequestHash(ImportOperationCapability, content, "PERSON", []int{2}),
	} {
		if other == base {
			t.Fatalf("request hash must change with operation, content, resource type and selected rows: %q", other)
		}
	}
}

func TestCapabilityImportConfirmationIsAllOrNothing(t *testing.T) {
	repo := &confirmingImportStore{}
	s := Service{Repo: repo}
	rows := []CapabilityImportPreviewRow{capabilityConfirmRow(2, "u1"), capabilityConfirmRow(3, "u2")}
	repo.failCapability = func(item domain.Capability) error {
		if item.UserID == "u2" {
			return ValidationError("资质编号已属于其他人员，不能覆盖")
		}
		return nil
	}
	_, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0001",
		ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("csv")), "PERSON", []int{2, 3}), rows, []int{2, 3})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "第 3 行") || !strings.Contains(err.Error(), "资质编号已属于其他人员") {
		t.Fatalf("second row failure must fail the batch with row number and reason: %v", err)
	}
	if len(repo.capabilities) != 0 {
		t.Fatalf("partial write leaked despite batch failure: %+v", repo.capabilities)
	}
	if len(repo.records) != 0 {
		t.Fatalf("failed import must not record an idempotency entry: %+v", repo.records)
	}
	// 修正数据后同键可以成功，并且只写一次。
	repo.failCapability = nil
	hash := ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("csv")), "PERSON", []int{2, 3})
	result, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0001", hash, rows, []int{2, 3})
	if err != nil || result.Imported != 2 || result.Skipped != 0 || len(repo.capabilities) != 2 || len(repo.records) != 1 {
		t.Fatalf("retry after failure=%+v err=%v writes=%d records=%d", result, err, len(repo.capabilities), len(repo.records))
	}
}

func TestCapabilityImportConfirmationReplaysSameResponse(t *testing.T) {
	repo := &confirmingImportStore{}
	s := Service{Repo: repo}
	rows := []CapabilityImportPreviewRow{capabilityConfirmRow(2, "u1"), capabilityConfirmRow(3, "u2")}
	hash := ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("csv")), "PERSON", []int{2, 3})
	first, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0002", hash, rows, []int{2, 3})
	if err != nil || first.Imported != 2 || len(repo.capabilities) != 2 {
		t.Fatalf("first confirm=%+v err=%v writes=%d", first, err, len(repo.capabilities))
	}
	second, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0002", hash, rows, []int{2, 3})
	if err != nil || second.Imported != first.Imported || second.Skipped != first.Skipped || len(second.Rows) != len(first.Rows) {
		t.Fatalf("replay must return the first response: first=%+v second=%+v err=%v", first, second, err)
	}
	if len(repo.capabilities) != 2 {
		t.Fatalf("replay must not write again, writes=%d", len(repo.capabilities))
	}
	// 空键在应用层也失败关闭。
	if _, err = s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "  ", hash, rows, []int{2, 3}); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank idempotency key must be rejected: %v", err)
	}
}

func TestImportConfirmationRejectsReusedKeyForDifferentRequest(t *testing.T) {
	repo := &confirmingImportStore{}
	s := Service{Repo: repo}
	rows := []CapabilityImportPreviewRow{capabilityConfirmRow(2, "u1")}
	firstHash := ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("first.csv")), "PERSON", []int{2})
	if _, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0003", firstHash, rows, []int{2}); err != nil {
		t.Fatal(err)
	}
	otherHash := ImportRequestHash(ImportOperationCapability, ImportContentHash([]byte("second.csv")), "PERSON", []int{2})
	_, err := s.ConfirmCapabilityImport(context.Background(), capabilityImportPrincipal(), "pm-import-0003", otherHash, rows, []int{2})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("reused key with a different request hash must conflict: %v", err)
	}
	if len(repo.capabilities) != 1 {
		t.Fatalf("conflicting reuse must not write again, writes=%d", len(repo.capabilities))
	}
}

func TestEquipmentImportConfirmationCreatesAndUpdatesInOneTransaction(t *testing.T) {
	repo := &confirmingImportStore{}
	s := Service{Repo: repo}
	create := CapabilityImportPreviewRow{RowNo: 2, Action: "CREATE", Status: "READY", Capability: domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-0009", ResourceName: "扫描仪", Codes: []string{"C1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageAny}}
	update := CapabilityImportPreviewRow{RowNo: 3, Action: "UPDATE", Status: "READY", Capability: domain.Capability{ResourceType: "EQUIPMENT", ResourceID: "EQ-0002", ResourceName: "扫描仪", Codes: []string{"C1"}, Status: "ACTIVE", UsageScope: domain.EquipmentUsageCompanyOnly}}
	hash := ImportRequestHash(ImportOperationEquipment, ImportContentHash([]byte("equipment.csv")), "EQUIPMENT", []int{2, 3})
	result, err := s.ConfirmEquipmentImport(context.Background(), equipmentImportPrincipal(), "pm-import-0004", hash, []CapabilityImportPreviewRow{create, update}, []int{2, 3})
	if err != nil || result.Imported != 2 {
		t.Fatalf("confirm=%+v err=%v", result, err)
	}
	if len(repo.equipmentCreates) != 1 || repo.equipmentCreates[0].ResourceID != "EQ-0009" {
		t.Fatalf("CREATE must use pure insert, creates=%+v", repo.equipmentCreates)
	}
	if len(repo.capabilities) != 1 || repo.capabilities[0].ResourceID != "EQ-0002" {
		t.Fatalf("UPDATE must upsert the existing equipment, upserts=%+v", repo.capabilities)
	}
}

func TestDetectionCategoryImportConfirmationIsAllOrNothing(t *testing.T) {
	repo := &confirmingImportStore{}
	s := Service{Repo: repo}
	rows := []DetectionCategoryImportPreviewRow{
		{RowNo: 2, Action: "CREATE", Status: "READY", Item: domain.DetectionCategory{Category: "等保", SpecialMethod: domain.SpecialMethodNo, Enabled: true}},
		{RowNo: 3, Action: "CREATE", Status: "READY", Item: domain.DetectionCategory{Category: "密评", SpecialMethod: domain.SpecialMethodNo, Enabled: true}},
	}
	repo.failCategory = func(item domain.DetectionCategory) error {
		if item.Category == "密评" {
			return ErrValidation
		}
		return nil
	}
	hash := ImportRequestHash(ImportOperationDetectionCategory, ImportContentHash([]byte("categories.csv")), "DETECTION_CATEGORY", []int{2, 3})
	if _, err := s.ConfirmDetectionCategoryImport(context.Background(), principalWith("project_rule.manage", platform.DataScope{ScopeType: "APPLICATION"}), "pm-import-0005", hash, rows, []int{2, 3}); !errors.Is(err, ErrValidation) {
		t.Fatalf("second category failure must fail the batch: %v", err)
	}
	if len(repo.categories) != 0 {
		t.Fatalf("partial category write leaked: %+v", repo.categories)
	}
	repo.failCategory = nil
	result, err := s.ConfirmDetectionCategoryImport(context.Background(), principalWith("project_rule.manage", platform.DataScope{ScopeType: "APPLICATION"}), "pm-import-0005", hash, rows, []int{2})
	if err != nil || result.Imported != 1 || result.Rows[1].Status != "SKIPPED" || len(repo.categories) != 1 {
		t.Fatalf("confirm=%+v err=%v writes=%+v", result, err, repo.categories)
	}
}
