package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

func TestWarningRulesOnlyAcceptEffectiveCheckTypesAndPositiveThresholds(t *testing.T) {
	repository := &scopeRepository{}
	service := &Service{Repo: repository}
	principal := principalWith("project_rule.manage", platform.DataScope{RoleCode: "admin", ScopeType: "TENANT", ScopeID: "tenant-1"})

	for _, checkType := range []string{warningCheckQualification, warningCheckCapability, warningCheckOther} {
		created, err := service.CreateRule(context.Background(), principal, domain.Rule{
			Kind: "warning-rules", Name: "  " + checkType + "预警  ", CheckType: "  " + checkType + "  ", Threshold: "03", Enabled: true,
		})
		if err != nil {
			t.Fatalf("create %s: %v", checkType, err)
		}
		if created.Name != checkType+"预警" || created.CheckType != checkType || created.Threshold != "3" {
			t.Fatalf("rule was not normalized: %+v", created)
		}
	}

	for _, test := range []struct {
		name      string
		checkType string
		threshold string
	}{
		{name: "unknown check type", checkType: "排期冲突", threshold: "1"},
		{name: "missing threshold", checkType: warningCheckQualification},
		{name: "text threshold", checkType: warningCheckCapability, threshold: "连续 3 项冲突"},
		{name: "zero threshold", checkType: warningCheckOther, threshold: "0"},
		{name: "decimal threshold", checkType: warningCheckOther, threshold: "1.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.CreateRule(context.Background(), principal, domain.Rule{
				Kind: "warning-rules", Name: "预警", CheckType: test.checkType, Threshold: test.threshold,
			})
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error=%v, want validation error", err)
			}
		})
	}

	if _, err := service.UpdateRule(context.Background(), principal, 7, domain.Rule{
		Kind: "warning-rules", Name: "非法更新", CheckType: "场地冲突", Threshold: "1",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("update error=%v, want validation error", err)
	}
}
