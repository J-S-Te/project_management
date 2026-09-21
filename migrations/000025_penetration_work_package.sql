-- 等保测评服务项内嵌渗透测试专项：独立决策/实施/报告，不生成额外服务项或结算项。
CREATE TABLE pm_penetration_work_package (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(32) NOT NULL,
  parent_service_item_id VARCHAR(32) NOT NULL,
  decision_status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  customer_contact VARCHAR(128) NOT NULL DEFAULT '',
  communicated_at DATETIME(3) NULL,
  communication_summary VARCHAR(2000) NOT NULL DEFAULT '',
  decision_change_reason VARCHAR(1000) NOT NULL DEFAULT '',
  planned_start DATETIME(3) NULL,
  planned_end DATETIME(3) NULL,
  engineer_ids JSON NULL,
  auth_doc_no VARCHAR(128) NOT NULL DEFAULT '',
  auth_start DATETIME(3) NULL,
  auth_end DATETIME(3) NULL,
  auth_scope TEXT NOT NULL,
  test_scope TEXT NOT NULL,
  test_window VARCHAR(128) NOT NULL DEFAULT '',
  emergency_contact VARCHAR(128) NOT NULL DEFAULT '',
  rollback_plan TEXT NOT NULL,
  execution_status VARCHAR(16) NOT NULL DEFAULT 'NOT_STARTED',
  report_status VARCHAR(16) NOT NULL DEFAULT 'NONE',
  report_revision INT UNSIGNED NOT NULL DEFAULT 0,
  version BIGINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_penetration_parent (tenant_id, parent_service_item_id),
  KEY idx_pm_penetration_project_state (tenant_id, project_id, decision_status, execution_status),
  CONSTRAINT chk_pm_penetration_decision CHECK (decision_status IN ('PENDING','REQUIRED','NOT_REQUIRED')),
  CONSTRAINT chk_pm_penetration_execution CHECK (execution_status IN ('NOT_STARTED','IN_PROGRESS','COMPLETED','CANCELLED')),
  CONSTRAINT chk_pm_penetration_report CHECK (report_status IN ('NONE','DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE pm_penetration_work_package_event (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  work_package_id VARCHAR(32) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  actor_user_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  payload JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_penetration_event_idempotency (tenant_id, idempotency_key),
  KEY idx_pm_penetration_event_package (tenant_id, work_package_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- 报告版本账本增加主体维度；旧数据全部保持 SERVICE_ITEM 语义。
ALTER TABLE pm_report_revision
  ADD COLUMN subject_type VARCHAR(32) NOT NULL DEFAULT 'SERVICE_ITEM' AFTER service_item_id,
  ADD COLUMN subject_id VARCHAR(32) NOT NULL DEFAULT '' AFTER subject_type;

UPDATE pm_report_revision SET subject_id = service_item_id WHERE subject_id = '';

ALTER TABLE pm_report_revision
  DROP INDEX uq_pm_report_revision,
  ADD UNIQUE KEY uq_pm_report_revision_subject (tenant_id, subject_type, subject_id, revision),
  ADD KEY idx_pm_report_revision_parent_item (tenant_id, service_item_id, subject_type),
  ADD CONSTRAINT chk_pm_report_revision_subject_type
    CHECK (subject_type IN ('SERVICE_ITEM','PENETRATION_WORK_PACKAGE'));

ALTER TABLE pm_report_revision
  DROP CHECK chk_pm_report_revision_status,
  ADD CONSTRAINT chk_pm_report_revision_status
    CHECK (status IN ('NONE','COMPILING','REVIEWED','DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'));
