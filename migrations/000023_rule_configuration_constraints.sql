-- 规则配置运行时唯一性：应用层负责友好提示，数据库约束负责封住并发写入竞态。
--
-- 只约束启用中的规则。存量重复记录不物理删除：保留最早一条启用，其余停用并保留审计历史；
-- 停用规则的生成列为 NULL，MySQL 唯一索引允许多行 NULL，因此管理员仍可查看和清理历史配置。

UPDATE pm_automation AS duplicate_rule
JOIN pm_automation AS keeper
  ON keeper.tenant_id = duplicate_rule.tenant_id
 AND keeper.kind = duplicate_rule.kind
 AND keeper.`trigger` = duplicate_rule.`trigger`
 AND keeper.target = duplicate_rule.target
 AND keeper.id < duplicate_rule.id
 AND keeper.enabled = TRUE
SET duplicate_rule.enabled = FALSE,
    duplicate_rule.updated_at = UTC_TIMESTAMP(3),
    duplicate_rule.updated_by = 'migration-000023'
WHERE duplicate_rule.enabled = TRUE;

ALTER TABLE pm_automation
  ADD COLUMN active_trigger VARCHAR(128)
    GENERATED ALWAYS AS (CASE WHEN enabled = TRUE THEN `trigger` ELSE NULL END) STORED,
  ADD COLUMN active_target VARCHAR(255)
    GENERATED ALWAYS AS (CASE WHEN enabled = TRUE THEN target ELSE NULL END) STORED,
  ADD UNIQUE KEY uq_pm_automation_active_condition
    (tenant_id, kind, active_trigger, active_target);

UPDATE pm_field_permission AS duplicate_rule
JOIN pm_field_permission AS keeper
  ON keeper.tenant_id = duplicate_rule.tenant_id
 AND keeper.kind = duplicate_rule.kind
 AND keeper.role_code = duplicate_rule.role_code
 AND keeper.field_name = duplicate_rule.field_name
 AND keeper.id < duplicate_rule.id
 AND keeper.enabled = TRUE
SET duplicate_rule.enabled = FALSE,
    duplicate_rule.updated_at = UTC_TIMESTAMP(3),
    duplicate_rule.updated_by = 'migration-000023'
WHERE duplicate_rule.enabled = TRUE;

ALTER TABLE pm_field_permission
  ADD COLUMN active_role_code VARCHAR(64)
    GENERATED ALWAYS AS (CASE WHEN enabled = TRUE THEN role_code ELSE NULL END) STORED,
  ADD COLUMN active_field_name VARCHAR(128)
    GENERATED ALWAYS AS (CASE WHEN enabled = TRUE THEN field_name ELSE NULL END) STORED,
  ADD UNIQUE KEY uq_pm_field_permission_active_condition
    (tenant_id, kind, active_role_code, active_field_name);

UPDATE pm_sla AS duplicate_rule
JOIN pm_sla AS keeper
  ON keeper.tenant_id = duplicate_rule.tenant_id
 AND keeper.kind = duplicate_rule.kind
 AND keeper.`status` = duplicate_rule.`status`
 AND keeper.id < duplicate_rule.id
 AND keeper.enabled = TRUE
SET duplicate_rule.enabled = FALSE,
    duplicate_rule.updated_at = UTC_TIMESTAMP(3),
    duplicate_rule.updated_by = 'migration-000023'
WHERE duplicate_rule.enabled = TRUE;

ALTER TABLE pm_sla
  ADD COLUMN active_status VARCHAR(64)
    GENERATED ALWAYS AS (CASE WHEN enabled = TRUE THEN `status` ELSE NULL END) STORED,
  ADD UNIQUE KEY uq_pm_sla_active_status
    (tenant_id, kind, active_status);
