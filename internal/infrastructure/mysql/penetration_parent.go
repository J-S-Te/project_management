package mysql

import "github.com/j-s-te/project-management/internal/application"

// validatePenetrationParentMutation 防止专项命令使父项已完成的交付门禁失效。
// 参数：item为事务内锁定父项，eventType为待执行的专项命令类型。
// 返回值：父项归档、终止或现场完成后改变结论时返回前置条件错误。
func validatePenetrationParentMutation(item *serviceItemRecord, eventType string) error {
	if item.ArchivedAt.Valid || item.Status == "已终止" || item.ReportStatus == "ARCHIVED" {
		return application.PreconditionError("父服务项已归档或终止，不能修改专项；请先通过合法更正或返工流程处理")
	}
	if eventType == "DECISION_RECORDED" && item.Status == "现场实施完成" {
		return application.PreconditionError("父服务项现场已完成，变更专项结论前请先申请报告返工")
	}
	return nil
}
