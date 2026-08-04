CREATE TABLE IF NOT EXISTS pm_project (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  customer VARCHAR(255) NOT NULL,
  contract VARCHAR(64) NOT NULL,
  services INT NOT NULL DEFAULT 0,
  category VARCHAR(255) NOT NULL DEFAULT '',
  team VARCHAR(128) NOT NULL DEFAULT '',
  manager VARCHAR(128) NOT NULL DEFAULT '',
  health VARCHAR(32) NOT NULL DEFAULT '待确认',
  status VARCHAR(32) NOT NULL,
  progress INT NOT NULL DEFAULT 0,
  due VARCHAR(64) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_project_tenant_id (tenant_id, id),
  KEY idx_pm_project_tenant_status (tenant_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS pm_service_item (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(32) NOT NULL,
  batch VARCHAR(64) NOT NULL DEFAULT '',
  site VARCHAR(255) NOT NULL DEFAULT '',
  category VARCHAR(255) NOT NULL DEFAULT '',
  requirement TEXT NOT NULL,
  `system` VARCHAR(128) NOT NULL DEFAULT '',
  special VARCHAR(16) NOT NULL DEFAULT '否',
  status VARCHAR(32) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_service_tenant_project (tenant_id, project_id),
  CONSTRAINT fk_pm_service_project FOREIGN KEY (tenant_id, project_id) REFERENCES pm_project(tenant_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS pm_rule (
  id BIGINT NOT NULL AUTO_INCREMENT,
  tenant_id VARCHAR(64) NOT NULL,
  kind VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  scope VARCHAR(255) NOT NULL,
  `trigger` TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  KEY idx_pm_rule_tenant_kind (tenant_id, kind)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
