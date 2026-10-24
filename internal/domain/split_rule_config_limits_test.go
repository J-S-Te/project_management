package domain

import (
	"strings"
	"testing"
)

func TestDetectionCategoryAndSplitOverrideLengthLimits(t *testing.T) {
	category := DetectionCategory{Category: "类别", SystemStandard: strings.Repeat("标", 129), SpecialMethod: SpecialMethodNo}
	if err := ValidateDetectionCategory(category); err == nil {
		t.Fatal("overlong system standard was accepted")
	}
	override := SplitOverride{Name: strings.Repeat("规", 256), Match: SplitOverrideMatch{CustomerContains: "客户"}, Priority: 10}
	if err := ValidateSplitOverride(override); err == nil {
		t.Fatal("overlong override name was accepted")
	}
	override = SplitOverride{Name: "规则", Match: SplitOverrideMatch{ContractContains: strings.Repeat("合", 65)}, Priority: 10}
	if err := ValidateSplitOverride(override); err == nil {
		t.Fatal("overlong contract match was accepted")
	}
}
