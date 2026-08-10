ALTER TABLE pm_project
  ADD COLUMN contract_version VARCHAR(64) NOT NULL DEFAULT '' AFTER contract,
  ADD COLUMN supplement_status VARCHAR(32) NOT NULL DEFAULT 'NONE' AFTER contract_version,
  ADD UNIQUE KEY uq_pm_project_contract_version (tenant_id, contract, contract_version);

ALTER TABLE pm_service_item
  ADD COLUMN source_service_id TEXT NOT NULL AFTER project_id,
  ADD COLUMN test_mode VARCHAR(32) NOT NULL DEFAULT 'STANDARD' AFTER special,
  ADD COLUMN team_lead_id VARCHAR(64) NOT NULL DEFAULT '' AFTER test_mode,
  ADD COLUMN project_manager_id VARCHAR(64) NOT NULL DEFAULT '' AFTER team_lead_id,
  ADD COLUMN engineer_ids JSON NULL AFTER project_manager_id,
  ADD COLUMN equipment_ids JSON NULL AFTER engineer_ids,
  ADD COLUMN required_codes JSON NULL AFTER equipment_ids,
  ADD COLUMN planned_start DATETIME(3) NULL AFTER required_codes,
  ADD COLUMN planned_end DATETIME(3) NULL AFTER planned_start,
  ADD COLUMN conflict_status VARCHAR(32) NOT NULL DEFAULT 'UNCHECKED' AFTER planned_end;

CREATE TABLE IF NOT EXISTS pm_delivery_event (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(32) NOT NULL,
  service_item_id VARCHAR(32) NOT NULL DEFAULT '',
  event_type VARCHAR(64) NOT NULL,
  actor_user_id VARCHAR(64) NOT NULL,
  payload JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_pm_event_tenant_project_time (tenant_id, project_id, created_at),
  KEY idx_pm_event_tenant_service_time (tenant_id, service_item_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS pm_capability (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  resource_type VARCHAR(16) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  resource_name VARCHAR(128) NOT NULL,
  capability_codes JSON NOT NULL,
  valid_from DATETIME(3) NULL,
  valid_until DATETIME(3) NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_capability_resource (tenant_id, resource_type, resource_id),
  KEY idx_pm_capability_tenant_status (tenant_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
