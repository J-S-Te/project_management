-- 站点档案：项目/服务项的 site 原本只是从合同复制的自由文本，既无法按站点聚合，
-- 也没有坐标可比对。站点台账把站点提升为主数据：稳定编码 + 名称 + 地址 + 坐标。
-- 坐标由录入人在现场通过浏览器定位自动获取，也可手工修正；为空表示尚未采集。
CREATE TABLE IF NOT EXISTS pm_site (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  site_code VARCHAR(64) NOT NULL,
  name VARCHAR(255) NOT NULL,
  address VARCHAR(512) NOT NULL DEFAULT '',
  latitude DECIMAL(10,7) NULL,
  longitude DECIMAL(10,7) NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'ACTIVE',
  notes VARCHAR(512) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  updated_by VARCHAR(64) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_site_tenant_code (tenant_id, site_code),
  KEY idx_pm_site_tenant_status (tenant_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
