-- 主合同、普通补充合同及历史绑定共用身份围栏，防止跨表重复归属。
CREATE TABLE IF NOT EXISTS pm_contract_identity_guard (
 tenant_id VARCHAR(64) NOT NULL,
 contract_identity VARCHAR(128) NOT NULL,
 project_id VARCHAR(32) NOT NULL DEFAULT '',
 PRIMARY KEY(tenant_id,contract_identity)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
