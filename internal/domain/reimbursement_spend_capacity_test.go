package domain

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// capacitySnapshot 创建包含完整原付款和全额反向的协议边界样例。
// 参数：count 为单次付款的项目分摊数量。
// 返回值：带正确摘要的全历史快照。
func capacitySnapshot(count int) SpendSnapshot {
	p := SpendEffect{EffectID: "pay", PaymentRecordID: "pay", Kind: "PAYMENT_RECORDED", AmountMinor: strconv.Itoa(count), OccurredAt: "2026-11-10T01:00:00Z", RecordedAt: "2026-11-10T01:00:00Z", ApprovedContentHash: strings.Repeat("a", 64)}
	r := p
	r.EffectID, r.Kind, r.ReversesEffectID, r.AmountMinor = "reverse", "PAYMENT_FULLY_REVERSED", "pay", "-"+p.AmountMinor
	s := SpendSnapshot{SchemaVersion: 1, EventID: "event", SourceApplication: "reimbursement", SourceEnvironment: "test", TenantID: "t", ClaimID: "c", ClaimNo: "BX-1", DocumentRevision: 2, ApprovalRound: 1, ProjectionRevision: 2, CompleteSnapshot: true, Trigger: "PAYMENT_FULLY_REVERSED", Currency: "CNY"}
	for i := 0; i < count; i++ {
		project := fmt.Sprintf("project-%d", i)
		p.Allocations = append(p.Allocations, SpendAllocation{ProjectID: project, FeeType: "TRAVEL", AmountMinor: "1"})
		r.Allocations = append(r.Allocations, SpendAllocation{ProjectID: project, FeeType: "TRAVEL", AmountMinor: "-1"})
		s.Contributions = append(s.Contributions, SpendContribution{ProjectID: project, FeeType: "TRAVEL", PaidNetMinor: "0"})
	}
	s.Effects = []SpendEffect{p, r}
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	return s
}

// TestSpendCapacityAllows501AllocationReversal 验证既有合法付款501条可完整反向。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSpendCapacityAllows501AllocationReversal(t *testing.T) {
	if err := ValidateSpendSnapshot(capacitySnapshot(501)); err != nil {
		t.Fatalf("501条付款反向被历史累计上限卡死: %v", err)
	}
}

// TestSpendCapacityAllowsMaximumLegacyReversal 验证旧协议最大1000条付款完整冲正仍合法。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSpendCapacityAllowsMaximumLegacyReversal(t *testing.T) {
	if err := ValidateSpendSnapshot(capacitySnapshot(1000)); err != nil {
		t.Fatalf("旧合法付款无法全额冲正: %v", err)
	}
}

// TestSpendCapacityKeepsBoundedImmutableHistory 验证重复完整历史不能绕过受控累计边界。
// 参数：t 为测试上下文。
// 返回值：无。
func TestSpendCapacityKeepsBoundedImmutableHistory(t *testing.T) {
	s := capacitySnapshot(1000)
	p, r := s.Effects[0], s.Effects[1]
	p.EffectID, p.PaymentRecordID = "pay2", "pay2"
	r.EffectID, r.PaymentRecordID, r.ReversesEffectID = "reverse2", "pay2", "pay2"
	s.Effects = append(s.Effects, p, r)
	s.SnapshotHash, _ = SpendSnapshotHash(s)
	if ValidateSpendSnapshot(s) == nil {
		t.Fatal("4000条历史突破2000条累计硬上限")
	}
}
