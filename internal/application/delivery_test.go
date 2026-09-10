package application

import (
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

func TestValidatePenetrationComplianceRequiresFullAuthorizationSet(t *testing.T) {
	valid := domain.ImplementationPlanInput{
		PenetrationTestPlan: "扫描与漏洞验证步骤",
		AuthDocNo:           "AUTH-2026-001",
		AuthStart:           "2026-01-01T00:00:00Z",
		AuthEnd:             "2026-12-31T23:59:59Z",
		AuthScope:           "内网段 10.0.0.0/8",
		TestScope:           "业务系统 WEB 渗透",
		TestWindow:          "00:00-06:00",
		EmergencyContact:    "王工 13800000000",
		RollbackPlan:        "失败回滚到基线版本",
	}
	if err := validatePenetrationCompliance(valid); err != nil {
		t.Fatalf("valid compliance rejected: %v", err)
	}
	missing := valid
	missing.AuthDocNo = ""
	if err := validatePenetrationCompliance(missing); err != ErrValidation {
		t.Fatalf("missing auth_doc_no should fail validation, got %v", err)
	}
	badWindow := valid
	badWindow.AuthStart = "2026-12-31T00:00:00Z"
	badWindow.AuthEnd = "2026-01-01T00:00:00Z"
	if err := validatePenetrationCompliance(badWindow); err != ErrValidation {
		t.Fatalf("reversed auth window should fail validation, got %v", err)
	}
	standard := valid
	standard.PenetrationTestPlan = ""
	if err := validatePenetrationCompliance(standard); err != ErrValidation {
		t.Fatalf("missing penetration plan should fail validation, got %v", err)
	}
}

func TestReviewDecisionAndReportPhaseStateValidation(t *testing.T) {
	if err := validatePenetrationCompliance(domain.ImplementationPlanInput{}); err != ErrValidation {
		t.Fatal("empty compliance should fail")
	}
}