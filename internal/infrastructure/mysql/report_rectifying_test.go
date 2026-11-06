package mysql

import (
	"testing"

	"github.com/j-s-te/project-management/internal/domain"
)

// TestReportRectifyingRankRequiresSequentialAdvance 验证整改作为主报告第一阶段且未知阶段拒绝。
// 参数：t 为测试上下文。
// 返回值：无；顺序不连续或错误放行未知阶段时测试失败。
// 阶段序号来自 domain 的唯一报告阶段表（本层不再维护第二份清单）：
// 未知码统一为 -1（不可判定），NONE 为 0。
func TestReportRectifyingRankRequiresSequentialAdvance(t *testing.T) {
	for _, test := range []struct {
		phase string
		rank  int
	}{
		{"NONE", 0}, {"RECTIFYING", 1}, {"COMPILING", 2}, {"REVIEWED", 3}, {"ISSUED", 4}, {"ARCHIVED", 5}, {" rectifying ", 1}, {"unknown", -1},
	} {
		if got := domain.ReportPhaseRank(test.phase); got != test.rank {
			t.Errorf("%s rank=%d，期望%d", test.phase, got, test.rank)
		}
		if got := allowedReportPhase(test.phase); got != (test.rank > 0) {
			t.Errorf("%s允许推进=%t", test.phase, got)
		}
	}
}
