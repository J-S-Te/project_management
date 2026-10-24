package handover

import "testing"

// 回执主键必须确定且抗碰撞（旧实现对 mod 1e6，item-6609 与 item-10020 会撞）。
func TestShortReceiptIDIsDeterministicAndCollisionResistant(t *testing.T) {
	first := shortReceiptID("handover:batch:item-6609")
	second := shortReceiptID("handover:batch:item-10020")
	if first == second {
		t.Fatalf("collision: both keys map to %s", first)
	}
	if len(first) != 26 || len(second) != 26 {
		t.Fatalf("receipt id must be 26 chars, got %d and %d", len(first), len(second))
	}
	if shortReceiptID("handover:batch:item-6609") != first {
		t.Fatal("receipt id must be deterministic for the same idempotency key")
	}
	seen := make(map[string]struct{}, 20000)
	for i := 0; i < 20000; i++ {
		id := shortReceiptID(fmtKey(i))
		if _, ok := seen[id]; ok {
			t.Fatalf("unexpected collision at %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func fmtKey(i int) string { return "handover:batch:item-" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
