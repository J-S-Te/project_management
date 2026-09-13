-- 服务项「进入当前状态的时刻」。
--
-- 背景：SLA 的「状态停留时长」业务定义是"停留在当前状态的时长"，此前依赖 updated_at，
-- 但任何无关字段的更新（改备注、同步资质等）都会刷新 updated_at，导致计时被意外重置。
-- 本列只在 status 真正变化时写入，作为 SLA 计时的唯一基准。
--
-- 存量行没有历史状态变更记录，按最近一次行更新回填：这与本迁移之前的口径一致，
-- 不引入倒退，也不伪造更早的时间。新行由服务项创建路径写入初始值。
ALTER TABLE pm_service_item
  ADD COLUMN status_changed_at DATETIME(3) NULL AFTER `status`;

UPDATE pm_service_item
   SET status_changed_at = updated_at
 WHERE status_changed_at IS NULL;

-- SLA 按状态过滤候选并比较停留时长，加索引避免全表扫描。
CREATE INDEX idx_pm_service_tenant_status_changed ON pm_service_item (tenant_id, status, status_changed_at);
