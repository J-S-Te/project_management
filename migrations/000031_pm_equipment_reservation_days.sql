-- One row per equipment per occupied calendar day. Concurrent preparation
-- transactions cannot both reserve the same device/day: MySQL's unique key
-- turns the race into a business conflict instead of two valid JSON plans.
CREATE TABLE IF NOT EXISTS pm_equipment_reservation (
  tenant_id VARCHAR(64) NOT NULL,
  resource_id VARCHAR(64) NOT NULL,
  use_date DATE NOT NULL,
  service_item_id VARCHAR(32) NOT NULL,
  window_start DATE NOT NULL,
  window_end DATE NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (tenant_id, resource_id, use_date),
  KEY idx_pm_equipment_reservation_item (tenant_id, service_item_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
