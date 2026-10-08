package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type equipmentImportRepository struct {
	capabilityRepository
	creates      int
	rejectCreate bool
}

func (r *equipmentImportRepository) CreateEquipmentForImport(_ context.Context, item domain.Capability, _ string) (domain.Capability, error) {
	if r.rejectCreate {
		return item, ValidationError("设备编号已被其他请求占用")
	}
	r.creates++
	r.saved = append(r.saved, item)
	return item, nil
}
func equipmentImportPrincipal() platform.Principal {
	return principalWith("project.device.manage", platform.DataScope{ScopeType: "APPLICATION"})
}
func equipmentInput(row int, id string) EquipmentImportInput {
	return EquipmentImportInput{RowNo: row, Capability: domain.Capability{ResourceType: "EQUIPMENT", ResourceName: "扫描设备", ResourceID: id, Codes: []string{"C1"}}}
}

func TestEquipmentImportPreviewAllocatesWithoutWriting(t *testing.T) {
	repo := &equipmentImportRepository{}
	s := Service{Repo: repo}
	result, err := s.PreviewEquipmentImport(context.Background(), equipmentImportPrincipal(), []EquipmentImportInput{equipmentInput(2, ""), equipmentInput(3, "EQ-001"), equipmentInput(4, "")})
	if err != nil || result.Valid != 3 || len(repo.saved) != 0 {
		t.Fatalf("preview=%+v err=%v writes=%v", result, err, repo.saved)
	}
	if result.Rows[0].Capability.ResourceID == "EQ-001" || result.Rows[0].Capability.ResourceID == result.Rows[2].Capability.ResourceID {
		t.Fatal("generated number collision")
	}
	if _, err := s.ConfirmEquipmentImportRow(context.Background(), equipmentImportPrincipal(), result.Rows[0]); err != nil || repo.creates != 1 {
		t.Fatalf("confirmation=%v creates=%d", err, repo.creates)
	}
	repo.rejectCreate = true
	if _, err := s.ConfirmEquipmentImportRow(context.Background(), equipmentImportPrincipal(), result.Rows[2]); err == nil || repo.creates != 1 {
		t.Fatal("conflicting create overwrote equipment")
	}
}

func TestEquipmentImportValidationAndUpdate(t *testing.T) {
	repo := &equipmentImportRepository{capabilityRepository: capabilityRepository{capabilities: []domain.Capability{{ResourceType: "EQUIPMENT", ResourceID: "EQ-001", Codes: []string{"C1"}, UsageScope: domain.EquipmentUsageCompanyOnly}}}}
	s := Service{Repo: repo}
	p := equipmentImportPrincipal()
	result, err := s.PreviewEquipmentImport(context.Background(), p, []EquipmentImportInput{equipmentInput(2, "EQ-001")})
	if err != nil || result.Valid != 1 || result.Rows[0].Action != "UPDATE" || result.Rows[0].Capability.UsageScope != domain.EquipmentUsageCompanyOnly {
		t.Fatalf("update=%+v %v", result, err)
	}
	if _, err := s.ConfirmEquipmentImportRow(context.Background(), p, result.Rows[0]); err != nil || repo.creates != 0 || len(repo.saved) != 1 {
		t.Fatalf("update save %v %+v", err, repo.saved)
	}
	result, err = s.PreviewEquipmentImport(context.Background(), p, []EquipmentImportInput{equipmentInput(2, "EQ-001"), equipmentInput(3, "EQ-001")})
	if err != nil || result.Invalid != 2 {
		t.Fatalf("duplicate=%+v %v", result, err)
	}
	for _, mutate := range []func(*EquipmentImportInput){
		func(i *EquipmentImportInput) { i.Capability.ResourceName = "" },
		func(i *EquipmentImportInput) { i.Capability.ResourceName = strings.Repeat("设", 129) },
		func(i *EquipmentImportInput) { i.Capability.ResourceID = strings.Repeat("E", 65) },
		func(i *EquipmentImportInput) { i.Capability.ResourceType = "PERSON" },
		func(i *EquipmentImportInput) { i.Capability.ResourceID = "=HYPERLINK()" },
		func(i *EquipmentImportInput) { i.Capability.Status = "EXPIRED" },
		func(i *EquipmentImportInput) { i.Capability.UsageScope = "INVALID" },
		func(i *EquipmentImportInput) { i.Capability.Codes = []string{"UNKNOWN"} },
		func(i *EquipmentImportInput) {
			i.Capability.ValidFrom = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			i.Capability.ValidUntil = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		},
		func(i *EquipmentImportInput) { i.Errors = []string{"日期格式错误"} },
	} {
		input := equipmentInput(2, "")
		mutate(&input)
		result, err = s.PreviewEquipmentImport(context.Background(), p, []EquipmentImportInput{input})
		if err != nil || result.Invalid != 1 {
			t.Fatalf("invalid accepted=%+v %v", result, err)
		}
	}
}

func TestEquipmentImportPermissionsAndDependency(t *testing.T) {
	s := Service{Repo: &equipmentImportRepository{}}
	if _, err := s.PreviewEquipmentImport(context.Background(), principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"}), []EquipmentImportInput{equipmentInput(2, "")}); err == nil {
		t.Fatal("personnel permission granted equipment import")
	}
	s.Repo = nil
	if _, err := s.PreviewEquipmentImport(context.Background(), equipmentImportPrincipal(), []EquipmentImportInput{equipmentInput(2, "")}); err == nil {
		t.Fatal("missing repository accepted")
	}
}
