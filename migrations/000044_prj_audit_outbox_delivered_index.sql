-- 已投递审计行的清理索引。
--
-- internal/auditoutbox.GORMStore.PurgeDelivered 的清理谓词是
--   delivery_status = 'delivered' AND delivered_at IS NOT NULL AND delivered_at < cutoff
--   ORDER BY delivered_at, id LIMIT batch
-- 000030 只建了 (delivery_status, next_attempt_at) 的领取索引，清理谓词用不上它：
-- outbox 表随投递只增不减时，每轮清理都会退化成扫全部已投递行再排序。
-- 本迁移补 (delivery_status, delivered_at)，前导列等值 + 第二列范围/排序与该谓词同序。
--
-- 只加索引，不改列、不删行、不回填历史数据，可安全重跑。
-- 清理本身默认关闭（AUDIT_OUTBOX_DELIVERED_RETENTION_DAYS 缺省=0），
-- 因此本索引在启用清理前只占空间，不影响任何读写语义。
-- 同名索引若已存在但定义漂移，则失败关闭，不默默替换未知索引。

DROP PROCEDURE IF EXISTS pm_ensure_prj_audit_outbox_delivered_index;

-- pm_ensure_prj_audit_outbox_delivered_index 幂等补建清理索引。
-- 参数：无。
-- 返回值：无；索引已存在且定义一致时为空操作。
CREATE PROCEDURE pm_ensure_prj_audit_outbox_delivered_index()
BEGIN
  DECLARE index_columns TEXT DEFAULT NULL;
  DECLARE index_count INT DEFAULT 0;

  SELECT COUNT(*), COALESCE(GROUP_CONCAT(LOWER(COLUMN_NAME) ORDER BY SEQ_IN_INDEX), '')
    INTO index_count, index_columns
    FROM information_schema.STATISTICS
   WHERE TABLE_SCHEMA = DATABASE()
     AND TABLE_NAME = 'prj_audit_outbox'
     AND INDEX_NAME = 'idx_prj_audit_outbox_delivered';

  IF index_count = 0 THEN
    ALTER TABLE prj_audit_outbox
      ADD INDEX idx_prj_audit_outbox_delivered (delivery_status, delivered_at);
  ELSEIF index_columns <> 'delivery_status,delivered_at' THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'idx_prj_audit_outbox_delivered definition mismatch; no data changed';
  END IF;
END;

CALL pm_ensure_prj_audit_outbox_delivered_index();
DROP PROCEDURE pm_ensure_prj_audit_outbox_delivered_index;
