-- 合同编号仅用于展示；跨系统幂等优先使用合同管理的稳定合同 ID + 版本。
-- 历史/人工项目以及旧测试数据可能仍没有 contract_id，生成列在滚动发布期间回退到
-- contract，既避免多个空 contract_id 互相冲突，也保留旧数据的同合同版本唯一性。
ALTER TABLE pm_project
  DROP INDEX uq_pm_project_contract_version,
  ADD COLUMN contract_identity_key VARCHAR(64)
    GENERATED ALWAYS AS (CASE WHEN contract_id <> '' THEN contract_id ELSE contract END) STORED,
  ADD UNIQUE KEY uq_pm_project_contract_version (tenant_id, contract_identity_key, contract_version);
