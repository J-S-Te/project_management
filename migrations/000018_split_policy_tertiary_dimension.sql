-- 分组维度 3（可选）：原型 PG-CFG-01 的覆盖规则示例是「场所 + 批次 + 检测类别」三维分组，
-- 两维模型表达不了。空串表示只用前两维，默认分组规则（批次 + 检测类别）不受影响。
ALTER TABLE pm_split_policy
  ADD COLUMN dimension_tertiary VARCHAR(32) NOT NULL DEFAULT '' AFTER dimension_secondary;
