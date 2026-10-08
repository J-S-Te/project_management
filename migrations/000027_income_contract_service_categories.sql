-- 收入合同需要准确分类。只插入缺失的类别，不覆盖租户已有配置或启停状态。
INSERT INTO pm_detection_category
  (tenant_id, category, system_standard, required_qualifications, required_codes,
   special_method, enabled, created_at, updated_at, updated_by)
SELECT tenants.tenant_id, categories.category, '', '', '', 'NO', 1,
       UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 'system-migration-27'
FROM (
  SELECT tenant_id FROM pm_detection_category
  UNION SELECT tenant_id FROM pm_split_policy
  UNION SELECT tenant_id FROM pm_project
) AS tenants
CROSS JOIN (SELECT '模块开发' AS category UNION ALL SELECT '技术咨询') AS categories
WHERE NOT EXISTS (
  SELECT 1 FROM pm_detection_category existing
  WHERE existing.tenant_id = tenants.tenant_id AND existing.category = categories.category
);
