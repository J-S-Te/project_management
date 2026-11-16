-- 000049_detection_category_dictionary.sql
-- 检测类别名称由平台 CONTRACT_SERVICE_TYPE 字典维护；项目只保留扩展字段。
ALTER TABLE pm_detection_category
  ADD COLUMN dictionary_item_code VARCHAR(64) NULL AFTER category;

UPDATE pm_detection_category
SET dictionary_item_code = CASE category
  WHEN '等保测评' THEN 'CLASSIFIED_PROTECTION'
  WHEN '商用密码应用安全性评估' THEN 'COMMERCIAL_CRYPTO'
  WHEN '软件测试' THEN 'SOFTWARE_TEST'
  WHEN '源代码审计' THEN 'SOURCE_CODE_AUDIT'
  WHEN '渗透测试' THEN 'PENETRATION_TEST'
  WHEN '漏洞扫描' THEN 'VULNERABILITY_SCAN'
  WHEN 'APP 安全加固' THEN 'APP_SECURITY_HARDENING'
  WHEN '上线测试' THEN 'GO_LIVE_TEST'
  WHEN '网络安全风险评估' THEN 'NETWORK_RISK_ASSESSMENT'
  WHEN '差距分析' THEN 'GAP_ANALYSIS'
  WHEN '机房检测' THEN 'DATA_CENTER_INSPECTION'
  WHEN '网络安全巡检服务' THEN 'NETWORK_PATROL'
  WHEN '安全培训' THEN 'SECURITY_TRAINING'
  WHEN '安全性测试' THEN 'SECURITY_TEST'
  WHEN '应急响应服务' THEN 'EMERGENCY_RESPONSE'
  WHEN '网络安全攻防演练' THEN 'ATTACK_DEFENSE_DRILL'
  WHEN '安全运维' THEN 'SECURITY_OPERATION'
  WHEN '模块开发' THEN 'MODULE_DEVELOPMENT'
  WHEN '技术咨询' THEN 'TECHNICAL_CONSULTING'
  ELSE NULL
END;

ALTER TABLE pm_detection_category
  DROP INDEX uk_pm_detection_category,
  ADD UNIQUE KEY uk_pm_detection_category_code (tenant_id, dictionary_item_code);
