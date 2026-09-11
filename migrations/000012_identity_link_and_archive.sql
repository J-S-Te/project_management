-- 评审修复配套迁移：
-- 1. pm_capability.user_id：人员能力档案关联平台 user_id，消除「指派用平台账号、
--    计划用档案编号」的双轨制，让实施计划可以直接从已指派人员推导。
-- 2. pm_project.customer_id：合同激活携带客户标识落库，客户聚合与对账不再依赖自由文本匹配。
-- 3. pm_service_item.archived_at：拆解调整改为归档旧服务项而非物理删除，
--    保留交付事件、设备借用历史与报告依据的完整审计链。
ALTER TABLE pm_capability ADD COLUMN user_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE pm_project ADD COLUMN customer_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE pm_service_item ADD COLUMN archived_at DATETIME(3) NULL;
