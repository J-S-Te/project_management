-- 资源事实自动化：组织工作日历、资源例外、强证据自动确认策略和用量来源。
-- 迁移只新增结构；没有日历的组织继续不推算容量，历史用量保持 MANUAL。

CREATE TABLE IF NOT EXISTS pm_metric_work_calendar (
 id VARCHAR(32) NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 organization_id VARCHAR(64) NOT NULL, weekday TINYINT NOT NULL,
 intervals JSON NOT NULL, version BIGINT UNSIGNED NOT NULL,
 note VARCHAR(1000) NOT NULL DEFAULT '',
 organization_witness_id VARCHAR(64) NOT NULL DEFAULT '',
 updated_by VARCHAR(64) NOT NULL DEFAULT '', updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_pm_metric_calendar_weekday(tenant_id,organization_id,weekday),
 KEY idx_pm_metric_calendar_org(tenant_id,organization_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS pm_metric_calendar_exception (
 id VARCHAR(32) NOT NULL PRIMARY KEY, tenant_id VARCHAR(64) NOT NULL,
 organization_id VARCHAR(64) NOT NULL,
 resource_type VARCHAR(16) NOT NULL, resource_id VARCHAR(64) NOT NULL,
 identity_id VARCHAR(64) NOT NULL DEFAULT '',
 business_date VARCHAR(10) NOT NULL, kind VARCHAR(16) NOT NULL,
 intervals JSON NOT NULL, version BIGINT UNSIGNED NOT NULL,
 note VARCHAR(1000) NOT NULL, created_by VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_pm_metric_exception_day(tenant_id,organization_id,resource_type,resource_id,business_date),
 KEY idx_pm_metric_exception_org(tenant_id,organization_id,business_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS pm_metric_auto_confirm_setting (
 tenant_id VARCHAR(64) NOT NULL PRIMARY KEY,
 enabled TINYINT(1) NOT NULL DEFAULT 0,
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 updated_by VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

SET @pm_metric_col=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND column_name='source');
SET @pm_metric_sql=IF(@pm_metric_col=0,
 'ALTER TABLE pm_resource_usage ADD COLUMN source VARCHAR(32) NOT NULL DEFAULT ''MANUAL'' AFTER corrects_id','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;

SET @pm_metric_col=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND column_name='source_ref');
SET @pm_metric_sql=IF(@pm_metric_col=0,
 'ALTER TABLE pm_resource_usage ADD COLUMN source_ref VARCHAR(128) NOT NULL DEFAULT '''' AFTER source','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;

SET @pm_metric_col=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND column_name='source_event_ids');
SET @pm_metric_sql=IF(@pm_metric_col=0,
 'ALTER TABLE pm_resource_usage ADD COLUMN source_event_ids JSON NULL AFTER source_ref','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;

SET @pm_metric_col=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND column_name='auto_confirmed');
SET @pm_metric_sql=IF(@pm_metric_col=0,
 'ALTER TABLE pm_resource_usage ADD COLUMN auto_confirmed TINYINT(1) NOT NULL DEFAULT 0 AFTER source_event_ids','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;

SET @pm_metric_col=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND column_name='source_ref_key');
SET @pm_metric_sql=IF(@pm_metric_col=0,
 'ALTER TABLE pm_resource_usage ADD COLUMN source_ref_key VARCHAR(128) GENERATED ALWAYS AS (NULLIF(source_ref,'''')) STORED','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;

SET @pm_metric_idx=(SELECT COUNT(*) FROM information_schema.statistics
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND index_name='uq_pm_usage_source_ref');
SET @pm_metric_sql=IF(@pm_metric_idx=0,
 'ALTER TABLE pm_resource_usage ADD UNIQUE INDEX uq_pm_usage_source_ref(tenant_id,source_ref_key)','SELECT 1');
PREPARE pm_metric_stmt FROM @pm_metric_sql; EXECUTE pm_metric_stmt; DEALLOCATE PREPARE pm_metric_stmt;
