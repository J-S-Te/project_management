ALTER TABLE pm_project
  ADD COLUMN owner_org_id VARCHAR(64) NOT NULL DEFAULT '' AFTER tenant_id,
  ADD COLUMN owner_identity_id VARCHAR(128) NOT NULL DEFAULT '' AFTER manager,
  ADD COLUMN manager_identity_id VARCHAR(128) NOT NULL DEFAULT '' AFTER owner_identity_id,
  ADD KEY idx_pm_project_tenant_owner_org (tenant_id, owner_org_id, id),
  ADD KEY idx_pm_project_tenant_owner_identity (tenant_id, owner_identity_id, id),
  ADD KEY idx_pm_project_tenant_manager_identity (tenant_id, manager_identity_id, id);
