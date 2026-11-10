package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type ReimbursementIntegration struct {
	Enabled               bool
	ReferenceClientID     string
	SpendClientID         string
	Audience              string
	Issuer                string
	PublicKeyPath         string
	CallerEnvironmentCode string
}

// LoadReimbursementIntegration 读取专用报销机器信任配置并拒绝缺省或混用身份。
// 参数：无；只读取本进程明确下发的配置变量。
// 返回值：已校验配置；启用时任一信任绑定缺失或两个用途复用客户端返回错误。
func LoadReimbursementIntegration() (ReimbursementIntegration, error) {
	c := ReimbursementIntegration{}
	raw := strings.TrimSpace(os.Getenv("REIMBURSEMENT_INTEGRATION_ENABLED"))
	if raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return c, fmt.Errorf("REIMBURSEMENT_INTEGRATION_ENABLED must be boolean")
		}
		c.Enabled = value
	}
	c.ReferenceClientID = strings.TrimSpace(os.Getenv("REIMBURSEMENT_REFERENCE_CLIENT_ID"))
	c.SpendClientID = strings.TrimSpace(os.Getenv("REIMBURSEMENT_SPEND_CLIENT_ID"))
	c.Audience = strings.TrimSpace(os.Getenv("REIMBURSEMENT_MACHINE_AUDIENCE"))
	c.Issuer = strings.TrimSpace(os.Getenv("REIMBURSEMENT_MACHINE_ISSUER"))
	c.PublicKeyPath = strings.TrimSpace(os.Getenv("REIMBURSEMENT_MACHINE_PUBLIC_KEY_PATH"))
	c.CallerEnvironmentCode = strings.TrimSpace(os.Getenv("REIMBURSEMENT_MACHINE_CALLER_ENVIRONMENT_CODE"))
	if !c.Enabled {
		return c, nil
	}
	for name, value := range map[string]string{"REFERENCE_CLIENT_ID": c.ReferenceClientID, "SPEND_CLIENT_ID": c.SpendClientID, "MACHINE_AUDIENCE": c.Audience, "MACHINE_ISSUER": c.Issuer, "MACHINE_PUBLIC_KEY_PATH": c.PublicKeyPath, "MACHINE_CALLER_ENVIRONMENT_CODE": c.CallerEnvironmentCode} {
		if value == "" || placeholder(value) {
			return c, fmt.Errorf("REIMBURSEMENT_%s is required", name)
		}
	}
	if c.ReferenceClientID == c.SpendClientID {
		return c, fmt.Errorf("reimbursement reference and spend clients must be distinct")
	}
	return c, nil
}
