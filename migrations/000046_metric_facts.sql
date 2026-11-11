-- 历史报告保持旧流程及首次审核unknown；只有升级后新版本要求真实审核轮次。
SET @pm_metric_column_exists = (SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_report_revision' AND column_name='review_required');
SET @pm_metric_sql = IF(@pm_metric_column_exists=0,
 'ALTER TABLE pm_report_revision ADD COLUMN review_required TINYINT(1) NOT NULL DEFAULT 0', 'SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql;
EXECUTE pm_metric_stmt;
DEALLOCATE PREPARE pm_metric_stmt;

CREATE TABLE IF NOT EXISTS pm_report_review_round (
 id VARCHAR(32) NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 project_id VARCHAR(32) NOT NULL, service_item_id VARCHAR(32) NOT NULL,
 subject_type VARCHAR(32) NOT NULL, subject_id VARCHAR(32) NOT NULL,
 revision BIGINT UNSIGNED NOT NULL, round_no BIGINT UNSIGNED NOT NULL,
 source_version BIGINT UNSIGNED NOT NULL, organization_id VARCHAR(64) NOT NULL DEFAULT '',
 file_id VARCHAR(64) NOT NULL, file_sha256 VARCHAR(64) NOT NULL,
 identity_id VARCHAR(64) NOT NULL DEFAULT '', prepared_by VARCHAR(64) NOT NULL,
 submitted_by VARCHAR(64) NOT NULL, submitted_at DATETIME(3) NOT NULL,
 decision VARCHAR(16) NOT NULL, decided_by VARCHAR(64) NOT NULL DEFAULT '',
 decided_at DATETIME(3) NULL, reason_code VARCHAR(32) NOT NULL DEFAULT '',
 note VARCHAR(1000) NOT NULL DEFAULT '',
 UNIQUE KEY uq_metric_review_round(tenant_id,subject_type,subject_id,revision,round_no),
 KEY idx_metric_review_item(tenant_id,service_item_id,submitted_at),
 KEY idx_metric_review_decision(tenant_id,decision,decided_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS pm_resource_capacity (
 id VARCHAR(32) NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 resource_type VARCHAR(16) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 identity_id VARCHAR(64) NOT NULL DEFAULT '', business_date VARCHAR(10) NOT NULL,
 organization_id VARCHAR(64) NOT NULL, organization_witness_id VARCHAR(64) NOT NULL,
 intervals JSON NOT NULL, minutes BIGINT NOT NULL, version BIGINT UNSIGNED NOT NULL,
 status VARCHAR(16) NOT NULL, note VARCHAR(1000) NOT NULL, published_by VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_metric_capacity_version(tenant_id,resource_type,resource_id,business_date,version),
 KEY idx_metric_capacity_current(tenant_id,resource_type,resource_id,business_date,status),
 KEY idx_metric_capacity_org(tenant_id,organization_id,business_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS pm_resource_usage (
 id VARCHAR(32) NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 resource_type VARCHAR(16) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 identity_id VARCHAR(64) NOT NULL DEFAULT '', project_id VARCHAR(32) NOT NULL,
 service_item_id VARCHAR(32) NOT NULL, business_date VARCHAR(10) NOT NULL,
 organization_id VARCHAR(64) NOT NULL, capacity_id VARCHAR(32) NOT NULL,
 started_at DATETIME(3) NOT NULL, ended_at DATETIME(3) NOT NULL, minutes BIGINT NOT NULL,
 status VARCHAR(16) NOT NULL, version BIGINT UNSIGNED NOT NULL,
 note VARCHAR(1000) NOT NULL, evidence VARCHAR(1000) NOT NULL, corrects_id VARCHAR(32) NOT NULL DEFAULT '',
 created_by VARCHAR(64) NOT NULL, submitted_by VARCHAR(64) NOT NULL DEFAULT '',
 decided_by VARCHAR(64) NOT NULL DEFAULT '', decision_note VARCHAR(1000) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 KEY idx_metric_usage_overlap(tenant_id,resource_type,resource_id,status,started_at,ended_at),
 KEY idx_metric_usage_capacity(tenant_id,capacity_id,status),
 KEY idx_metric_usage_project(tenant_id,project_id,business_date),
 KEY idx_metric_usage_identity(tenant_id,identity_id,business_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS pm_metric_operation (
 tenant_id VARCHAR(64) NOT NULL, operation_key VARCHAR(128) NOT NULL,
 fingerprint CHAR(64) NOT NULL, result_json JSON NOT NULL, created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(tenant_id,operation_key), KEY idx_metric_operation_time(tenant_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
