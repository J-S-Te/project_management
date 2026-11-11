-- 同一平台人员的多个能力档案共享互斥身份；原resource_id证据保持不变。
CREATE TABLE IF NOT EXISTS pm_metric_resource_mutex (
 tenant_id VARCHAR(64) NOT NULL,
 resource_type VARCHAR(16) NOT NULL,
 resource_key VARCHAR(64) NOT NULL,
 PRIMARY KEY(tenant_id,resource_type,resource_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- 不猜合并既有重复容量；若存在重复有效人员日容量，唯一索引创建失败并关闭升级。
SET @pm_identity_column_exists=(SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema=DATABASE() AND table_name='pm_resource_capacity' AND column_name='published_person_identity');
SET @pm_identity_sql=IF(@pm_identity_column_exists=0,
 'ALTER TABLE pm_resource_capacity ADD COLUMN published_person_identity VARCHAR(64) GENERATED ALWAYS AS (CASE WHEN resource_type=''PERSON'' AND status=''PUBLISHED'' THEN identity_id ELSE NULL END) STORED', 'SELECT 1');
PREPARE pm_identity_stmt FROM @pm_identity_sql;
EXECUTE pm_identity_stmt;
DEALLOCATE PREPARE pm_identity_stmt;

SET @pm_identity_index_exists=(SELECT COUNT(*) FROM information_schema.statistics
 WHERE table_schema=DATABASE() AND table_name='pm_resource_capacity' AND index_name='uq_pm_capacity_person_day');
SET @pm_identity_sql=IF(@pm_identity_index_exists=0,
 'ALTER TABLE pm_resource_capacity ADD UNIQUE INDEX uq_pm_capacity_person_day(tenant_id,published_person_identity,business_date)', 'SELECT 1');
PREPARE pm_identity_stmt FROM @pm_identity_sql;
EXECUTE pm_identity_stmt;
DEALLOCATE PREPARE pm_identity_stmt;

SET @pm_identity_index_exists=(SELECT COUNT(*) FROM information_schema.statistics
 WHERE table_schema=DATABASE() AND table_name='pm_resource_usage' AND index_name='idx_pm_usage_identity_overlap');
SET @pm_identity_sql=IF(@pm_identity_index_exists=0,
 'ALTER TABLE pm_resource_usage ADD INDEX idx_pm_usage_identity_overlap(tenant_id,resource_type,identity_id,status,started_at,ended_at)', 'SELECT 1');
PREPARE pm_identity_stmt FROM @pm_identity_sql;
EXECUTE pm_identity_stmt;
DEALLOCATE PREPARE pm_identity_stmt;
