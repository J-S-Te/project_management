package domain

import "strings"

// 项目状态节点：一个项目在任一时刻必须恰好落在其中一个节点上。
// 顺序即交付推进顺序，也是「取最滞后服务项」汇总规则的依据；
// 前端与服务端都必须只使用这些节点，不得各自拼装状态集合。
const (
	ProjectStatusPendingDecomposition = "待拆解确认"
	ProjectStatusPendingAllocation    = "待分配"
	ProjectStatusPendingPlan          = "待制定计划"
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
var projectStatusNodes = []string{
	ProjectStatusPendingDecomposition,
	ProjectStatusPendingAllocation,
	ProjectStatusPendingPlan,
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
	"待确认":                          0,
	"待复核":                          0,
	ProjectStatusPendingAllocation: 1,
	ProjectStatusPendingPlan:       2,
	ProjectStatusPendingExecution:  3,
	ProjectStatusPreparing:         4,
	ProjectStatusInProgress:        5,
	ProjectStatusException:         6,
	ProjectStatusFieldCompleted:    7,
}

// ProjectStatusNodes 返回线性推进节点的副本，供校验与展示使用。
func ProjectStatusNodes() []string {
	nodes := make([]string, len(projectStatusNodes))
	copy(nodes, projectStatusNodes)
	return nodes
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

// serviceItemLifecycleRank 把服务项映射到 0..9 的推进等级；
// 未知或空状态按最滞后处理，避免把项目误报为已推进。
func serviceItemLifecycleRank(item ProjectStatusItem) int {
	status := strings.TrimSpace(item.Status)
	if status == ProjectStatusFieldCompleted {
		switch strings.ToUpper(strings.TrimSpace(item.ReportStatus)) {
		case "COMPILING", "REVIEWED", "ISSUED":
			return 8
		case "ARCHIVED":
			return 9
		default:
			return 7
		}
	}
	if rank, ok := serviceItemStatusRank[status]; ok {
		return rank
	}
	return 0
}
