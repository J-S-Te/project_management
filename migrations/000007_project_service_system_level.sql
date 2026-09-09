ALTER TABLE pm_service_item
  ADD COLUMN system_level VARCHAR(64) NOT NULL DEFAULT '' AFTER `system`;
