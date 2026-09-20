package application

import (
	"context"
	"errors"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
	"github.com/j-s-te/project-management/internal/platform"
)

type triageStub struct {
	result domain.DeviationTriageResult
	err    error
}

func (s triageStub) TriageDeviation(context.Context, domain.DeviationInput) (domain.DeviationTriageResult, error) {
	return s.result, s.err
}

func TestTriageDeviationIsPermissionedAndAdvisory(t *testing.T) {
	service := Service{DeviationTriage: triageStub{result: domain.DeviationTriageResult{Mode: "ADVISORY_ONLY", Model: "jev-test", Probabilities: map[string]float64{"safety_risk": 0.8}}}}
	principal := platform.Principal{Permissions: map[string]bool{"project.deviation.report": true}}
	result, err := service.TriageDeviation(context.Background(), principal, domain.DeviationInput{Description: "  设备检定过期  ", Severity: "HIGH"})
	if err != nil || result.Mode != "ADVISORY_ONLY" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := service.TriageDeviation(context.Background(), platform.Principal{}, domain.DeviationInput{Description: "偏差"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permission error=%v", err)
	}
	if _, err := (&Service{}).TriageDeviation(context.Background(), principal, domain.DeviationInput{Description: "偏差"}); !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
}
