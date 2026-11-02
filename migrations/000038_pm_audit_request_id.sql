-- 统一审计方案：补齐 outbox 的请求关联列。
--
-- internal/auditoutbox 的 GORM 模型按 request_id 读取领取行并映射为
-- AuditEvent.RequestID/CorrelationID，但 000030 建表与 000034 补列都没有该列。
-- 按迁移建库的真实 MySQL 上 Claim/ListFailed 会因 1054 Unknown column 直接失败，
-- 而 sqlite AutoMigrate 测试会按模型自动建列，掩盖该缺口。
--
-- 列可空：既有行与当前写入侧（auditOutboxRecord / handoverAuditRow）不写该列，
-- 保持 NULL 由投递器回退到平台生成的 request_id；不得回填或改写历史行。
ALTER TABLE prj_audit_outbox
    ADD COLUMN request_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER resource_id;
