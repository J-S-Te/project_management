package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestProjectTextLimitsMatchColumnWidths(t *testing.T) {
	if err := validateProjectTextLength(strings.Repeat("项", 255), 255, "项目名称"); err != nil {
		t.Fatalf("boundary name rejected: %v", err)
	}
	if err := validateProjectTextLength(strings.Repeat("项", 256), 255, "项目名称"); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong name error = %v", err)
	}
	items := []domain.ContractService{{Site: strings.Repeat("场", 256), Batch: "批次", Category: "类别"}}
	if err := validateContractServiceTexts(items); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong service item error = %v", err)
	}
	decision := domain.PenetrationDecisionInput{CustomerContact: strings.Repeat("人", 129), CommunicationSummary: "结论"}
	if err := validatePenetrationDecisionTexts(decision); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong penetration contact error = %v", err)
	}
	plan := domain.PenetrationPlanInput{AuthDocNo: strings.Repeat("A", 129), AuthScope: "范围", TestScope: "范围", TestWindow: "00:00-06:00", EmergencyContact: "人", RollbackPlan: "回滚"}
	if err := validatePenetrationPlanTexts(plan); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong auth doc error = %v", err)
	}
	implementation := domain.ImplementationPlanInput{SitePlan: strings.Repeat("计", 5001)}
	if err := validateImplementationPlanTexts(implementation); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong site plan error = %v", err)
	}
	travel := domain.TravelArrangementInput{Origin: strings.Repeat("地", 256)}
	if err := validateTravelTexts(travel); !errors.Is(err, ErrValidation) {
		t.Fatalf("overlong travel origin error = %v", err)
	}
}
