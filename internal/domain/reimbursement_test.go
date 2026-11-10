package domain

import (
	"testing"
	"time"
)

// TestExpenseEligibilityPreservesServiceBoundary 验证服务项资格不扩散且原生项目无需合同。
// 参数：t 为测试上下文。
// 返回值：无；边界不符时测试失败。
func TestExpenseEligibilityPreservesServiceBoundary(t *testing.T) {
	now := time.Now().UTC()
	p := Project{ID: "p", TenantID: "t", SourceKind: "NATIVE", Version: 1}
	a := ServiceItem{ID: "a", ProjectID: "p", TenantID: "t", EngineerIDs: []string{"u"}, Version: 1}
	b := ServiceItem{ID: "b", ProjectID: "p", TenantID: "t", Version: 1}
	if ExpenseReference(p, &a, "u", nil, now).Eligible != true {
		t.Fatal("own assignment rejected")
	}
	if ExpenseReference(p, &b, "u", nil, now).Eligible {
		t.Fatal("assignment leaked to another service item")
	}
	if ExpenseReference(p, nil, "u", nil, now).Eligible {
		t.Fatal("item assignment leaked to project scope")
	}
	p.OwnerIdentityID = "u"
	if !ExpenseReference(p, &b, "u", nil, now).Eligible {
		t.Fatal("owner project scope rejected")
	}
	p.OwnerIdentityID = ""
	grant := ExpenseParticipant{TenantID: "t", ProjectID: "p", SubjectID: "u", Status: "ACTIVE", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), Version: 2}
	if !ExpenseReference(p, nil, "u", &grant, now).Eligible {
		t.Fatal("explicit grant rejected")
	}
	grant.ValidUntil = now
	if ExpenseReference(p, nil, "u", &grant, now).Eligible {
		t.Fatal("expired grant accepted")
	}
}

// expenseSnapshotFixture 创建完整付款与其对应分摊的协议样例。
// 参数：无。
// 返回值：未填充摘要的完整快照。
func expenseSnapshotFixture() SpendSnapshot {
	return SpendSnapshot{SchemaVersion: 1, EventID: "e1", SourceApplication: "reimbursement", SourceEnvironment: "test", TenantID: "t", ClaimID: "c", ClaimNo: "BX-1", DocumentRevision: 1, ApprovalRound: 1, ProjectionRevision: 1, CompleteSnapshot: true, Trigger: "PAYMENT_RECORDED", Currency: "CNY", Effects: []SpendEffect{{EffectID: "pay", PaymentRecordID: "pay", Kind: "PAYMENT_RECORDED", AmountMinor: "100", OccurredAt: "2026-11-10T01:00:00Z", RecordedAt: "2026-11-10T01:00:00Z", ApprovedContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Allocations: []SpendAllocation{{ProjectID: "p", FeeType: "TRAVEL", AmountMinor: "100"}}}}, Contributions: []SpendContribution{{ProjectID: "p", FeeType: "TRAVEL", PaidNetMinor: "100"}}}
}

// TestSpendSnapshotProtocol 验证摘要、分摊和全额冲正必须一致。
// 参数：t 为测试上下文。
// 返回值：无；协议错误未拒绝时测试失败。
func TestSpendSnapshotProtocol(t *testing.T) {
	s := expenseSnapshotFixture()
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	if err := ValidateSpendSnapshot(s); err != nil {
		t.Fatal(err)
	}
	s.Contributions[0].PaidNetMinor = "99"
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	if ValidateSpendSnapshot(s) == nil {
		t.Fatal("unbalanced contribution accepted")
	}
	s = expenseSnapshotFixture()
	reverse := s.Effects[0]
	reverse.EffectID = "rev"
	reverse.Kind = "PAYMENT_FULLY_REVERSED"
	reverse.ReversesEffectID = "pay"
	reverse.AmountMinor = "-100"
	reverse.Allocations = []SpendAllocation{{ProjectID: "p", FeeType: "TRAVEL", AmountMinor: "-100"}}
	s.Effects = append(s.Effects, reverse)
	s.Contributions[0].PaidNetMinor = "0"
	s.Trigger = "PAYMENT_FULLY_REVERSED"
	s.ProjectionRevision = 2
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	if err := ValidateSpendSnapshot(s); err != nil {
		t.Fatal(err)
	}
	s.Effects[1].Allocations[0].ProjectID = "other"
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	if ValidateSpendSnapshot(s) == nil {
		t.Fatal("reverse allocation drift accepted")
	}
}
