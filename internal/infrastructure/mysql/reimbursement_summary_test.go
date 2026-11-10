package mysql

import (
	"context"
	"encoding/json"
	"github.com/j-s-te/project-management/internal/domain"
	"strings"
	"testing"
)

// TestExpenseSummaryFiltersCategoryAndPaymentDate 验证摘要仅公开当前项目类别和付款日期。
// 参数：t 为隔离测试上下文。
// 返回值：无。
func TestExpenseSummaryFiltersCategoryAndPaymentDate(t *testing.T) {
	r := expenseRepositoryFixture(t)
	r.db.Create(&projectRecord{ID: "other", TenantID: "t", Name: "其他项目", Version: 1})
	s := expenseSpendFixture()
	s.Effects[0].AmountMinor = "300"
	s.Effects[0].Allocations = append(s.Effects[0].Allocations, domain.SpendAllocation{ProjectID: "other", FeeType: "PRIVATE", AmountMinor: "200"})
	later := s.Effects[0]
	later.EffectID = "private-payment"
	later.PaymentRecordID = "private-payment"
	later.AmountMinor = "200"
	later.OccurredAt = "2026-11-11T01:00:00Z"
	later.Allocations = []domain.SpendAllocation{{ProjectID: "other", FeeType: "PRIVATE", AmountMinor: "200"}}
	reverse := s.Effects[0]
	reverse.EffectID = "reverse"
	reverse.Kind = "PAYMENT_FULLY_REVERSED"
	reverse.ReversesEffectID = "pay"
	reverse.AmountMinor = "-300"
	reverse.OccurredAt = "2026-11-12T01:00:00Z"
	reverse.Allocations = []domain.SpendAllocation{{ProjectID: "p", FeeType: "TRAVEL", AmountMinor: "-100"}, {ProjectID: "other", FeeType: "PRIVATE", AmountMinor: "-200"}}
	s.Effects = append(s.Effects, later, reverse)
	s.Contributions = []domain.SpendContribution{{ProjectID: "p", FeeType: "TRAVEL", PaidNetMinor: "0"}, {ProjectID: "other", FeeType: "PRIVATE", PaidNetMinor: "200"}}
	s.SnapshotHash, _ = domain.SpendSnapshotHash(s)
	if _, e := r.ApplyExpenseSpend(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	rows, e := r.ReadExpenseSpendSummary(context.Background(), "t", "p", "", 20)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rows)
	var out []struct {
		PaidNetMinor          string `json:"paid_net_minor"`
		LastPaymentOccurredAt string `json:"last_payment_occurred_at"`
		FeeCategories         []struct {
			FeeType      string `json:"fee_type"`
			PaidNetMinor string `json:"paid_net_minor"`
		} `json:"fee_categories"`
	}
	json.Unmarshal(raw, &out)
	if len(out) != 1 || out[0].PaidNetMinor != "0" || out[0].LastPaymentOccurredAt != "2026-11-10T01:00:00Z" || len(out[0].FeeCategories) != 1 || out[0].FeeCategories[0].FeeType != "TRAVEL" || out[0].FeeCategories[0].PaidNetMinor != "0" {
		t.Fatalf("summary lost project boundary or dates: %s", raw)
	}
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "private-payment") {
		t.Fatalf("other project leaked %s", raw)
	}
}
