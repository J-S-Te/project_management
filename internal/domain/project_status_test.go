package domain

import "testing"

func TestDeriveProjectStatusTakesLeastAdvancedItem(t *testing.T) {
	cases := []struct {
		name  string
		items []ProjectStatusItem
		want  string
	}{
		{"待确认与待复核同属待拆解确认", []ProjectStatusItem{{Status: "待确认"}, {Status: "待复核"}}, ProjectStatusPendingDecomposition},
		{"全部待分配", []ProjectStatusItem{{Status: "待分配"}, {Status: "待分配"}}, ProjectStatusPendingAllocation},
		{"一个仍待分配则项目不前进", []ProjectStatusItem{{Status: "待分配"}, {Status: "实施中"}}, ProjectStatusPendingAllocation},
		{"异常处理中不早于实施中", []ProjectStatusItem{{Status: "实施中"}, {Status: "异常处理中"}}, ProjectStatusInProgress},
		{"实现准备中滞后于实施中", []ProjectStatusItem{{Status: "实施准备中"}, {Status: "实施中"}}, ProjectStatusPreparing},
		{"现场完成后报告未开始则停在现场完成", []ProjectStatusItem{{Status: "现场实施完成", ReportStatus: "NONE"}, {Status: "现场实施完成", ReportStatus: "COMPILING"}}, ProjectStatusFieldCompleted},
		{"全部报告编制中", []ProjectStatusItem{{Status: "现场实施完成", ReportStatus: "COMPILING"}, {Status: "现场实施完成", ReportStatus: "ISSUED"}}, ProjectStatusReporting},
		{"全部报告归档才算完成", []ProjectStatusItem{{Status: "现场实施完成", ReportStatus: "ARCHIVED"}, {Status: "现场实施完成", ReportStatus: "ARCHIVED"}}, ProjectStatusCompleted},
		{"未知状态按最滞后处理", []ProjectStatusItem{{Status: ""}, {Status: "实施中"}}, ProjectStatusPendingDecomposition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveProjectStatus(tc.items, "NONE", "待拆解确认"); got != tc.want {
				t.Fatalf("DeriveProjectStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeriveProjectStatusTerminatedOnlyWhenAllItemsTerminated(t *testing.T) {
	if got := DeriveProjectStatus([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: ProjectStatusTerminated}}, "NONE", ""); got != ProjectStatusTerminated {
		t.Fatalf("all terminated = %q", got)
	}
	if got := DeriveProjectStatus([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: "实施中"}}, "NONE", ""); got != ProjectStatusInProgress {
		t.Fatalf("partially terminated = %q", got)
	}
}

func TestDeriveProjectStatusSupplementAgreementTakesPrecedence(t *testing.T) {
	items := []ProjectStatusItem{{Status: "实施中"}}
	if got := DeriveProjectStatus(items, "REQUIRED", ""); got != ProjectStatusSupplementRequired {
		t.Fatalf("supplement required = %q", got)
	}
	if got := DeriveProjectStatus(items, "none", ""); got != ProjectStatusInProgress {
		t.Fatalf("supplement cleared = %q", got)
	}
}

func TestDeriveProjectStatusFallsBackToStoredStatusWithoutItems(t *testing.T) {
	if got := DeriveProjectStatus(nil, "NONE", ProjectStatusPendingAllocation); got != ProjectStatusPendingAllocation {
		t.Fatalf("stored fallback = %q", got)
	}
	if got := DeriveProjectStatus(nil, "NONE", ""); got != ProjectStatusPendingDecomposition {
		t.Fatalf("empty fallback = %q", got)
	}
}

func TestProjectStatusNodesAreUniqueAndOrdered(t *testing.T) {
	nodes := ProjectStatusNodes()
	if len(nodes) != 9 {
		t.Fatalf("node count = %d, want 9", len(nodes))
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		if seen[node] {
			t.Fatalf("duplicate node %q", node)
		}
		seen[node] = true
	}
	for _, branch := range []string{ProjectStatusTerminated, ProjectStatusSupplementRequired} {
		if seen[branch] {
			t.Fatalf("branch node %q must not be part of the linear sequence", branch)
		}
	}
}

// 风险项目指标在健康度字段移除后改由派生状态决定：只有异常处理中与已终止计入，
// 健康度曾经用到的"关注"等中间态不再影响风险口径。
func TestIsRiskProjectStatusUsesDerivedStatusVocabulary(t *testing.T) {
	risk := []string{ProjectStatusException, ProjectStatusTerminated}
	for _, status := range risk {
		if !IsRiskProjectStatus(status) {
			t.Fatalf("IsRiskProjectStatus(%q) = false, want true", status)
		}
	}
	safe := []string{
		ProjectStatusPendingDecomposition, ProjectStatusPendingAllocation,
		ProjectStatusPendingExecution, ProjectStatusPreparing, ProjectStatusInProgress,
		ProjectStatusFieldCompleted, ProjectStatusReporting, ProjectStatusCompleted,
		ProjectStatusSupplementRequired, "关注", "风险", "正常", "",
	}
	for _, status := range safe {
		if IsRiskProjectStatus(status) {
			t.Fatalf("IsRiskProjectStatus(%q) = true, want false", status)
		}
	}
}

// 已终止项不再有剩余工作量，必须同时从分子和分母剔除：
// 按满分计入会得出「1 终止 + 1 待实施 = 62%」这种与状态口径相反的进度。
func TestDeriveProjectProgressExcludesTerminatedItems(t *testing.T) {
	// 等级表最高级为 8（现场完成 + 报告归档），待实施为 2。
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: ProjectStatusPendingExecution}}); got != 25 {
		t.Fatalf("一个终止 + 一个待实施 = %d, want 25（只按未终止项计算）", got)
	}
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: ProjectStatusTerminated}}); got != 100 {
		t.Fatalf("全部终止 = %d, want 100（没有剩余工作量）", got)
	}
	if got := DeriveProjectProgress(nil); got != 0 {
		t.Fatalf("无服务项 = %d, want 0", got)
	}
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusPendingAllocation}, {Status: ProjectStatusPendingAllocation}}); got != 12 {
		t.Fatalf("两个待分配 = %d, want 12", got)
	}
}
