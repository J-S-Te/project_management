-- 标准方法更新评估（standards）：技术总监/质量经理维护检测标准方法的变更登记与影响评估清单。
-- 使用与治理类配置一致的字段形状，复用现有规则 CRUD 通道（ListRules/CreateRule/UpdateRule/SetRuleEnabled）。
CREATE TABLE IF NOT EXISTS pm_standard (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  scope VARCHAR(255) NOT NULL DEFAULT '',
  standard_code VARCHAR(128) NOT NULL DEFAULT '',
  description VARCHAR(512) NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_standard_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;