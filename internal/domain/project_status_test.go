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
// 全部终止时进度为 0：项目被放弃不等于交付完成（前端把 100% 与成功交付视觉绑定），
// "存在终止项"这条信息由风险口径 IsRiskProject 承载。
func TestDeriveProjectProgressExcludesTerminatedItems(t *testing.T) {
	// 等级表最高级为 8（现场完成 + 报告归档），待实施为 2。
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: ProjectStatusPendingExecution}}); got != 25 {
		t.Fatalf("一个终止 + 一个待实施 = %d, want 25（只按未终止项计算）", got)
	}
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusTerminated}, {Status: ProjectStatusTerminated}}); got != 0 {
		t.Fatalf("全部终止 = %d, want 0（项目被放弃，不是交付完成）", got)
	}
	if got := DeriveProjectProgress(nil); got != 0 {
		t.Fatalf("无服务项 = %d, want 0", got)
	}
	if got := DeriveProjectProgress([]ProjectStatusItem{{Status: ProjectStatusPendingAllocation}, {Status: ProjectStatusPendingAllocation}}); got != 12 {
		t.Fatalf("两个待分配 = %d, want 12", got)
	}
}

// 风险口径必须同时覆盖"派生状态风险"与"存在终止项"两类：
// 只终止了部分服务项、其余已归档的项目派生状态是「已完成」，
// 但它确实有被砍掉的工作，管理者必须能在风险口径里看到。
func TestIsRiskProjectCoversPartialTermination(t *testing.T) {
	completed := ProjectStatusItem{Status: ProjectStatusFieldCompleted, ReportStatus: "ARCHIVED"}
	terminated := ProjectStatusItem{Status: ProjectStatusTerminated}
	pending := ProjectStatusItem{Status: ProjectStatusPendingExecution}

	if !IsRiskProject(ProjectStatusCompleted, []ProjectStatusItem{completed, terminated}) {
		t.Fatal("含终止项的项目必须计入风险，即使派生状态是「已完成」")
	}
	if !IsRiskProject(ProjectStatusTerminated, []ProjectStatusItem{terminated}) {
		t.Fatal("全终止项目必须计入风险")
	}
	if !IsRiskProject(ProjectStatusException, []ProjectStatusItem{{Status: ProjectStatusException}}) {
		t.Fatal("异常处理中必须计入风险")
	}
	if IsRiskProject(ProjectStatusInProgress, []ProjectStatusItem{pending, completed}) {
		t.Fatal("无终止项且状态正常的项目不应计入风险")
	}
	if IsRiskProject(ProjectStatusCompleted, nil) {
		t.Fatal("无服务项且状态为已完成的项目不应计入风险")
	}
}

// 服务项状态等级表必须由单一来源派生：新增状态常量却忘记登记等级，
// 会让该状态被静默当成最滞后（等级 0），把整个项目状态拖回「待拆解确认」。
func TestServiceItemStatusRankCoversEveryKnownStatus(t *testing.T) {
	for _, status := range serviceItemStatusStages[0].Statuses {
		if _, ok := serviceItemStatusRank[status]; !ok {
			t.Fatalf("等级表缺少第一阶段状态 %q", status)
		}
	}
	for _, stage := range serviceItemStatusStages {
		for _, status := range stage.Statuses {
			if got := serviceItemStatusRank[status]; got != stage.Rank {
				t.Fatalf("状态 %q 的等级 = %d, want %d", status, got, stage.Rank)
			}
		}
	}
	// 已知状态不得被识别为未知；未知状态必须能被认出来。
	known := []ProjectStatusItem{{Status: ProjectStatusPendingExecution}, {Status: ProjectStatusTerminated}}
	if unknown := UnknownServiceItemStatuses(known); len(unknown) != 0 {
		t.Fatalf("已知状态被误判为未知: %v", unknown)
	}
	broken := []ProjectStatusItem{{Status: ProjectStatusPendingExecution}, {Status: "未来新增状态"}, {Status: "未来新增状态"}}
	unknown := UnknownServiceItemStatuses(broken)
	if len(unknown) != 1 || unknown[0] != "未来新增状态" {
		t.Fatalf("未知状态必须被识别且去重，实际 %v", unknown)
	}
}
