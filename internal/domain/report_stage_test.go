package domain

import "testing"

// TestReportPhaseStagesAreTheSingleSourceOfTruth 守护报告阶段表的唯一性：
// 序号连续、码唯一、生命周期等级单调、展示文案齐全，且导出视图与派生函数一致。
// 参数：t 为测试上下文。
// 返回值：无；表漂移时测试失败。
func TestReportPhaseStagesAreTheSingleSourceOfTruth(t *testing.T) {
	stages := ReportPhaseStages()
	if len(stages) != 6 {
		t.Fatalf("报告阶段数量=%d，期望6", len(stages))
	}
	seenCode := map[string]bool{}
	for index, stage := range stages {
		if stage.Phase != index {
			t.Fatalf("%s 的 Phase=%d，期望按表顺序 %d", stage.Code, stage.Phase, index)
		}
		if seenCode[stage.Code] {
			t.Fatalf("报告阶段码重复: %s", stage.Code)
		}
		seenCode[stage.Code] = true
		if _, ok := reportPhaseLabels[stage.Code]; !ok {
			t.Fatalf("%s 缺少展示文案", stage.Code)
		}
		if got := ReportPhaseRank(stage.Code); got != stage.Phase {
			t.Fatalf("ReportPhaseRank(%s)=%d，期望%d", stage.Code, got, stage.Phase)
		}
		if !IsReportPhase(stage.Code) {
			t.Fatalf("IsReportPhase(%s)=false", stage.Code)
		}
		if got := ReportPhaseCode(stage.Phase); got != stage.Code {
			t.Fatalf("ReportPhaseCode(%d)=%q，期望%q", stage.Phase, got, stage.Code)
		}
		if stage.Phase > 0 && stage.LifecycleRank < stages[stage.Phase-1].LifecycleRank {
			t.Fatalf("%s 的生命周期等级 %d 小于前一阶段", stage.Code, stage.LifecycleRank)
		}
	}
	// 副本：调用方修改不能污染表。
	stages[0].Code = "MUTATED"
	if ReportPhaseStages()[0].Code == "MUTATED" {
		t.Fatal("ReportPhaseStages 泄漏了可变全局状态")
	}

	// NONE 不在 SLA 配置清单内；其余 5 个阶段按报告链顺序。
	slaCodes := ReportPhaseSLACodes()
	wantSLA := []string{ReportPhaseRectifying, ReportPhaseCompiling, ReportPhaseReviewed, ReportPhaseIssued, ReportPhaseArchived}
	if len(slaCodes) != len(wantSLA) {
		t.Fatalf("SLA 报告阶段清单=%v，期望%d项", slaCodes, len(wantSLA))
	}
	for index, code := range wantSLA {
		if slaCodes[index] != code {
			t.Fatalf("SLA 报告阶段清单[%d]=%q，期望%q", index, slaCodes[index], code)
		}
	}

	// ServiceItemReportStages 由同一张表派生：等级 7 为整改至签发，等级 8 为归档。
	reportStages := ServiceItemReportStages()
	if len(reportStages) != 2 {
		t.Fatalf("报告等级表=%+v，期望2行", reportStages)
	}
	if reportStages[0].Rank != 7 || len(reportStages[0].Statuses) != 4 {
		t.Fatalf("等级7=%+v，期望4个阶段码", reportStages[0])
	}
	if reportStages[1].Rank != 8 || len(reportStages[1].Statuses) != 1 || reportStages[1].Statuses[0] != ReportPhaseArchived {
		t.Fatalf("等级8=%+v，期望仅归档", reportStages[1])
	}

	// 未知码与空串：rank=-1、IsReportPhase=false、code=""; NONE 的 rank 为 0。
	for _, unknown := range []string{"", "  ", "DRAFTING", "ARCHIVED2", "none2"} {
		if got := ReportPhaseRank(unknown); got != -1 {
			t.Fatalf("ReportPhaseRank(%q)=%d，期望-1", unknown, got)
		}
		if IsReportPhase(unknown) {
			t.Fatalf("IsReportPhase(%q)=true，期望false", unknown)
		}
	}
	if ReportPhaseRank(ReportPhaseNone) != 0 || !IsReportPhase(ReportPhaseNone) {
		t.Fatal("NONE 必须是已知阶段且序号为0")
	}
	if ReportPhaseCode(99) != "" {
		t.Fatal("表外序号必须返回空串")
	}
	// 归一化：大小写与首尾空白不影响判定。
	if ReportPhaseRank("  rectifying ") != 1 || !IsReportPhase(" Compiling ") || ReportPhaseLabel(" issued ") != "已签发" {
		t.Fatal("报告阶段判定未做大小写/空白归一化")
	}
	if ReportPhaseLabel("DRAFTING") != "DRAFTING" {
		t.Fatal("未知阶段的展示名应回退为归一化后的码")
	}
}

