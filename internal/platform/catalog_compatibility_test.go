package platform

import "testing"

func TestProjectCatalogCompatibilityRejectsOutsideNMinusOne(t *testing.T) {
	if err := validateProjectCatalogCompatibility("2", "3", []string{"3", "2"}); err != nil {
		t.Fatalf("N-1 catalog rejected: %v", err)
	}
	if err := validateProjectCatalogCompatibility("1", "3", []string{"3", "2"}); err == nil {
		t.Fatal("catalog older than N-1 accepted")
	}
	if err := validateProjectCatalogCompatibility("2", "3", []string{"3"}); err == nil {
		t.Fatal("incomplete compatibility window accepted")
	}
}
