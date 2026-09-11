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

// 服务项状态到推进等级的映射；待确认/待复核属于同一「待拆解确认」阶段。
var serviceItemStatusRank = map[string]int{
	"待确认":                         0,
	"待复核":                         0,
	ProjectStatusPendingAllocation: 1,
	ProjectStatusPendingExecution:  2,
	ProjectStatusPreparing:         3,
	ProjectStatusInProgress:        4,
	ProjectStatusException:         5,
	ProjectStatusFieldCompleted:    6,
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

// serviceItemLifecycleRank 把服务项映射到 0..8 的推进等级；
// 未知或空状态按最滞后处理，避免把项目误报为已推进。
func serviceItemLifecycleRank(item ProjectStatusItem) int {
	status := strings.TrimSpace(item.Status)
	if status == ProjectStatusFieldCompleted {
		switch strings.ToUpper(strings.TrimSpace(item.ReportStatus)) {
		case "COMPILING", "REVIEWED", "ISSUED":
			return 7
		case "ARCHIVED":
			return 8
		default:
			return 6
		}
	}
	if rank, ok := serviceItemStatusRank[status]; ok {
		return rank
	}
	return 0
}

// DeriveProjectProgress 按服务项推进等级派生项目进度百分比：
// 各服务项等级均值除以最高等级，已终止服务项按 100% 计。
// 进度不再由事件手工写入固定值，与派生状态共用同一套等级表。
func DeriveProjectProgress(items []ProjectStatusItem) int {
	if len(items) == 0 {
		return 0
	}
	maxRank := len(projectStatusNodes) - 1
	total := 0
	for _, item := range items {
		if strings.TrimSpace(item.Status) == ProjectStatusTerminated {
			total += maxRank
			continue
		}
		total += serviceItemLifecycleRank(item)
	}
	return total * 100 / (maxRank * len(items))
}
