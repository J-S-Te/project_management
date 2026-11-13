package domain

import "strings"

// 项目状态节点：一个项目在任一时刻必须恰好落在其中一个节点上。
// 顺序即交付推进顺序，也是「取最滞后服务项」汇总规则的依据；
// 前端与服务端都必须只使用这些节点，不得各自拼装状态集合。
const (
	ProjectStatusPendingDecomposition = "待拆解确认"
	ProjectStatusPendingAllocation    = "待分配"
	ProjectStatusPendingExecution     = "待实施"
	ProjectStatusPreparing            = "实施准备中"
	ProjectStatusInProgress           = "实施中"
	ProjectStatusException            = "异常处理中"
	ProjectStatusFieldCompleted       = "现场实施完成"
	ProjectStatusReporting            = "报告编制"
	ProjectStatusCompleted            = "已完成"
)

// 分支节点：不属于线性推进序列，按业务标记单独判定。
const (
	ProjectStatusTerminated         = "已终止"
	ProjectStatusSupplementRequired = "补充协议处理中"
)

// projectStatusNodes 是线性推进节点的唯一顺序表，索引即推进等级。
// 「待制定计划」已移除：任务分配完成即具备计划前置条件，不存在只等排期的独立阶段。
var projectStatusNodes = []string{
	ProjectStatusPendingDecomposition,
	ProjectStatusPendingAllocation,
	ProjectStatusPendingExecution,
	ProjectStatusPreparing,
	ProjectStatusInProgress,
	ProjectStatusException,
	ProjectStatusFieldCompleted,
	ProjectStatusReporting,
	ProjectStatusCompleted,
}

// 主报告阶段码：默认 NONE → RECTIFYING → COMPILING → REVIEWED → ISSUED → ARCHIVED。
// 无需整改时允许 NONE 直接进入 COMPILING，其余节点保持顺序推进。
// 只有 NONE 表示"尚未进入报告流程"，其余五个阶段都可以作为 SLA 停留规则的配置值。
const (
	ReportPhaseNone       = "NONE"
	ReportPhaseRectifying = "RECTIFYING"
	ReportPhaseCompiling  = "COMPILING"
	ReportPhaseReviewed   = "REVIEWED"
	ReportPhaseIssued     = "ISSUED"
	ReportPhaseArchived   = "ARCHIVED"
)

// ReportPhaseStage 是主报告阶段的唯一事实来源，同时承载两张维度：
//   - Phase：报告链内部顺序（0..5）。项目级 report_stage 取所有服务项中**最小**
//     Phase（与项目状态"取最滞后服务项"同一原则），所以 NONE 会赢过 RECTIFYING。
//   - LifecycleRank：该阶段对应的项目生命周期推进等级，取值与 projectStatusNodes
//     下标一致（现场实施完成=6、报告编制=7、已完成=8）。
//
// 表外的未知码不得静默归入任何阶段：ReportPhaseRank 返回 -1、IsReportPhase
// 返回 false，由调用方显式按"不可判定"处理。
type ReportPhaseStage struct {
	Code          string
	Phase         int
	LifecycleRank int
}

// reportPhaseStages 是报告阶段的唯一定义：ServiceItemReportStages、
// serviceItemLifecycleRank、SLA 报告阶段清单都由它派生，
// 禁止在基础设施层、应用层或前端另写一份状态清单。
var reportPhaseStages = []ReportPhaseStage{
	// NONE 表示待整改：仍属「现场实施完成」等级，因此生命周期等级为 6。
	{Code: ReportPhaseNone, Phase: 0, LifecycleRank: 6},
	{Code: ReportPhaseRectifying, Phase: 1, LifecycleRank: 7},
	{Code: ReportPhaseCompiling, Phase: 2, LifecycleRank: 7},
	{Code: ReportPhaseReviewed, Phase: 3, LifecycleRank: 7},
	{Code: ReportPhaseIssued, Phase: 4, LifecycleRank: 7},
	{Code: ReportPhaseArchived, Phase: 5, LifecycleRank: 8},
}

