package domain

import (
	"slices"
	"testing"
	"time"
)

// TestExpenseParticipationAndTerminationAreSeparate 验证终止障碍不掩盖当前参与资格且错关联不保留资格。
// 参数：t为测试上下文。
// 返回值：无；资格与状态混淆或边界放行时失败。
func TestExpenseParticipationAndTerminationAreSeparate(t *testing.T) {
	now := time.Now().UTC()
	p := Project{ID: "p", TenantID: "t", OwnerIdentityID: "u", Status: ProjectStatusTerminated, SourceKind: "NATIVE", Version: 1}
	item := ServiceItem{ID: "a", ProjectID: "p", TenantID: "t", Status: ProjectStatusTerminated, Version: 2}
	ref := ExpenseReference(p, &item, "u", nil, now)
	if !ref.ParticipantEligible || ref.Eligible || ref.ProjectStatus != ProjectStatusTerminated || ref.ServiceItemStatus != ProjectStatusTerminated || !slices.Contains(ref.ReasonCodes, "PROJECT_TERMINATED") || !slices.Contains(ref.ReasonCodes, "SERVICE_ITEM_TERMINATED") {
		t.Fatalf("termination %+v", ref)
	}
	if ExpenseReference(p, &item, "outsider", nil, now).ParticipantEligible {
		t.Fatal("termination made outsider eligible")
	}
	item.ProjectID = "foreign"
	if ExpenseReference(p, &item, "u", nil, now).ParticipantEligible {
		t.Fatal("invalid service preserved eligibility")
	}
	item.ProjectID = "p"
	item.TenantID = "foreign"
	if ExpenseReference(p, &item, "u", nil, now).ParticipantEligible {
		t.Fatal("wrong tenant preserved eligibility")
	}
	p.OwnerIdentityID = ""
	grant := ExpenseParticipant{TenantID: "t", ProjectID: "p", SubjectID: "u", Status: "ACTIVE", ValidFrom: now.Add(-time.Hour), ValidUntil: now}
	if ExpenseReference(p, nil, "u", &grant, now).ParticipantEligible {
		t.Fatal("expired participant accepted")
	}
}

// TestExpenseQualificationClockChangesReferenceVersion 验证显式资格到期后的资格来源变化进入快照版本。
// 参数：t为测试上下文。
// 返回值：无；不同权威资格来源得到相同版本时失败。
func TestExpenseQualificationClockChangesReferenceVersion(t *testing.T) {
	now := time.Now().UTC()
	p := Project{ID: "p", TenantID: "t", OwnerIdentityID: "u", Version: 1}
	grant := ExpenseParticipant{TenantID: "t", ProjectID: "p", SubjectID: "u", Status: "ACTIVE", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Second), Version: 1}
	before := ExpenseReference(p, nil, "u", &grant, now)
	after := ExpenseReference(p, nil, "u", &grant, now.Add(time.Second))
	if before.EligibilitySource == after.EligibilitySource || before.ReferenceVersion == after.ReferenceVersion {
		t.Fatal("eligibility source change not versioned", before.EligibilitySource, after.EligibilitySource)
	}
}
