package domain

import (
	"testing"
	"time"
)

// TestExpenseOwnerIsOnlyAssignedProjectManager 验证费用负责人仅来自真实项目经理字段且参与资格不受影响。
// 参数：t为测试上下文。
// 返回值：无；回退团队负责人或owner、改变参与资格时失败。
func TestExpenseOwnerIsOnlyAssignedProjectManager(t *testing.T) {
	p := Project{ID: "p", TenantID: "t", OwnerIdentityID: "owner", ManagerIdentityID: "pm-project", Version: 7}
	item := ServiceItem{ID: "a", ProjectID: "p", TenantID: "t", TeamLeadID: "lead", ProjectManagerID: "pm-item", EngineerIDs: []string{"engineer"}, Version: 8}
	ref := ExpenseReference(p, &item, "engineer", nil, time.Now().UTC())
	if !ref.ParticipantEligible || len(ref.RequiredApproverRefs) != 1 || ref.RequiredApproverRefs[0].SubjectID != "pm-item" || ref.RequiredApproverRefs[0].NodeRole != "PROJECT_EXPENSE_OWNER" || ref.RequiredApproverRefs[0].SourceVersion != 8 {
		t.Fatal("wrong service owner", ref.RequiredApproverRefs)
	}
	item.ProjectManagerID = ""
	ref = ExpenseReference(p, &item, "lead", nil, time.Now().UTC())
	if !ref.ParticipantEligible || len(ref.RequiredApproverRefs) != 0 {
		t.Fatal("team lead or project manager fallback", ref.RequiredApproverRefs)
	}
	ref = ExpenseReference(p, nil, "owner", nil, time.Now().UTC())
	if !ref.ParticipantEligible || len(ref.RequiredApproverRefs) != 1 || ref.RequiredApproverRefs[0].SubjectID != "pm-project" || ref.RequiredApproverRefs[0].SourceField != "manager_identity_id" || ref.RequiredApproverRefs[0].SourceVersion != 7 {
		t.Fatal("wrong project expense owner", ref.RequiredApproverRefs)
	}
	p.ManagerIdentityID = ""
	ref = ExpenseReference(p, nil, "owner", nil, time.Now().UTC())
	if !ref.ParticipantEligible || len(ref.RequiredApproverRefs) != 0 {
		t.Fatal("owner used as expense owner")
	}
}