// reportPhaseByCode / reportPhaseCodeByPhase / reportLifecycleRank 均由上表派生。
var (
	reportPhaseByCode      = make(map[string]ReportPhaseStage, len(reportPhaseStages))
	reportPhaseCodeByPhase = make(map[int]string, len(reportPhaseStages))
	reportLifecycleRank    = make(map[string]int, len(reportPhaseStages))
)

func init() {
	for _, stage := range reportPhaseStages {
		reportPhaseByCode[stage.Code] = stage
		reportPhaseCodeByPhase[stage.Phase] = stage.Code
		reportLifecycleRank[stage.Code] = stage.LifecycleRank
	}
}

// reportPhaseLabels 是报告阶段的展示文案。它只用于通知/目录等展示，
// 不参与任何判定；与阶段表的键一致性由单元测试守护。
var reportPhaseLabels = map[string]string{
	ReportPhaseNone:       "待整改",
	ReportPhaseRectifying: "整改中",
	ReportPhaseCompiling:  "编制中",
	ReportPhaseReviewed:   "已审核",
	ReportPhaseIssued:     "已签发",
	ReportPhaseArchived:   "已归档",
}

// normalizeReportPhase 统一报告阶段码的归一化方式：去首尾空白并转大写。
// 报告状态列存的就是大写码，但历史数据或手工 SQL 可能带空格/小写，
// 所有判定入口（领域、SQL、SLA）必须走同一归一化。
func normalizeReportPhase(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ReportPhaseRank 返回报告阶段在报告链中的顺序。
// 参数：code 为报告阶段码，忽略大小写与首尾空白。
// 返回值：NONE 为 0，RECTIFYING..ARCHIVED 为 1..5；表外未知码返回 -1。
func ReportPhaseRank(code string) int {
	if stage, ok := reportPhaseByCode[normalizeReportPhase(code)]; ok {
		return stage.Phase
	}
	return -1
}

// CanAdvanceReportPhase 判断报告是否按默认顺序推进或在待整改时直接开始编制。
// 参数：current 为当前报告阶段，target 为本次操作选择的目标阶段。
// 返回值：相邻推进或 NONE 到 COMPILING 返回 true，未知、重复或其它跳步返回 false。
func CanAdvanceReportPhase(current, target string) bool {
	from, to := ReportPhaseRank(current), ReportPhaseRank(target)
	if from < 0 || to <= 0 {
		return false
	}
	return to == from+1 ||
		(normalizeReportPhase(current) == ReportPhaseNone &&
			normalizeReportPhase(target) == ReportPhaseCompiling)
}

// IsReportPhase 判断 code 是否是已知的主报告阶段码（含 NONE）。
// 参数：code 为报告阶段码，忽略大小写与首尾空白。
// 返回值：表内返回 true，空串与未知码返回 false。
func IsReportPhase(code string) bool {
	_, ok := reportPhaseByCode[normalizeReportPhase(code)]
	return ok
}

// ReportPhaseCode 返回阶段序号对应的报告阶段码，供 SQL 侧按派生 Phase 分组后
// 反查展示码。参数：phase 为 0..5 的阶段序号；返回值：命中返回码，表外返回 ""。
func ReportPhaseCode(phase int) string {
	return reportPhaseCodeByPhase[phase]
}

// ReportPhaseStages 返回报告阶段表的只读副本（含 NONE），供展示与校验使用。
func ReportPhaseStages() []ReportPhaseStage {
	stages := make([]ReportPhaseStage, len(reportPhaseStages))
	copy(stages, reportPhaseStages)
	return stages
}

// ReportPhaseSLACodes 返回可作为 SLA 停留规则的 5 个报告阶段码（不含 NONE，
// 按报告链顺序）：NONE 表示报告尚未开始，其停留由「现场实施完成」状态规则覆盖。
// SLA 规则目录、监控 SQL 谓词与 Go 侧判定都从这里取清单，避免三处各写一份。
func ReportPhaseSLACodes() []string {
	codes := make([]string, 0, len(reportPhaseStages))
	for _, stage := range reportPhaseStages {
		if stage.Phase == 0 {
			continue
		}
		codes = append(codes, stage.Code)
	}
	return codes
}

// ReportPhaseLabel 返回报告阶段的中文展示名（如「整改中」）。
// 参数：code 为报告阶段码，忽略大小写与首尾空白。
// 返回值：命中返回中文名，未知码返回去空白后的大写码本身。
func ReportPhaseLabel(code string) string {
	normalized := normalizeReportPhase(code)
	if label, ok := reportPhaseLabels[normalized]; ok {
		return label
	}
	return normalized
}

// serviceItemStatusStages 是服务项状态到推进等级的**唯一来源**：同阶段的状态取相同等级。
// rank 表由它派生，避免"新增状态却忘了登记等级"——那种漂移会让该状态被静默当成最滞后（等级 0），
// 把整个项目的状态拖回「待拆解确认」。
var serviceItemStatusStages = []struct {
	Rank     int
	Statuses []string
}{
	{0, []string{"待确认", "待复核"}}, // 待确认/待复核同属「待拆解确认」阶段
	{1, []string{ProjectStatusPendingAllocation}},
	{2, []string{ProjectStatusPendingExecution}},
	{3, []string{ProjectStatusPreparing}},
	{4, []string{ProjectStatusInProgress}},
	{5, []string{ProjectStatusException}},
	{6, []string{ProjectStatusFieldCompleted}},
}

// serviceItemStatusRank 由上面的阶段表派生，不再手写第二份。
var serviceItemStatusRank = func() map[string]int {
	ranks := make(map[string]int, len(serviceItemStatusStages))
	for _, stage := range serviceItemStatusStages {
		for _, status := range stage.Statuses {
			ranks[status] = stage.Rank
		}
	}
	return ranks
}()

// UnknownServiceItemStatuses 返回投影中无法识别的服务项状态。
// 未知状态会被保守地按最滞后处理（不误报项目更晚期），但这属于数据异常：
// 调用方必须把它显式暴露出来，而不是让项目状态静默变化。
func UnknownServiceItemStatuses(items []ProjectStatusItem) []string {
	seen := map[string]struct{}{}
	unknown := make([]string, 0, len(items))
	for _, item := range items {
		status := strings.TrimSpace(item.Status)
		if status == ProjectStatusTerminated {
			continue
		}
		if _, known := serviceItemStatusRank[status]; known {
			continue
		}
		if _, done := seen[status]; done {
			continue
		}
		seen[status] = struct{}{}
		unknown = append(unknown, status)
	}
	return unknown
}

// ProjectStatusNodes 返回线性推进节点的副本，供校验与展示使用。
func ProjectStatusNodes() []string {
	nodes := make([]string, len(projectStatusNodes))
	copy(nodes, projectStatusNodes)
	return nodes
}

// riskProjectStatuses 是"风险项目"指标的唯一口径：异常处理中与已终止。
// 健康度字段移除后，风险项目不再依赖人工维护的健康标签，前端也按同一组状态计数。
var riskProjectStatuses = map[string]struct{}{
	ProjectStatusException:  {},
	ProjectStatusTerminated: {},
}

// IsRiskProjectStatus 判断派生项目状态是否计入风险项目。
func IsRiskProjectStatus(status string) bool {
	_, ok := riskProjectStatuses[status]
	return ok
}

// IsRiskProject 判定风险项目：派生状态落在风险集合内，**或项目内存在已终止的服务项**。
//
// 后者不可省略：只终止了部分服务项、其余已归档的项目，派生状态是「已完成」，
// 但它确实有被砍掉的工作，管理者必须能在风险口径里看到；把"是否交付完成"（状态）
// 与"是否有终止项"（风险）分开表达，两个指标才不会互相矛盾。
func IsRiskProject(status string, items []ProjectStatusItem) bool {
	if IsRiskProjectStatus(status) {
		return true
	}
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			return true
		}
	}
	return false
}

