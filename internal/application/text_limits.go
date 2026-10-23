package application

import (
	"fmt"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
)

// validateProjectTextLength 让页面 maxlength 与 MySQL 列宽使用同一组上限；
// 按字符数而不是字节数计算，避免中文被提前截断。
func validateProjectTextLength(value string, max int, label string) error {
	if len([]rune(strings.TrimSpace(value))) > max {
		return ValidationError(fmt.Sprintf("%s不能超过 %d 个字符", label, max))
	}
	return nil
}

func validateContractServiceTexts(items []domain.ContractService) error {
	for index, item := range items {
		prefix := fmt.Sprintf("第 %d 个服务项", index+1)
		checks := []struct {
			value string
			max   int
			label string
		}{
			{item.Site, 255, "实施场所"}, {item.Batch, 64, "实施批次"},
			{item.Category, 255, "检测类别"}, {item.System, 128, "系统名称"},
			{item.SystemLevel, 64, "系统等级"}, {item.Requirement, 2000, "技术要求"},
		}
		for _, check := range checks {
			if err := validateProjectTextLength(check.value, check.max, prefix+check.label); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePenetrationDecisionTexts(input domain.PenetrationDecisionInput) error {
	checks := []struct {
		value string
		max   int
		label string
	}{
		{input.CustomerContact, 128, "客户联系人"},
		{input.CommunicationSummary, 2000, "沟通结论"},
		{input.ChangeReason, 1000, "变更原因"},
	}
	for _, check := range checks {
		if err := validateProjectTextLength(check.value, check.max, check.label); err != nil {
			return err
		}
	}
	return nil
}

func validatePenetrationPlanTexts(input domain.PenetrationPlanInput) error {
	checks := []struct {
		value string
		max   int
		label string
	}{
		{input.AuthDocNo, 128, "授权书编号"}, {input.AuthScope, 5000, "授权范围"},
		{input.TestScope, 5000, "计划测试范围"}, {input.TestWindow, 128, "测试时间窗"},
		{input.EmergencyContact, 128, "应急联系人"}, {input.RollbackPlan, 5000, "回滚方案"},
	}
	for _, check := range checks {
		if err := validateProjectTextLength(check.value, check.max, check.label); err != nil {
			return err
		}
	}
	return nil
}

func validateImplementationPlanTexts(input domain.ImplementationPlanInput) error {
	checks := []struct {
		value string
		max   int
		label string
	}{
		{input.SitePlan, 5000, "现场实施计划"}, {input.PenetrationTestPlan, 5000, "渗透测试计划"},
		{input.AuthDocNo, 128, "授权书编号"}, {input.AuthScope, 5000, "授权范围"},
		{input.TestScope, 5000, "测试范围"}, {input.TestWindow, 128, "测试时间窗"},
		{input.EmergencyContact, 128, "应急联系人"}, {input.RollbackPlan, 5000, "回滚方案"},
	}
	for _, check := range checks {
		if err := validateProjectTextLength(check.value, check.max, check.label); err != nil {
			return err
		}
	}
	for index, resource := range input.Personnel {
		if err := validateProjectTextLength(resource.Note, 500, fmt.Sprintf("第 %d 行备注", index+1)); err != nil {
			return err
		}
	}
	return nil
}

func validateTravelTexts(input domain.TravelArrangementInput) error {
	checks := []struct {
		value string
		max   int
		label string
	}{
		{input.NoTravelReason, 500, "无需行程原因"}, {input.Origin, 255, "出发地"},
		{input.Destination, 255, "目的地"}, {input.Transport, 255, "交通方式"},
		{input.AccommodationNeed, 255, "住宿需求"},
	}
	for _, check := range checks {
		if err := validateProjectTextLength(check.value, check.max, check.label); err != nil {
			return err
		}
	}
	return nil
}
