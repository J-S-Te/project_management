-- 仅新增费用参与资格及报销支出可靠投影，不修改历史业务数据。
-- 所有来源标识使用二进制排序，避免大小写折叠导致幂等键或身份碰撞。
CREATE TABLE IF NOT EXISTS pm_expense_participant (
 tenant_id VARCHAR(128) NOT NULL, project_id VARCHAR(128) NOT NULL,
 subject_id VARCHAR(128) NOT NULL, status VARCHAR(16) NOT NULL,
 valid_from DATETIME(3) NOT NULL, valid_until DATETIME(3) NOT NULL,
 granted_by VARCHAR(128) NOT NULL, reason VARCHAR(1000) NOT NULL, version BIGINT UNSIGNED NOT NULL,
 PRIMARY KEY(tenant_id,project_id,subject_id), KEY idx_expense_participant_subject(tenant_id,subject_id,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_cursor (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, claim_id VARCHAR(128) NOT NULL,
 projection_revision BIGINT UNSIGNED NOT NULL DEFAULT 0, snapshot_hash VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(3) NULL, PRIMARY KEY(tenant_id,source_environment,claim_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_inbox (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, event_id VARCHAR(128) NOT NULL,
 claim_id VARCHAR(128) NOT NULL, event_hash CHAR(64) NOT NULL, receipt LONGTEXT NOT NULL,
 received_at DATETIME(3) NOT NULL, PRIMARY KEY(tenant_id,source_environment,event_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_snapshot (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, claim_id VARCHAR(128) NOT NULL,
 projection_revision BIGINT UNSIGNED NOT NULL, snapshot_hash CHAR(64) NOT NULL, payload LONGTEXT NOT NULL,
 PRIMARY KEY(tenant_id,source_environment,claim_id,projection_revision)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_effect (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, effect_id VARCHAR(128) NOT NULL,
 claim_id VARCHAR(128) NOT NULL, payment_record_id VARCHAR(128) NOT NULL, kind VARCHAR(32) NOT NULL,
 effect_hash CHAR(64) NOT NULL, payload LONGTEXT NOT NULL,
 PRIMARY KEY(tenant_id,source_environment,effect_id),
 UNIQUE KEY expense_effect_business(tenant_id,source_environment,payment_record_id,kind),
 KEY idx_expense_effect_claim(tenant_id,source_environment,claim_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_allocation (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, effect_id VARCHAR(128) NOT NULL,
 project_id VARCHAR(128) NOT NULL, service_item_id VARCHAR(128) NOT NULL DEFAULT '', fee_type VARCHAR(64) NOT NULL,
 amount_minor BIGINT NOT NULL,
 PRIMARY KEY(tenant_id,source_environment,effect_id,project_id,service_item_id,fee_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_projection (
 tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL, claim_id VARCHAR(128) NOT NULL,
 project_id VARCHAR(128) NOT NULL, service_item_id VARCHAR(128) NOT NULL DEFAULT '', fee_type VARCHAR(64) NOT NULL,
 paid_net_minor BIGINT NOT NULL,
 PRIMARY KEY(tenant_id,source_environment,claim_id,project_id,service_item_id,fee_type),
 KEY idx_expense_projection_project(tenant_id,project_id,claim_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_repair (
 id VARCHAR(128) NOT NULL, tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL,
 claim_id VARCHAR(128) NOT NULL, actor VARCHAR(128) NOT NULL, reason VARCHAR(1000) NOT NULL,
 created_at DATETIME(3) NOT NULL, PRIMARY KEY(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pm_expense_conflict (
 id VARCHAR(128) NOT NULL, tenant_id VARCHAR(128) NOT NULL, source_environment VARCHAR(64) NOT NULL,
 claim_id VARCHAR(128) NOT NULL, event_id VARCHAR(128) NOT NULL, event_hash CHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL, PRIMARY KEY(id), KEY idx_expense_conflict_source(tenant_id,source_environment,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
