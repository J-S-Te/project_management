package application

import (
	"strconv"
	"strings"

	"github.com/j-s-te/project-management/internal/domain"
)

const (
	warningCheckQualification = "资质能力冲突"
	warningCheckCapability    = "能力缺失"
	warningCheckOther         = "其他冲突"
)

var supportedWarningCheckTypes = map[string]struct{}{
	warningCheckQualification: {},
	warningCheckCapability:    {},
	warningCheckOther:         {},
}

// normalizeWarningRule 只接受告警引擎能够实际归类的检查类型和正整数阈值。
// 历史页面允许任意文本，保存成功后可能永远无法匹配冲突；服务端必须同步收口，
// 避免调用方绕过页面继续制造无效配置。
func normalizeWarningRule(input *domain.Rule) error {
	input.Name = strings.TrimSpace(input.Name)
	input.CheckType = strings.TrimSpace(input.CheckType)
	input.Threshold = strings.TrimSpace(input.Threshold)
	if input.Name == "" {
		return ValidationError("配置名称不能为空")
	}
	if _, ok := supportedWarningCheckTypes[input.CheckType]; !ok {
		return ValidationError("所选检查类型不受预警引擎支持")
	}
	threshold, err := strconv.Atoi(input.Threshold)
	if err != nil || threshold < 1 {
		return ValidationError("阈值必须是大于等于 1 的整数")
	}
	input.Threshold = strconv.Itoa(threshold)
	return nil
}
