package domain

import "testing"

func TestIncomeContractServiceCategoriesHaveNoInventedRequirements(t *testing.T) {
	wanted := map[string]bool{"模块开发": false, "技术咨询": false}
	for _, item := range DefaultDetectionCategories() {
		if _, ok := wanted[item.Category]; !ok {
			continue
		}
		if wanted[item.Category] {
			t.Fatalf("duplicate category %s", item.Category)
		}
		wanted[item.Category] = true
		if !item.Enabled || item.SpecialMethod != SpecialMethodNo || item.RequiredCodes != "" || item.RequiredQualifications != "" || item.SystemStandard != "" {
			t.Fatalf("unexpected default restrictions: %+v", item)
		}
		if err := ValidateDetectionCategory(item); err != nil {
			t.Fatal(err)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("missing category %s", name)
		}
	}
}