// ProjectStatusItem 是派生项目状态所需的最小服务项投影。
type ProjectStatusItem struct {
	Status       string
	ReportStatus string
}

// DeriveProjectStatus 计算项目的唯一状态。规则：
//
//  1. 补充协议处理中优先：拆解调整后项目进入该分支节点，直到重新确认。
//  2. 取最滞后的服务项阶段：只要还有服务项停在较早阶段，项目就停在那个阶段。
//  3. 现场实施完成后按报告阶段推进：报告编制中→报告编制，全部归档→已完成。
//  4. 全部服务项已终止→已终止。
//
// 无服务项时回退到已存储状态，避免把空项目误判为「待拆解确认」。
func DeriveProjectStatus(items []ProjectStatusItem, supplementStatus, storedStatus string) string {
	if strings.EqualFold(strings.TrimSpace(supplementStatus), "REQUIRED") {
		return ProjectStatusSupplementRequired
	}
	if len(items) == 0 {
		if stored := strings.TrimSpace(storedStatus); stored != "" {
			return stored
		}
		return ProjectStatusPendingDecomposition
	}
	lowest := len(projectStatusNodes) - 1
	terminated := 0
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			terminated++
			continue
		}
		if rank := serviceItemLifecycleRank(item); rank < lowest {
			lowest = rank
		}
	}
	if terminated == len(items) {
		return ProjectStatusTerminated
	}
	return projectStatusNodes[lowest]
}

