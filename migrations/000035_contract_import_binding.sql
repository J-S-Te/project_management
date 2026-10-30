-- 新机器命令、来源映射与结果共用事务，保留主合同唯一身份约束。
CREATE TABLE IF NOT EXISTS pm_contract_import_binding (
 tenant_id VARCHAR(64) NOT NULL,
 command_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 origin_import_item_id VARCHAR(64) NOT NULL,
 source_application VARCHAR(64) NOT NULL,
 contract_id VARCHAR(64) NOT NULL,
 project_id VARCHAR(32) NOT NULL DEFAULT '',
 relationship VARCHAR(16) NOT NULL,
 source_revision BIGINT UNSIGNED NOT NULL,
 policy_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 release_version BIGINT UNSIGNED NOT NULL,
 payload_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 receipt JSON NULL,
 bound_by VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY (tenant_id, command_id),
 UNIQUE KEY uq_pm_import_item (tenant_id, origin_import_item_id),
 UNIQUE KEY uq_pm_import_contract (tenant_id, source_application, contract_id),
 KEY idx_pm_import_project (tenant_id, project_id),
 CONSTRAINT ck_pm_import_relationship CHECK (relationship IN ('PRIMARY','SUPPLEMENT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
