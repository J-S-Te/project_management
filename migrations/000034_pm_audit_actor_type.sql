-- 统一审计方案 §2.2：区分人工操作与系统自动执行。
--
-- 自动化/告警事件由 Worker 在源事件之后追加，操作者沿用源事件的人，但执行者是系统；
-- 缺少该列时平台只能把它们当成人工操作。历史行保持空值，按 USER 兼容读取。
ALTER TABLE prj_audit_outbox
    ADD COLUMN actor_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER actor_id;