// TestDeriveProjectReportStageMatrix 覆盖项目级报告阶段的全部边界：
// 无服务项、全终止、未到报告段、NONE 与后续阶段混合、空报告状态、
// 未知码、大小写空格、归档与待整改混合、终止项剔除与补充协议不短路。
// 参数：t 为测试上下文。
// 返回值：无；任一分支与"取最滞后阶段"原则不符时测试失败。
func TestDeriveProjectReportStageMatrix(t *testing.T) {
	field := func(phase string) ProjectStatusItem {
		return ProjectStatusItem{Status: ProjectStatusFieldCompleted, ReportStatus: phase}
	}
	terminated := ProjectStatusItem{Status: ProjectStatusTerminated}
	cases := []struct {
		name  string
		items []ProjectStatusItem
		want  string
	}{
		{"无服务项不进入报告段", nil, ""},
		{"全终止不进入报告段", []ProjectStatusItem{terminated, terminated}, ""},
		{"现场实施中未到报告段", []ProjectStatusItem{{Status: ProjectStatusInProgress, ReportStatus: ReportPhaseCompiling}}, ""},
		{"待拆解确认未到报告段", []ProjectStatusItem{{Status: "待确认", ReportStatus: ReportPhaseArchived}}, ""},
		{"混合未完成项时整项不可判定", []ProjectStatusItem{field(ReportPhaseArchived), {Status: ProjectStatusPreparing}}, ""},
		{"现场完成待整改", []ProjectStatusItem{field(ReportPhaseNone)}, ReportPhaseNone},
		{"空报告状态视为NONE", []ProjectStatusItem{field("")}, ReportPhaseNone},
		{"空白报告状态视为NONE", []ProjectStatusItem{field("   ")}, ReportPhaseNone},
		{"NONE 胜过 RECTIFYING", []ProjectStatusItem{field(ReportPhaseRectifying), field(ReportPhaseNone)}, ReportPhaseNone},
		{"整改中", []ProjectStatusItem{field(ReportPhaseRectifying)}, ReportPhaseRectifying},
		{"编制中", []ProjectStatusItem{field(ReportPhaseCompiling)}, ReportPhaseCompiling},
		{"审核中", []ProjectStatusItem{field(ReportPhaseReviewed)}, ReportPhaseReviewed},
		{"已签发", []ProjectStatusItem{field(ReportPhaseIssued)}, ReportPhaseIssued},
		{"全部归档", []ProjectStatusItem{field(ReportPhaseArchived), field(ReportPhaseArchived)}, ReportPhaseArchived},
		{"归档与待整改混合取待整改", []ProjectStatusItem{field(ReportPhaseArchived), field(ReportPhaseNone)}, ReportPhaseNone},
		{"归档与整改中混合取整改中", []ProjectStatusItem{field(ReportPhaseArchived), field(ReportPhaseRectifying)}, ReportPhaseRectifying},
		{"未知码不可判定", []ProjectStatusItem{field("DRAFTING")}, ""},
		{"未知码与已知码混合仍不可判定", []ProjectStatusItem{field(ReportPhaseCompiling), field("SUBMITTED")}, ""},
		{"大小写与空白归一化", []ProjectStatusItem{field("  compiling  ")}, ReportPhaseCompiling},
		{"已终止项被剔除", []ProjectStatusItem{terminated, field(ReportPhaseCompiling)}, ReportPhaseCompiling},
		{"补充协议不短路报告维度", []ProjectStatusItem{field(ReportPhaseArchived)}, ReportPhaseArchived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveProjectReportStage(tc.items); got != tc.want {
				t.Fatalf("DeriveProjectReportStage()=%q，期望%q", got, tc.want)
			}
		})
	}
}

