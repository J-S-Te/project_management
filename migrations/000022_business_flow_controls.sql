-- 项目业务闭环强化：报告职责分离、不可变版本账本、可靠通知和现场证据。
ALTER TABLE pm_project
  ADD COLUMN contract_id VARCHAR(64) NOT NULL DEFAULT '' AFTER contract;

UPDATE pm_project SET contract_id = contract WHERE contract_id = '';

ALTER TABLE pm_service_item
  ADD COLUMN report_prepared_by VARCHAR(64) NOT NULL DEFAULT '' AFTER report_revision,
  ADD COLUMN report_reviewed_by VARCHAR(64) NOT NULL DEFAULT '' AFTER report_prepared_by,
  ADD COLUMN report_issued_by VARCHAR(64) NOT NULL DEFAULT '' AFTER report_reviewed_by,
  ADD COLUMN site_code VARCHAR(64) NOT NULL DEFAULT '' AFTER site;

CREATE INDEX idx_pm_service_site_code
  ON pm_service_item (tenant_id, site_code);

CREATE TABLE pm_report_revision (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  service_item_id VARCHAR(32) NOT NULL,
  revision INT UNSIGNED NOT NULL,
  status VARCHAR(32) NOT NULL,
  validity_status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
  correction_request_id VARCHAR(32) NOT NULL DEFAULT '',
  correction_reason VARCHAR(1000) NOT NULL DEFAULT '',
  file_id VARCHAR(64) NOT NULL DEFAULT '',
  file_name VARCHAR(255) NOT NULL DEFAULT '',
  file_mime VARCHAR(128) NOT NULL DEFAULT '',
  file_size BIGINT UNSIGNED NOT NULL DEFAULT 0,
  file_sha256 CHAR(64) NOT NULL DEFAULT '',
  prepared_by VARCHAR(64) NOT NULL DEFAULT '',
  prepared_at DATETIME(3) NULL,
  reviewed_by VARCHAR(64) NOT NULL DEFAULT '',
  reviewed_at DATETIME(3) NULL,
  issued_by VARCHAR(64) NOT NULL DEFAULT '',
  issued_at DATETIME(3) NULL,
  archived_by VARCHAR(64) NOT NULL DEFAULT '',
  archived_at DATETIME(3) NULL,
  invalidated_by VARCHAR(64) NOT NULL DEFAULT '',
  invalidated_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_report_revision (tenant_id, service_item_id, revision),
  KEY idx_pm_report_revision_state (tenant_id, validity_status, status, updated_at),
  CONSTRAINT chk_pm_report_revision_status
    CHECK (status IN ('NONE','COMPILING','REVIEWED','ISSUED','ARCHIVED')),
  CONSTRAINT chk_pm_report_revision_validity
    CHECK (validity_status IN ('ACTIVE','VOID'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 存量报告按服务项当前状态补建 R0，不虚构历史操作者；后续动作会逐步补齐职责字段。
INSERT INTO pm_report_revision
  (tenant_id, service_item_id, revision, status, validity_status, prepared_by,
   reviewed_by, issued_by, created_at, updated_at)
SELECT tenant_id, id, report_revision,
       CASE WHEN report_status IN ('COMPILING','REVIEWED','ISSUED','ARCHIVED') THEN report_status ELSE 'NONE' END,
       'ACTIVE',
       CASE WHEN report_status IN ('COMPILING','REVIEWED','ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END,
       CASE WHEN report_status IN ('REVIEWED','ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END,
       CASE WHEN report_status IN ('ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END,
       created_at, updated_at
FROM pm_service_item
WHERE archived_at IS NULL;

UPDATE pm_service_item
SET report_prepared_by = CASE WHEN report_status IN ('COMPILING','REVIEWED','ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END,
    report_reviewed_by = CASE WHEN report_status IN ('REVIEWED','ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END,
    report_issued_by = CASE WHEN report_status IN ('ISSUED','ARCHIVED') THEN report_updated_by ELSE '' END
WHERE archived_at IS NULL;

CREATE TABLE pm_evidence_file (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  service_item_id VARCHAR(32) NOT NULL,
  evidence_kind VARCHAR(32) NOT NULL,
  file_id VARCHAR(64) NOT NULL,
  file_name VARCHAR(255) NOT NULL,
  file_mime VARCHAR(128) NOT NULL,
  file_size BIGINT UNSIGNED NOT NULL,
  file_sha256 CHAR(64) NOT NULL,
  created_by VARCHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_evidence_file (tenant_id, file_id),
  KEY idx_pm_evidence_item (tenant_id, service_item_id, evidence_kind, created_at),
  CONSTRAINT chk_pm_evidence_kind
    CHECK (evidence_kind IN ('FIELD','DEVIATION','REPORT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE pm_notification_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  event_id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  aggregate_type VARCHAR(32) NOT NULL,
  aggregate_id VARCHAR(64) NOT NULL,
  payload JSON NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  next_retry_at DATETIME(3) NULL,
  locked_by VARCHAR(128) NOT NULL DEFAULT '',
  locked_until DATETIME(3) NULL,
  last_error_summary VARCHAR(1000) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  sent_at DATETIME(3) NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_notification_event (event_id),
  UNIQUE KEY uq_pm_notification_idempotency (tenant_id, idempotency_key),
  KEY idx_pm_notification_lease (status, next_retry_at, locked_until, created_at),
  CONSTRAINT chk_pm_notification_status
    CHECK (status IN ('PENDING','PROCESSING','RETRY_WAIT','SENT','DEAD_LETTER'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
