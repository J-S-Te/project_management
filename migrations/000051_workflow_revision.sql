-- One revision per changed workflow resource, committed with its delivery event.
CREATE TABLE IF NOT EXISTS pm_workflow_revision (
    tenant_id VARCHAR(64) NOT NULL,
    project_id VARCHAR(32) NOT NULL,
    service_item_id VARCHAR(32) NOT NULL DEFAULT '',
    revision BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (tenant_id, project_id, service_item_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
