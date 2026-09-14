-- 资质 / 能力编码目录：租户在系统配置中自主维护人员资质和设备能力编码。
--
-- 目录复用现有 /rules CRUD 通道，但使用独立表，避免与预警规则等运行参数混存：
--   scope      = 规范化后的编码（例如 CISP / SCANNER-L3）
--   check_type = PERSON（人员资质）/ EQUIPMENT（设备能力）
-- 禁用或删除目录项不会级联修改 pm_capability 中的历史快照。
CREATE TABLE IF NOT EXISTS pm_capability_code (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL DEFAULT 'capability-codes',
  name VARCHAR(255) NOT NULL,
  scope VARCHAR(128) NOT NULL,
  check_type VARCHAR(16) NOT NULL,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_pm_capability_code_tenant_type_code (tenant_id, check_type, scope),
  KEY idx_pm_capability_code_tenant_enabled (tenant_id, check_type, enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 把现有人员/设备档案中的 JSON 编码展开后回填为已启用目录项。
-- 这一步避免首次手工配置时，未被手工重录的存量编码立即变成不可编辑的“目录外编码”。
-- MySQL 8.4 支持 JSON_TABLE；空数组自然不产生记录。
INSERT INTO pm_capability_code
  (tenant_id, kind, name, scope, check_type, enabled, created_at, updated_at, updated_by)
SELECT DISTINCT
  capability.tenant_id,
  'capability-codes',
  UPPER(TRIM(code_row.code)),
  UPPER(TRIM(code_row.code)),
  UPPER(TRIM(capability.resource_type)),
  TRUE,
  NOW(3),
  NOW(3),
  'migration-000021'
FROM pm_capability AS capability
JOIN JSON_TABLE(
  capability.capability_codes,
  '$[*]' COLUMNS(code VARCHAR(512) PATH '$')
) AS code_row
WHERE UPPER(TRIM(capability.resource_type)) IN ('PERSON', 'EQUIPMENT')
  AND TRIM(code_row.code) <> ''
  AND CHAR_LENGTH(TRIM(code_row.code)) <= 128
ON DUPLICATE KEY UPDATE scope = VALUES(scope);

-- 服务项保存的是项目拆解当时的必检资质快照，也必须纳入目录回填。
-- 这样即使对应能力档案尚未建立，存量项目要求仍会出现在系统配置中，
-- 管理员不能误以为是“无引用编码”并删除。
INSERT INTO pm_capability_code
  (tenant_id, kind, name, scope, check_type, enabled, created_at, updated_at, updated_by)
SELECT DISTINCT
  service_item.tenant_id,
  'capability-codes',
  UPPER(TRIM(code_row.code)),
  UPPER(TRIM(code_row.code)),
  'PERSON',
  TRUE,
  NOW(3),
  NOW(3),
  'migration-000021'
FROM pm_service_item AS service_item
JOIN JSON_TABLE(
  COALESCE(service_item.required_codes, JSON_ARRAY()),
  '$[*]' COLUMNS(code VARCHAR(512) PATH '$')
) AS code_row
WHERE TRIM(code_row.code) <> ''
  AND CHAR_LENGTH(TRIM(code_row.code)) <= 128
ON DUPLICATE KEY UPDATE scope = VALUES(scope);
