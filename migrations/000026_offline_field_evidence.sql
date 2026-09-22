-- 现场签到、记录、电子签名支持离线采集后安全补传。
-- NULL 表示历史/在线非幂等事件；MySQL 唯一索引允许多个 NULL，避免影响既有事件。
ALTER TABLE pm_delivery_event
  ADD COLUMN client_operation_id VARCHAR(128) NULL AFTER actor_user_id,
  ADD COLUMN captured_at DATETIME(3) NULL AFTER client_operation_id,
  ADD UNIQUE KEY uq_pm_event_offline_operation
    (tenant_id, service_item_id, actor_user_id, client_operation_id),
  ADD KEY idx_pm_event_capture_time (tenant_id, service_item_id, captured_at);
