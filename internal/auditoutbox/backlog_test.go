package auditoutbox

import "testing"

// C2：pending 汇总待投递/退避重试/租约中，dead 汇总达到上限的终态失败。
func TestBacklogFromStatusCountsSeparatesPendingAndDead(t *testing.T) {
	got := backlogFromStatusCounts(map[string]int64{"pending": 3, "retry": 2, "processing": 1, "delivered": 9, "failed": 4})
	if got.Pending != 6 || got.Dead != 4 {
		t.Fatalf("backlog = %+v, want pending 6 dead 4", got)
	}
}

// C2：空表与全投递完成都必须折算成 0，而不是"指标缺失"。
func TestBacklogFromStatusCountsZeroWhenNothingPending(t *testing.T) {
	for name, counts := range map[string]map[string]int64{
		"empty":     {},
		"delivered": {"delivered": 12},
		"zero rows": {"pending": 0, "failed": 0},
	} {
		if got := backlogFromStatusCounts(counts); got.Pending != 0 || got.Dead != 0 {
			t.Fatalf("%s backlog = %+v, want zeros", name, got)
		}
	}
}

// C2：未识别状态按积压计入 pending，避免新增状态被静默漏告警。
func TestBacklogFromStatusCountsCountsUnknownStatusAsPending(t *testing.T) {
	if got := backlogFromStatusCounts(map[string]int64{"quarantined": 2}); got.Pending != 2 || got.Dead != 0 {
		t.Fatalf("backlog = %+v, want pending 2", got)
	}
}
