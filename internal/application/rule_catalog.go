package application

import "github.com/j-s-te/project-management/internal/domain"

// RuleOption 是规则编辑器使用的服务端权威选项。Value 是持久化值，Label/Description
// 只用于业务展示；浏览器不得自行发明事件码或状态值。
type RuleOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

var automationTriggerCatalog = []RuleOption{
	{EventDecompositionAdjusted, "拆解调整已提交", "服务项拆解清单发生调整"},
	{EventDecompositionReturned, "已退回拆解确认", "任务分配阶段退回重新拆解"},
	{EventScopeChangeDetected, "检测到合同范围变化", "拆解结果与合同清单不一致并进入补充协议处理"},
	{EventTeamAssigned, "团队负责人已分配", "业务管理员完成团队负责人指派"},
	{EventTeamAssignmentRevoked, "团队负责人分配已撤销", "待分配阶段撤销负责人"},
	{EventExecutionTeamAssigned, "执行人员已分配", "团队负责人完成项目经理和工程师指派"},
	{EventExecutionAssignmentRevoked, "执行人员分配已撤销", "撤销项目经理或工程师指派"},
	{EventImplementationPlanned, "实施计划已发布", "实施计划通过前置校验并发布"},
	{EventImplementationPlanRevoked, "实施计划已撤销", "项目退回计划制定阶段"},
	{EventPreparationStarted, "实施准备已发起", "设备和准备材料已登记"},
	{EventPreparationRevoked, "实施准备已撤销", "项目退回实施准备阶段"},
	{EventFieldStarted, "现场测评已开始", "项目经理确认从实施准备进入现场实施"},
	{EventFieldRecordSubmitted, "现场记录已提交", "现场原始记录和证据已提交"},
	{EventDeviationReported, "现场偏离已上报", "现场实施出现偏离或异常"},
	{EventDeviationReviewed, "现场偏离已评审", "偏离处置完成评审"},
	{EventFieldCompleted, "现场实施已完成", "服务项现场实施确认完成"},
	{EventSpecialMethodReviewed, "特殊方法已复核", "技术总监完成特殊方法复核"},
	{EventReportStatusUpdated, "报告状态已更新", "报告编制、审核、签发或归档状态推进"},
	{EventRollbackRequested, "流程回退已申请", "现场或报告阶段发起回退申请"},
	{EventRollbackApproved, "流程回退已批准", "回退申请审批通过"},
	{EventRollbackRejected, "流程回退已驳回", "回退申请审批驳回"},
	{EventRollbackWithdrawn, "流程回退申请已撤回", "申请人撤回尚未完成的回退申请"},
	{EventReportCorrectionRequested, "报告更正已申请", "已发布报告发起更正申请"},
	{EventReportCorrectionApproved, "报告更正已批准", "报告更正申请审批通过"},
	{EventReportCorrectionRejected, "报告更正已驳回", "报告更正申请审批驳回"},
	{EventEquipmentReturned, "设备已归还", "实施设备解除占用"},
}

var slaStatusCatalog = []RuleOption{
	{"待确认", "待确认", "等待业务管理员确认拆解结果"},
	{"待复核", "待复核", "等待特殊方法或拆解复核"},
	{domain.ProjectStatusPendingAllocation, domain.ProjectStatusPendingAllocation, "等待团队负责人分配"},
	{domain.ProjectStatusPendingExecution, domain.ProjectStatusPendingExecution, "等待实施计划发布"},
	{domain.ProjectStatusPreparing, domain.ProjectStatusPreparing, "实施资源和材料准备中"},
	{domain.ProjectStatusInProgress, domain.ProjectStatusInProgress, "现场实施进行中"},
	{domain.ProjectStatusException, domain.ProjectStatusException, "现场异常等待处置"},
	{domain.ProjectStatusFieldCompleted, domain.ProjectStatusFieldCompleted, "现场完成后等待报告流程闭环"},
}

func copyRuleOptions(source []RuleOption) []RuleOption {
	result := make([]RuleOption, len(source))
	copy(result, source)
	return result
}

func RuleConfigurationCatalog() (automationTriggers, slaStatuses []RuleOption) {
	return copyRuleOptions(automationTriggerCatalog), copyRuleOptions(slaStatusCatalog)
}

func ruleOptionExists(options []RuleOption, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}
