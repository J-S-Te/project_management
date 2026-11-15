package domain

import "testing"

// TestReviewedPhaseLabelUsesCompletedReview 验证审核通过阶段不再展示为审核中。
// 参数：t 为测试上下文。
// 返回值：无；规范化输入没有显示“已审核”时失败。
func TestReviewedPhaseLabelUsesCompletedReview(t *testing.T) {
	for _, code := range []string{"REVIEWED", " reviewed "} {
		if got := ReportPhaseLabel(code); got != "已审核" {
			t.Errorf("ReportPhaseLabel(%q)=%q，期望已审核", code, got)
		}
	}
}
