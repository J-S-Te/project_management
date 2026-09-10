-- 000008 项目治理扩展：特殊方法技术复核、报告状态、渗透测试专项合规要素与五套真实配置表。

ALTER TABLE pm_service_item
  ADD COLUMN tech_review_status VARCHAR(32) NOT NULL DEFAULT 'NONE' AFTER conflict_status,
  ADD COLUMN tech_reviewed_at DATETIME(3) NULL AFTER tech_review_status,
  ADD COLUMN tech_reviewed_by VARCHAR(64) NOT NULL DEFAULT '' AFTER tech_reviewed_at,
  ADD COLUMN tech_review_comment VARCHAR(512) NOT NULL DEFAULT '' AFTER tech_reviewed_by,
  ADD COLUMN report_status VARCHAR(32) NOT NULL DEFAULT 'NONE' AFTER tech_review_comment,
  ADD COLUMN report_updated_at DATETIME(3) NULL AFTER report_status,
  ADD COLUMN report_updated_by VARCHAR(64) NOT NULL DEFAULT '' AFTER report_updated_at;

-- 渗透测试专项/实施计划：与服务项 1:1，保存完整合规要素（授权书、白名单范围、
-- 测试时间窗、应急联系人、回滚方案等），不再只是校验不入库的文本。
CREATE TABLE IF NOT EXISTS pm_impl_plan (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  service_item_id VARCHAR(32) NOT NULL,
  planned_start DATETIME(3) NULL,
  planned_end DATETIME(3) NULL,
  site_plan TEXT NOT NULL,
  penetration_test_plan TEXT NOT NULL DEFAULT '',
  auth_doc_no VARCHAR(128) NOT NULL DEFAULT '',
  auth_start DATETIME(3) NULL,
  auth_end DATETIME(3) NULL,
  auth_scope TEXT NOT NULL DEFAULT '',
  test_scope TEXT NOT NULL DEFAULT '',
  test_window VARCHAR(128) NOT NULL DEFAULT '',
  emergency_contact VARCHAR(128) NOT NULL DEFAULT '',
  rollback_plan TEXT NOT NULL DEFAULT '',
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_impl_plan_item (tenant_id, service_item_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ① 拆分配置（分单规则）
CREATE TABLE IF NOT EXISTS pm_split_rule (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  scope VARCHAR(255) NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_split_rule_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ② 预警规则（检查类型 / 阈值）
CREATE TABLE IF NOT EXISTS pm_warning_rule (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  check_type VARCHAR(64) NOT NULL DEFAULT '',
  threshold VARCHAR(128) NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_warning_rule_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ③ 自动化动作（触发事件 / 目标）
CREATE TABLE IF NOT EXISTS pm_automation (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  `trigger` VARCHAR(128) NOT NULL DEFAULT '',
  target VARCHAR(255) NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_automation_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ④ 字段级权限（角色 / 字段 / 访问级别）
CREATE TABLE IF NOT EXISTS pm_field_permission (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  role_code VARCHAR(64) NOT NULL DEFAULT '',
  field_name VARCHAR(128) NOT NULL DEFAULT '',
  access_level VARCHAR(32) NOT NULL DEFAULT 'view',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_field_permission_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ⑤ SLA 规则（状态 / 时限小时 / 提醒小时）
CREATE TABLE IF NOT EXISTS pm_sla (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  `status` VARCHAR(64) NOT NULL DEFAULT '',
  deadline_hours INT NOT NULL DEFAULT 0,
  remind_hours INT NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_sla_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;