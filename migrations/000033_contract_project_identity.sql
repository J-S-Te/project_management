-- A04：每个合同只能建一个项目。建项身份从「合同 ID + 普通行版本」收敛为
-- 「合同 ID（历史空 ID 回退合同号）」；contract_version 继续保留为展示/审计字段，
-- 普通状态流转递增 version 不再释放重复建项。
--
-- 只读预检（先在目标库执行，确认没有同合同多项目；本迁移也会再检查一次）：
--   SELECT tenant_id,
--          CASE WHEN contract_id <> '' THEN contract_id ELSE contract END AS contract_identity_key,
--          COUNT(*) AS project_count,
--          GROUP_CONCAT(id ORDER BY created_at, id) AS project_ids
--     FROM pm_project
--    GROUP BY tenant_id, contract_identity_key
--   HAVING COUNT(*) > 1;
--   -- 历史行可能把合同编号写进 contract_id、新行写稳定 ID；再按权威合同号（合同系统内
--   -- 租户唯一）检查一次，覆盖跨存放形式的同合同多项目。
--   SELECT tenant_id, contract, COUNT(*) AS project_count,
--          GROUP_CONCAT(id ORDER BY created_at, id) AS project_ids
--     FROM pm_project
--    WHERE contract <> ''
--    GROUP BY tenant_id, contract
--   HAVING COUNT(*) > 1;
--
-- fail-closed：若预检有结果，本迁移以明确错误中止，不删除、不合并、不改写任何历史项目。
-- 处理方案必须由业务确认后单独执行，本迁移不替业务做选择。

DROP PROCEDURE IF EXISTS pm_guard_contract_project_identity;
CREATE PROCEDURE pm_guard_contract_project_identity()
BEGIN
  DECLARE duplicate_groups INT DEFAULT 0;
  DECLARE duplicate_sample TEXT DEFAULT '';
  SELECT COUNT(*) INTO duplicate_groups
    FROM (
      SELECT tenant_id, contract_identity_key AS identity_key
        FROM pm_project
       GROUP BY tenant_id, contract_identity_key
      HAVING COUNT(*) > 1
      UNION ALL
      SELECT tenant_id, contract AS identity_key
        FROM pm_project
       WHERE contract <> ''
       GROUP BY tenant_id, contract
      HAVING COUNT(*) > 1
    ) AS duplicated_groups;
  IF duplicate_groups > 0 THEN
    SELECT GROUP_CONCAT(
             CONCAT(tenant_id, '/', identity_key, '=', project_count)
             ORDER BY tenant_id, identity_key
             SEPARATOR '; '
           )
      INTO duplicate_sample
      FROM (
        SELECT tenant_id, contract_identity_key AS identity_key, COUNT(*) AS project_count
          FROM pm_project
         GROUP BY tenant_id, contract_identity_key
        HAVING COUNT(*) > 1
        UNION ALL
        SELECT tenant_id, contract AS identity_key, COUNT(*) AS project_count
          FROM pm_project
         WHERE contract <> ''
         GROUP BY tenant_id, contract
        HAVING COUNT(*) > 1
      ) AS duplicated_rows;
    SET duplicate_sample = CONCAT(
      'A04 aborted: ', duplicate_groups,
      ' contract identity duplicate(s): ', LEFT(duplicate_sample, 24),
      '; resolve manually, no data changed');
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = duplicate_sample;
  END IF;
END;
CALL pm_guard_contract_project_identity();
DROP PROCEDURE pm_guard_contract_project_identity;

-- 000033 尚未发布。DDL 隐式提交后、迁移记账前中断时，再次执行必须保留上面的
-- 重复预检，并校验索引定义；不因同名索引存在就默认它正确。先加新键再移除旧键，
-- 两步之间中断也可安全恢复，不修改历史行或已发布的 000024。
DROP PROCEDURE IF EXISTS pm_ensure_contract_project_identity;
CREATE PROCEDURE pm_ensure_contract_project_identity()
BEGIN
  DECLARE identity_index_count INT DEFAULT 0;
  DECLARE identity_index_columns TEXT DEFAULT '';
  DECLARE identity_index_non_unique INT DEFAULT 0;
  DECLARE identity_index_prefixes INT DEFAULT 0;
  DECLARE version_index_count INT DEFAULT 0;
  DECLARE version_index_columns TEXT DEFAULT '';
  DECLARE version_index_non_unique INT DEFAULT 0;
  DECLARE version_index_prefixes INT DEFAULT 0;

  SELECT COUNT(*), COALESCE(GROUP_CONCAT(column_name ORDER BY seq_in_index), ''),
         COALESCE(MAX(non_unique), 0), COUNT(sub_part)
    INTO identity_index_count, identity_index_columns, identity_index_non_unique, identity_index_prefixes
    FROM information_schema.statistics
   WHERE table_schema = DATABASE() AND table_name = 'pm_project'
     AND index_name = 'uq_pm_project_contract_identity';
  SELECT COUNT(*), COALESCE(GROUP_CONCAT(column_name ORDER BY seq_in_index), ''),
         COALESCE(MAX(non_unique), 0), COUNT(sub_part)
    INTO version_index_count, version_index_columns, version_index_non_unique, version_index_prefixes
    FROM information_schema.statistics
   WHERE table_schema = DATABASE() AND table_name = 'pm_project'
     AND index_name = 'uq_pm_project_contract_version';

  IF identity_index_count > 0 AND
     (identity_index_count <> 2 OR identity_index_columns <> 'tenant_id,contract_identity_key'
      OR identity_index_non_unique <> 0 OR identity_index_prefixes <> 0) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A04 aborted: contract identity index definition mismatch';
  END IF;
  IF version_index_count > 0 AND
     (version_index_count <> 3 OR version_index_columns <> 'tenant_id,contract_identity_key,contract_version'
      OR version_index_non_unique <> 0 OR version_index_prefixes <> 0) THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'A04 aborted: contract version index definition mismatch';
  END IF;

  IF identity_index_count = 0 THEN
    ALTER TABLE pm_project
      ADD UNIQUE KEY uq_pm_project_contract_identity (tenant_id, contract_identity_key);
  END IF;
  IF version_index_count > 0 THEN
    ALTER TABLE pm_project DROP INDEX uq_pm_project_contract_version;
  END IF;
END;
CALL pm_ensure_contract_project_identity();
DROP PROCEDURE pm_ensure_contract_project_identity;