// serviceItemLifecycleRank 把服务项状态及报告状态映射到线性推进等级。
// 参数：item 为派生项目状态所需的服务项最小投影。
// 返回值：0..8 的等级；未知或空现场状态按最滞后等级 0 处理。
func serviceItemLifecycleRank(item ProjectStatusItem) int {
	status := strings.TrimSpace(item.Status)
	if status == ProjectStatusFieldCompleted {
		// 报告等级由 reportPhaseStages 派生：NONE 不在报告表内（等级 6），
		// 未知/空报告状态同样按 6 处理，与历史行为一致。
		if rank, ok := reportLifecycleRank[normalizeReportPhase(item.ReportStatus)]; ok && rank > 6 {
			return rank
		}
		return 6
	}
	if rank, ok := serviceItemStatusRank[status]; ok {
		return rank
	}
	return 0
}

// ProjectStatusStage 是「状态 → 推进等级」分组映射的导出视图。基础设施层把派生
// 项目状态过滤下推到 SQL（分页/计数）时，必须与 DeriveProjectStatus 共用同一套表，
// 否则两处各写一份会在业务新增状态时漂移。
type ProjectStatusStage struct {
	Rank     int
	Statuses []string
}

// ServiceItemStatusStages 返回服务项状态到线性推进等级（0..6）的只读副本。
// 表外的未知/空状态一律按等级 0 处理（与 serviceItemLifecycleRank 的兜底一致）。
func ServiceItemStatusStages() []ProjectStatusStage {
	stages := make([]ProjectStatusStage, 0, len(serviceItemStatusStages))
	for _, stage := range serviceItemStatusStages {
		stages = append(stages, ProjectStatusStage{Rank: stage.Rank, Statuses: append([]string(nil), stage.Statuses...)})
	}
	return stages
}

// ServiceItemReportStages 返回现场完成后的报告等级表，供 SQL 与领域派生共用。
// 参数：无。
// 返回值：整改至签发对应等级 7，归档对应等级 8；NONE 与表外值由调用方按等级 6 处理。
// 本函数由 reportPhaseStages 派生，不再手写第二份清单。
func ServiceItemReportStages() []ProjectStatusStage {
	stages := make([]ProjectStatusStage, 0, 2)
	for _, rank := range []int{7, 8} {
		statuses := make([]string, 0, len(reportPhaseStages))
		for _, stage := range reportPhaseStages {
			if stage.LifecycleRank == rank {
				statuses = append(statuses, stage.Code)
			}
		}
		if len(statuses) > 0 {
			stages = append(stages, ProjectStatusStage{Rank: rank, Statuses: statuses})
		}
	}
	return stages
}

