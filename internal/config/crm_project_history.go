package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type CRMProjectHistoryConfig struct {
	Enabled                                            bool
	Issuer, Audience, PublicKeyPath, CallerEnvironment string
}

// loadCRMProjectHistory 加载独立 CRM 项目历史机器验签配置。
// 参数：无，读取部署设置和项目租户。
// 返回值：启用时配置不完整则报错。
func (c *Config) loadCRMProjectHistory() error {
	enabled, err := strconv.ParseBool(env("CRM_PROJECT_HISTORY_MACHINE_ENABLED", "false"))
	if err != nil {
		return fmt.Errorf("CRM_PROJECT_HISTORY_MACHINE_ENABLED: %w", err)
	}
	c.CRMProjectHistory.Enabled = enabled
	if !enabled {
		return nil
	}
	c.CRMProjectHistory = CRMProjectHistoryConfig{
		Enabled:           true,
		Issuer:            strings.TrimSpace(os.Getenv("CRM_PROJECT_HISTORY_MACHINE_ISSUER")),
		Audience:          strings.TrimSpace(os.Getenv("CRM_PROJECT_HISTORY_MACHINE_AUDIENCE")),
		PublicKeyPath:     strings.TrimSpace(os.Getenv("CRM_PROJECT_HISTORY_MACHINE_PUBLIC_KEY_PATH")),
		CallerEnvironment: strings.TrimSpace(os.Getenv("CRM_PROJECT_HISTORY_MACHINE_CALLER_ENVIRONMENT_CODE")),
	}
	for key, value := range map[string]string{
		"CRM_PROJECT_HISTORY_MACHINE_ISSUER":                  c.CRMProjectHistory.Issuer,
		"CRM_PROJECT_HISTORY_MACHINE_AUDIENCE":                c.CRMProjectHistory.Audience,
		"CRM_PROJECT_HISTORY_MACHINE_PUBLIC_KEY_PATH":         c.CRMProjectHistory.PublicKeyPath,
		"CRM_PROJECT_HISTORY_MACHINE_CALLER_ENVIRONMENT_CODE": c.CRMProjectHistory.CallerEnvironment,
		"OIDC_TENANT_ID": c.OIDCTenantID,
	} {
		if value == "" {
			return fmt.Errorf("%s is required for CRM project history", key)
		}
	}
	return nil
}
