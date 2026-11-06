-- 显式原生委托不代表已审批合同。旧记录继续按合同来源约束处理。
-- 必须在开启 PROJECT_NATIVE_ENABLED 之前应用；不改写历史业务数据或审批状态。
ALTER TABLE pm_project
  ADD COLUMN source_kind VARCHAR(16) NOT NULL DEFAULT 'CONTRACT',
  ADD COLUMN source_id VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN scope_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
  DROP INDEX uq_pm_project_contract_identity,
  MODIFY COLUMN contract_identity_key VARCHAR(64)
    GENERATED ALWAYS AS (
      CASE WHEN source_kind = 'CONTRACT'
        THEN CASE WHEN contract_id <> '' THEN contract_id ELSE contract END
        ELSE NULL END
    ) STORED,
  ADD UNIQUE KEY uq_pm_project_contract_identity (tenant_id, contract_identity_key),
  ADD COLUMN native_source_key VARCHAR(64)
    GENERATED ALWAYS AS (CASE WHEN source_kind = 'NATIVE' THEN source_id ELSE NULL END) STORED,
  ADD UNIQUE KEY uq_pm_project_native_source (tenant_id, native_source_key),
  ADD CONSTRAINT chk_pm_project_source CHECK (
    (source_kind='CONTRACT' AND scope_version=0)
    OR (source_kind='NATIVE' AND source_id<>'' AND scope_version>=1
      AND contract='' AND contract_id='' AND contract_version='')
  );

CREATE TABLE pm_native_project_command (
  tenant_id VARCHAR(64) NOT NULL,
  source_id VARCHAR(64) NOT NULL,
  fingerprint VARCHAR(64) NOT NULL,
  project_id VARCHAR(32) NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id,source_id)
);
CREATE TABLE pm_native_counterparty (
  tenant_id VARCHAR(64) NOT NULL,
  id VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id,id)
);
CREATE TABLE pm_native_scope_version (
  tenant_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(32) NOT NULL,
  version BIGINT UNSIGNED NOT NULL,
  source_id VARCHAR(64) NOT NULL,
  actor_identity_id VARCHAR(128) NOT NULL,
  service_items JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id,project_id,version)
);
