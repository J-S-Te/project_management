-- Local proposals never impersonate an approved contract or bypass delivery approval.
CREATE TABLE IF NOT EXISTS pm_native_scope_change (
 id VARCHAR(32) NOT NULL,
 tenant_id VARCHAR(64) NOT NULL,
 project_id VARCHAR(32) NOT NULL,
 operation_id VARCHAR(64) NOT NULL,
 pending_project VARCHAR(32) NULL,
 fingerprint CHAR(64) NOT NULL,
 base_scope_version BIGINT UNSIGNED NOT NULL,
 expected_project_version BIGINT UNSIGNED NOT NULL,
 version BIGINT UNSIGNED NOT NULL,
 status VARCHAR(16) NOT NULL,
 reason VARCHAR(500) NOT NULL,
 requested_by VARCHAR(128) NOT NULL,
 decided_by VARCHAR(128) NOT NULL DEFAULT '',
 decision_operation VARCHAR(64) NOT NULL DEFAULT '',
 decision_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
 comment VARCHAR(500) NOT NULL DEFAULT '',
 scope_hash CHAR(64) NOT NULL,
 catalog_hash CHAR(64) NOT NULL,
 applied_scope_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 service_items JSON NOT NULL,
 created_at DATETIME(3) NOT NULL,
 decided_at DATETIME(3) NULL,
 PRIMARY KEY (id),
 UNIQUE KEY uq_native_scope_operation (tenant_id,project_id,operation_id),
 UNIQUE KEY uq_native_scope_pending (tenant_id,pending_project),
 KEY idx_native_scope_project (tenant_id,project_id,created_at),
 CONSTRAINT chk_native_scope_decision CHECK (
  (status='PENDING' AND pending_project=project_id AND decided_at IS NULL AND applied_scope_version=0)
  OR (status IN ('APPROVED','REJECTED','WITHDRAWN') AND pending_project IS NULL
      AND decided_at IS NOT NULL AND decided_by<>''
      AND (status<>'APPROVED' OR (requested_by<>decided_by AND applied_scope_version=base_scope_version+1)))
 )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
-- Keep v1 scope evidence untouched. Approved v2+ evidence links through project/version.
-- Recover forward; never delete old scope or accounting/report evidence.
