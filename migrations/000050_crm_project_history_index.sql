-- CRM 按签名租户和客户主键查询合同来源项目，按项目 ID 稳定分页。
DROP PROCEDURE IF EXISTS pm_ensure_crm_customer_project_index;

-- pm_ensure_crm_customer_project_index 增量补建客户项目索引，可在 DDL 完成但账本未写入时重跑。
-- 参数：无。
-- 返回值：无；同名索引定义不匹配时返回错误。
CREATE PROCEDURE pm_ensure_crm_customer_project_index()
BEGIN
  DECLARE index_columns TEXT DEFAULT NULL;
  DECLARE index_count INT DEFAULT 0;

  SELECT COUNT(*), COALESCE(GROUP_CONCAT(LOWER(COLUMN_NAME) ORDER BY SEQ_IN_INDEX), '')
    INTO index_count, index_columns
    FROM information_schema.STATISTICS
   WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'pm_project'
     AND INDEX_NAME = 'idx_pm_project_crm_customer';

  IF index_count = 0 THEN
    ALTER TABLE pm_project ADD INDEX idx_pm_project_crm_customer (tenant_id, customer_id, source_kind, id);
  ELSEIF index_columns <> 'tenant_id,customer_id,source_kind,id' THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'idx_pm_project_crm_customer definition mismatch';
  END IF;
END;

CALL pm_ensure_crm_customer_project_index();
DROP PROCEDURE pm_ensure_crm_customer_project_index;
