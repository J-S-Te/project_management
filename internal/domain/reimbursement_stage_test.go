package domain

import (
	"slices"
	"testing"
	"time"
)

// TestExpenseReferenceRequiresReportingStage 验证报销引用从报告编制阶段开始开放。
// 参数：t为项目状态、参与资格及边界回归上下文。
// 返回值：无；提前阶段或未知状态放行、合法阶段拒绝时失败。
func TestExpenseReferenceRequiresReportingStage(t *testing.T) {
	for _, status := range []string{"", "未知", " 报告编制 ", ProjectStatusPendingDecomposition,
		ProjectStatusSupplementRequired, ProjectStatusException, ProjectStatusPendingAllocation,
		ProjectStatusPendingExecution, ProjectStatusPreparing, ProjectStatusInProgress,
		ProjectStatusFieldCompleted, ProjectStatusReporting, ProjectStatusCompleted} {
		p := Project{ID: "p", TenantID: "t", OwnerIdentityID: "u", Status: status}
		ref := ExpenseReference(p, nil, "u", nil, time.Now().UTC())
		want := status == ProjectStatusReporting || status == ProjectStatusCompleted
		if ref.Eligible != want || !ref.ParticipantEligible {
			t.Errorf("status=%q eligible=%v participant=%v", status, ref.Eligible, ref.ParticipantEligible)
		}
		if !want && !slices.Contains(ref.ReasonCodes, "PROJECT_REPORT_STAGE_REQUIRED") {
			t.Errorf("status=%q missing stage reason: %v", status, ref.ReasonCodes)
		}
		if ExpenseReference(p, nil, "outsider", nil, time.Now().UTC()).Eligible {
			t.Errorf("status=%q granted outsider access", status)
		}
	}
}

// TestExpenseReportingGatePreservesExplicitParticipation 验证显式参与名单不能绕过项目阶段门槛。
// 参数：t为当前名单与项目状态边界测试上下文。
// 返回值：无；名单丢失或提前阶段放行时失败。
func TestExpenseReportingGatePreservesExplicitParticipation(t *testing.T) {
	now := time.Now().UTC()
	p := Project{ID: "p", TenantID: "t", Status: ProjectStatusInProgress}
	grant := ExpenseParticipant{
		TenantID: "t", ProjectID: "p", SubjectID: "u", Status: "ACTIVE",
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
	}
	ref := ExpenseReference(p, nil, "u", &grant, now)
	if ref.Eligible || !ref.ParticipantEligible || ref.EligibilitySource != "EXPLICIT_PARTICIPANT" ||
		!slices.Contains(ref.ReasonCodes, "PROJECT_REPORT_STAGE_REQUIRED") {
		t.Fatalf("explicit participant bypassed stage: %+v", ref)
	}
	for _, status := range []string{ProjectStatusTerminated, "terminated", " TERMINATED "} {
		p.Status = status
		ref = ExpenseReference(p, nil, "u", &grant, now)
		if ref.Eligible || !ref.ParticipantEligible ||
			!slices.Equal(ref.ReasonCodes, []string{"PROJECT_TERMINATED"}) {
			t.Fatalf("termination reasons changed: %+v", ref)
		}
	}
}
