-- 导入确认幂等台账：同一「租户 + Idempotency-Key」只允许一份确认结果。
--
-- 三类 CSV 导入（人员资质 / 设备能力 / 检测类别）的确认环节由「逐行独立保存、部分成功」
-- 改为「单事务全有或全无 + Idempotency-Key 幂等」：
--   * 本次导入的全部行写入与该台账记录在同一个事务内提交，任一行失败时一起回滚，
--     不会留下半批数据；失败也不写操作记录。
--   * 唯一键 (tenant_id, idempotency_key) 是并发兜底：同键并发只有一个事务能登记成功，
--     另一个事务回滚并读回已记录结果，避免重复写入。
--   * request_hash 是「操作类型 + 文件内容 SHA-256 + 选中行集合 + 资源类型」的稳定指纹；
--     同键不同指纹表示复用幂等键提交了另一份请求，接口层按冲突处理（409），不覆盖首次结果。
--
-- 本迁移只新增台账，不改写任何历史能力/类别/项目数据，可安全重跑。
CREATE TABLE IF NOT EXISTS pm_import_operation (
  id VARCHAR(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  operation_type VARCHAR(64) NOT NULL,
  request_hash CHAR(64) NOT NULL,
  response_json LONGTEXT NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_pm_import_operation_key (tenant_id, idempotency_key),
  KEY idx_pm_import_operation_created (tenant_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
