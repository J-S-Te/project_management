-- 合同拆解规则配置 v2（对齐原型 PG-CFG-01「合同拆解规则配置」）
--
-- 旧模型把「拆解规则」做成了自由文本的规则清单（pm_split_rule.name + scope），
-- 既表达不了原型的分组维度，也无法承载检测类别域与覆盖规则，且其「存在规则且未命中
-- → 自动放行」的口径与拆解流程原型（命中→按批次+检测类别生成；未命中→标记待人工确认
-- 并通知业务管理员）相反。本次改为三块结构化配置：
--   ① 默认分组规则（每租户一行）
--   ② 检测类别（服务类型）域（主数据，含默认体系要求 / 必备资质 / 是否特殊方法）
--   ③ 覆盖规则（按客户 / 合同 / 服务项数匹配，按优先级覆盖默认规则）
-- 存量 pm_split_rule 行按确认结论直接删除（新模型下无对应语义，静默迁移会改变线上行为）；
-- 表本身保留一个版本以便回滚，不再被读写。

-- ① 默认分组规则：一个租户一行。
CREATE TABLE IF NOT EXISTS pm_split_policy (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  -- 分组维度 1：batch 批次 / site 场所 / customer 客户 / contract 合同
  dimension_primary VARCHAR(32) NOT NULL DEFAULT 'batch',
  -- 分组维度 2：category 检测类别 / system_standard 体系要求 / test_mode 方法类型
  dimension_secondary VARCHAR(32) NOT NULL DEFAULT 'category',
  -- 默认进入状态：待确认（业务管理员确认后→待分配）/ 待分配（跳过确认）
  default_status VARCHAR(32) NOT NULL DEFAULT '待确认',
  -- 是否生成「技术要求摘要」：合同条款抽取 / 留空人工填写
  generate_requirement_summary TINYINT(1) NOT NULL DEFAULT 1,
  -- 技术要求摘要字段：生成后默认可编辑 / 锁定（仅技术总监可改）
  requirement_summary_locked TINYINT(1) NOT NULL DEFAULT 0,
  -- 分组规则缺失时：HUMAN_CONFIRM 标记待人工确认并通知业务管理员 /
  --                DEFAULT_RULE 按默认规则生成 / SILENT 按默认规则生成但不通知
  missing_rule_action VARCHAR(32) NOT NULL DEFAULT 'HUMAN_CONFIRM',
  -- 范围变更检测：开启后确认拆解时勾对合同清单与拆解结果
  scope_change_detection TINYINT(1) NOT NULL DEFAULT 1,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_pm_split_policy_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ② 检测类别（服务类型）域。
CREATE TABLE IF NOT EXISTS pm_detection_category (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  category VARCHAR(128) NOT NULL,
  -- 默认体系要求：拆解时若服务项未显式给出体系要求，则取该默认值
  system_standard VARCHAR(128) NOT NULL DEFAULT '',
  -- 必备资质（默认）：自由文本，供页面展示与分配环节提示
  required_qualifications VARCHAR(255) NOT NULL DEFAULT '',
  -- 是否特殊方法：NO 否 / MARKABLE 可标记 / REQUIRED 必为特殊方法
  special_method VARCHAR(16) NOT NULL DEFAULT 'NO',
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_pm_detection_category (tenant_id, category)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ③ 覆盖规则（按客户 / 合同类型 / 服务项数）。
CREATE TABLE IF NOT EXISTS pm_split_override (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  -- 匹配条件（JSON）：customer_contains / contract_contains / max_service_items /
  --                    categories / min_service_items 等本项目可判定的字段
  match_conditions JSON NOT NULL,
  -- 覆盖设置（JSON）：dimension_primary / dimension_secondary / default_status /
  --                  generate_requirement_summary / requirement_summary_locked /
  --                  missing_rule_action / scope_change_detection 的任意子集
  override_settings JSON NOT NULL,
  priority INT NOT NULL DEFAULT 100,
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_split_override_tenant (tenant_id, enabled, priority)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 服务项的「体系要求」：原型的检测类别域带默认体系要求，服务项需要落库一个字段承载它。
-- 与既有的 system_level（系统等级）是两个不同口径：体系要求=等保 2.0 / ISO 9001 / ISO 27001。
ALTER TABLE pm_service_item
  ADD COLUMN system_standard VARCHAR(128) NOT NULL DEFAULT '' AFTER system_level;

-- 存量自由文本拆解规则按确认结论删除（表保留以便回滚，不再被读写）。
DELETE FROM pm_split_rule WHERE kind = 'split-rules';