// TestProjectReportStageInvariants 守护项目状态与报告阶段的正交不变量：
// 现场实施完成⇒NONE 或不可判定；报告编制⇒RECTIFYING..ISSUED；
// 已完成⇒ARCHIVED；其余线性节点（未到现场完成）⇒不可判定。
// 参数：t 为测试上下文。
// 返回值：无；两个派生函数口径分裂时测试失败。
func TestProjectReportStageInvariants(t *testing.T) {
	phases := []string{"", ReportPhaseNone, ReportPhaseRectifying, ReportPhaseCompiling, ReportPhaseReviewed, ReportPhaseIssued, ReportPhaseArchived, "DRAFTING"}
	for _, phase := range phases {
		items := []ProjectStatusItem{{Status: ProjectStatusFieldCompleted, ReportStatus: phase}}
		status := DeriveProjectStatus(items, "NONE", "")
		stage := DeriveProjectReportStage(items)
		switch status {
		case ProjectStatusFieldCompleted:
			if stage != ReportPhaseNone && stage != "" {
				t.Fatalf("现场实施完成且报告=%q 时 report_stage=%q，期望 NONE 或空", phase, stage)
			}
		case ProjectStatusReporting:
			if stage != ReportPhaseRectifying && stage != ReportPhaseCompiling && stage != ReportPhaseReviewed && stage != ReportPhaseIssued {
				t.Fatalf("报告编制且报告=%q 时 report_stage=%q，期望整改中到已签发", phase, stage)
			}
		case ProjectStatusCompleted:
			if stage != ReportPhaseArchived {
				t.Fatalf("已完成且报告=%q 时 report_stage=%q，期望 ARCHIVED", phase, stage)
			}
		default:
			t.Fatalf("单个现场完成项不应派生出 %q", status)
		}
	}

	// 其余线性节点（待拆解确认 → 异常处理中）都不在报告段。
	for _, status := range []string{
		ProjectStatusPendingDecomposition, ProjectStatusPendingAllocation,
		ProjectStatusPendingExecution, ProjectStatusPreparing, ProjectStatusInProgress, ProjectStatusException,
	} {
		items := []ProjectStatusItem{{Status: status, ReportStatus: ReportPhaseArchived}}
		if got := DeriveProjectReportStage(items); got != "" {
			t.Fatalf("%q 节点不应有报告阶段，实际 %q", status, got)
		}
	}

	// 分支节点与报告维度正交：全终止没有报告段；补充协议处理中由项目状态承载，
	// 不影响报告阶段本身（此处报告阶段只读服务项）。
	terminated := []ProjectStatusItem{{Status: ProjectStatusTerminated}}
	if status := DeriveProjectStatus(terminated, "NONE", ""); status != ProjectStatusTerminated {
		t.Fatalf("全终止项目状态=%q", status)
	}
	if got := DeriveProjectReportStage(terminated); got != "" {
		t.Fatalf("全终止项目 report_stage=%q，期望空", got)
	}
	supplement := []ProjectStatusItem{{Status: ProjectStatusFieldCompleted, ReportStatus: ReportPhaseRectifying}}
	if status := DeriveProjectStatus(supplement, "REQUIRED", ""); status != ProjectStatusSupplementRequired {
		t.Fatalf("补充协议项目状态=%q", status)
	}
	if got := DeriveProjectReportStage(supplement); got != ReportPhaseRectifying {
		t.Fatalf("补充协议不应短路报告阶段，实际 %q", got)
	}
}

// TestUnknownReportPhasesExposesUnknownCodes 验证未知报告状态被显式暴露而非静默归入 NONE。
// 参数：t 为测试上下文。
// 返回值：无；未知码漏报或重复上报时测试失败。
func TestUnknownReportPhasesExposesUnknownCodes(t *testing.T) {
	items := []ProjectStatusItem{
		{Status: ProjectStatusFieldCompleted, ReportStatus: "DRAFTING"},
		{Status: ProjectStatusFieldCompleted, ReportStatus: " drafting "},
		{Status: ProjectStatusFieldCompleted, ReportStatus: "RECTIFYING"},
		{Status: ProjectStatusFieldCompleted, ReportStatus: ""},
		{Status: ProjectStatusTerminated, ReportStatus: "SUBMITTED"},
		{Status: ProjectStatusInProgress, ReportStatus: "UNKNOWN_PHASE"},
	}
	got := UnknownReportPhases(items)
	if len(got) != 2 || got[0] != "DRAFTING" || got[1] != "UNKNOWN_PHASE" {
		t.Fatalf("未知报告状态=%v，期望 [DRAFTING UNKNOWN_PHASE]（去重、剔除终止项、按首次出现顺序）", got)
	}
	if len(UnknownReportPhases([]ProjectStatusItem{{Status: ProjectStatusFieldCompleted, ReportStatus: ReportPhaseCompiling}})) != 0 {
		t.Fatal("已知报告状态不应上报为未知")
	}
}
