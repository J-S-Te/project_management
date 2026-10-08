package application

import (
	"context"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

func TestDetectionCategoryImportPreviewReadOnlyAndDuplicates(t *testing.T) {
	repo := &capabilityCodeSplitRepository{saved: []domain.DetectionCategory{{Category: "既有"}}}
	s := Service{Repo: repo}
	p := principalWith("project_rule.manage", platform.DataScope{ScopeType: "APPLICATION"})
	inputs := []DetectionCategoryImportInput{{RowNo: 2, Item: domain.DetectionCategory{Category: "既有", SpecialMethod: "NO", Enabled: true}}, {RowNo: 3, Item: domain.DetectionCategory{Category: "新类别", SpecialMethod: "NO", Enabled: true}}}
	result, err := s.PreviewDetectionCategoryImport(context.Background(), p, inputs)
	if err != nil || result.Valid != 2 || result.Rows[0].Action != "UPDATE" || result.Rows[1].Action != "CREATE" || len(repo.saved) != 1 {
		t.Fatalf("result=%+v err=%v writes=%v", result, err, repo.saved)
	}
	inputs[1].Item.Category = "既有"
	result, err = s.PreviewDetectionCategoryImport(context.Background(), p, inputs)
	if err != nil || result.Invalid != 2 {
		t.Fatalf("duplicates=%+v %v", result, err)
	}
	if _, err := s.PreviewDetectionCategoryImport(context.Background(), platform.Principal{}, inputs); err == nil {
		t.Fatal("missing permission accepted")
	}
}

func TestDetectionCategoryImportPreviewValidation(t *testing.T) {
	repo := &capabilityCodeSplitRepository{capabilityCodeRuleRepository: capabilityCodeRuleRepository{rules: []domain.Rule{{Kind: capabilityCodeRuleKind, Scope: "PERSON-A", CheckType: "PERSON", Enabled: true}, {Kind: capabilityCodeRuleKind, Scope: "DEVICE-A", CheckType: "EQUIPMENT", Enabled: true}, {Kind: capabilityCodeRuleKind, Scope: "OFF", CheckType: "PERSON", Enabled: false}}}}
	s := Service{Repo: repo}
	p := principalWith("project_rule.manage", platform.DataScope{ScopeType: "APPLICATION"})
	for _, code := range []string{"UNKNOWN", "DEVICE-A", "OFF"} {
		result, err := s.PreviewDetectionCategoryImport(context.Background(), p, []DetectionCategoryImportInput{{RowNo: 2, Item: domain.DetectionCategory{Category: "测试", SpecialMethod: "NO", RequiredCodes: code}}})
		if err != nil || result.Invalid != 1 {
			t.Fatalf("code=%s %+v %v", code, result, err)
		}
	}
	for _, item := range []domain.DetectionCategory{{Category: "", SpecialMethod: "NO"}, {Category: "测试", SpecialMethod: "INVALID"}, {Category: "测试", SpecialMethod: "NO", SystemStandard: strings.Repeat("文", 129)}, {Category: "测试", SpecialMethod: "NO", RequiredQualifications: "包含\x00控制符"}, {Category: "=公式", SpecialMethod: "NO"}} {
		result, err := s.PreviewDetectionCategoryImport(context.Background(), p, []DetectionCategoryImportInput{{RowNo: 2, Item: item}})
		if err != nil || result.Invalid != 1 {
			t.Fatalf("item=%+v result=%+v %v", item, result, err)
		}
	}
}
