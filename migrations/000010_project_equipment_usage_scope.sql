-- 设备使用范围：可借出（ANY）或仅在公司使用（COMPANY_ONLY）。
-- 「仅在公司使用」的设备不允许登记到任何服务项的设备清单（即不可借出）；
-- 「在公司 / 不在公司」不落库，由设备当前是否处于某服务项的使用时段内自动派生。
ALTER TABLE pm_capability
  ADD COLUMN usage_scope VARCHAR(16) NOT NULL DEFAULT 'ANY' AFTER `status`;