// DeriveProjectReportStage 计算项目级报告阶段（与项目状态正交的维度）。
//
// 不变量（由单元测试守护）：
//  1. 报告阶段只由「未终止服务项的报告状态」决定，服务项未走到现场实施完成
//     （serviceItemLifecycleRank < 6）时项目尚未进入报告段，返回 ""。
//  2. 全部服务项已终止或没有服务项时返回 ""：项目没有报告段。
//  3. 空报告状态视为 NONE。
//  4. 取**最小** Phase（最滞后），与 DeriveProjectStatus 的"取最滞后服务项"同一原则：
//     一个服务项还在待整改（NONE）、另一个已整改中时，项目明确停在 NONE；
//     取最靠前阶段会让项目报告进度虚高，这正是本项目要避免的口径。
//  5. 任一服务项报告状态是未知码时返回 ""（不可判定），未知码由
//     UnknownReportPhases 显式暴露，不得静默归入 NONE。
//  6. 补充协议处理中不短路本维度：报告阶段反映的是报告链本身，
//     分支节点由项目状态承载。
//
// 参数：items 为项目全部服务项的最小投影（含已终止项）。
// 返回值：报告阶段码（NONE/RECTIFYING/COMPILING/REVIEWED/ISSUED/ARCHIVED）
// 或 ""（未进入报告段/不可判定）。
func DeriveProjectReportStage(items []ProjectStatusItem) string {
	lowestPhase := -1
	active := 0
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			continue
		}
		active++
		if serviceItemLifecycleRank(item) < 6 {
			return ""
		}
		code := normalizeReportPhase(item.ReportStatus)
		if code == "" {
			code = ReportPhaseNone
		}
		if !IsReportPhase(code) {
			return ""
		}
		if phase := ReportPhaseRank(code); lowestPhase < 0 || phase < lowestPhase {
			lowestPhase = phase
		}
	}
	if active == 0 {
		return ""
	}
	return reportPhaseCodeByPhase[lowestPhase]
}

// UnknownReportPhases 返回投影中无法识别的报告状态码（去重，保持首次出现顺序）。
// 未知报告状态会让 DeriveProjectReportStage 返回不可判定，与未知服务项状态一样
// 属于数据异常，必须由调用方显式告警而不是静默变化。
func UnknownReportPhases(items []ProjectStatusItem) []string {
	seen := map[string]struct{}{}
	unknown := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			continue
		}
		code := normalizeReportPhase(item.ReportStatus)
		if code == "" || IsReportPhase(code) {
			continue
		}
		if _, done := seen[code]; done {
			continue
		}
		seen[code] = struct{}{}
		unknown = append(unknown, code)
	}
	return unknown
}

// DeriveProjectProgress 按服务项推进等级派生项目进度百分比：
// 各服务项等级均值除以最高等级。已终止服务项不再有剩余工作量，既不计入分子也不计入分母：
// 按满分计入会得出「1 终止 + 1 待实施 = 62%」这种与状态口径相反的进度。
// 进度不再由事件手工写入固定值，与派生状态共用同一套等级表。
// 全部终止时返回 0：项目被放弃不等于交付完成，风险由 IsRiskProject 承载。
func DeriveProjectProgress(items []ProjectStatusItem) int {
	if len(items) == 0 {
		return 0
	}
	maxRank := len(projectStatusNodes) - 1
	total, counted := 0, 0
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			continue
		}
		total += serviceItemLifecycleRank(item)
		counted++
	}
	if counted == 0 {
		// 全部已终止：项目被放弃，完成度按业务直觉是 0，而不是"100% 完成"。
		// 前端把 100% 与"成功交付"视觉绑定（绿色指标、满格进度条），
		// 用 100 表达"没有剩余工作量"会让被终止的项目看起来像交付成功。
		// "存在终止项"这条信息由风险口径承载（IsRiskProject），不靠进度表达。
		return 0
	}
	return total * 100 / (maxRank * counted)
}
