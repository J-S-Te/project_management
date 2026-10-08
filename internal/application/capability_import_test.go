package application

import (
	"context"
	"errors"
	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
	"testing"
)

type importPersonnelDirectory struct {
	people []platform.OwnerDirectoryUser
	fail   bool
	calls  *int
}

type incompleteImportDirectory struct{}

func (incompleteImportDirectory) List(context.Context, platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	return platform.OwnerDirectoryPage{Total: 2, Items: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三"}}}, nil
}
func TestCapabilityImportRejectsIncompleteDirectoryAndFormulaIdentifier(t *testing.T) {
	p := principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"})
	s := Service{Repo: &capabilityRepository{}, Personnel: incompleteImportDirectory{}}
	input := CapabilityImportInput{RowNo: 2, Capability: domain.Capability{ResourceType: "PERSON", ResourceName: "张三", Codes: []string{"QUAL-1"}}}
	result, err := s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{input})
	if err != nil || result.Invalid != 1 {
		t.Fatalf("partial directory accepted: %+v %v", result, err)
	}
	s.Personnel = importPersonnelDirectory{people: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三"}}}
	for _, id := range []string{"=CMD()", "+SUM(1)", "-1", "@CELL", "P\n001"} {
		input.Capability.ResourceID = id
		result, err = s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{input})
		if err != nil || result.Invalid != 1 {
			t.Fatalf("formula ID accepted %q: %+v %v", id, result, err)
		}
	}
}

func (d importPersonnelDirectory) List(_ context.Context, q platform.OwnerDirectoryQuery) (platform.OwnerDirectoryPage, error) {
	if d.calls != nil {
		*d.calls++
	}
	if d.fail {
		return platform.OwnerDirectoryPage{}, errors.New("secret internal error")
	}
	items := []platform.OwnerDirectoryUser{}
	for _, p := range d.people {
		if q.UserID == "" || p.UserID == q.UserID {
			items = append(items, p)
		}
	}
	return platform.OwnerDirectoryPage{Items: items, Total: int64(len(items))}, nil
}
func TestCapabilityImportPreviewResolvesIdentityWithoutWriting(t *testing.T) {
	repo := &capabilityRepository{}
	s := Service{Repo: repo, Personnel: importPersonnelDirectory{people: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三", Organizations: []platform.OwnerDirectoryOrganization{{OrganizationID: "o1", OrganizationName: "检测部", IsPrimary: true}}}}}}
	p := principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"})
	inputs := []CapabilityImportInput{{RowNo: 2, OrganizationName: "检测部", Capability: domain.Capability{ResourceType: "PERSON", ResourceName: "张三", Codes: []string{"QUAL-1"}}}}
	preview, err := s.PreviewCapabilityImport(context.Background(), p, inputs)
	if err != nil || preview.Valid != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if len(repo.saved) != 0 || preview.Rows[0].Capability.UserID != "u1" || preview.Rows[0].Capability.ResourceID == "" {
		t.Fatalf("unexpected preview=%+v writes=%v", preview, repo.saved)
	}
	s.Personnel = importPersonnelDirectory{fail: true}
	result, err := s.ImportCapabilities(context.Background(), p, []domain.Capability{preview.Rows[0].Capability})
	if err != nil || result.Imported != 0 || result.Skipped != 1 || len(repo.saved) != 0 {
		t.Fatalf("confirmation bypassed directory: %+v %v", result, err)
	}
}
func TestCapabilityImportPreviewFailsAmbiguousDuplicateAndDisabledCodes(t *testing.T) {
	p := principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"})
	s := Service{Repo: &capabilityRepository{}, Personnel: importPersonnelDirectory{people: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三"}, {UserID: "u2", DisplayName: "张三"}}}}
	input := CapabilityImportInput{RowNo: 2, Capability: domain.Capability{ResourceType: "PERSON", ResourceName: "张三", Codes: []string{"QUAL-1"}}}
	result, err := s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{input})
	if err != nil || result.Invalid != 1 {
		t.Fatalf("ambiguous=%+v %v", result, err)
	}
	queries := 0
	s.Personnel = importPersonnelDirectory{people: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三"}}, calls: &queries}
	second := input
	second.RowNo = 3
	result, err = s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{input, second})
	if err != nil || result.Invalid != 2 {
		t.Fatalf("duplicate=%+v %v", result, err)
	}
	if queries != 1 {
		t.Fatalf("expected one batch lookup, got %d", queries)
	}
	input.Capability.Codes = []string{"NOT-CONFIGURED"}
	result, err = s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{input})
	if err != nil || result.Invalid != 1 {
		t.Fatalf("invalid code=%+v %v", result, err)
	}
}
func TestCapabilityImportPreviewPreservesExistingOwner(t *testing.T) {
	p := principalWith("project.resource.manage", platform.DataScope{ScopeType: "APPLICATION"})
	repo := &capabilityRepository{capabilities: []domain.Capability{{ResourceType: "PERSON", ResourceID: "P-001", UserID: "other", ResourceName: "李四"}}}
	s := Service{Repo: repo, Personnel: importPersonnelDirectory{people: []platform.OwnerDirectoryUser{{UserID: "u1", DisplayName: "张三"}}}}
	result, err := s.PreviewCapabilityImport(context.Background(), p, []CapabilityImportInput{{RowNo: 2, Capability: domain.Capability{ResourceType: "PERSON", ResourceID: "P-001", ResourceName: "张三", Codes: []string{"QUAL-1"}}}})
	if err != nil || result.Invalid != 1 || len(repo.saved) != 0 {
		t.Fatalf("owner overwrite: %+v %v", result, err)
	}
}
