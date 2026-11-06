-- 主服务项报告强制经过整改；共享修订表仍保留渗透专项的独立状态。
-- 本迁移不改写历史行。DDL 成功而迁移账本未记入时，可核验新约束后安全重跑；
-- 也兼容旧分步迁移在删除约束后中断的结构，添加约束会重新检查已有行。
-- 同名约束若定义漂移或未启用则失败关闭，不能默默替换未知规则。
DROP PROCEDURE IF EXISTS pm_ensure_report_rectifying;

-- pm_ensure_report_rectifying 核验并扩展报告修订的合法状态集合。
-- 参数：无。
-- 返回值：无；结构漂移或不合法历史值时拒绝迁移，保留数据。
CREATE PROCEDURE pm_ensure_report_rectifying()
BEGIN
  DECLARE check_count INT DEFAULT 0;
  DECLARE check_clause TEXT DEFAULT '';
  DECLARE check_enforced VARCHAR(3) DEFAULT '';
  DECLARE normalized_clause TEXT DEFAULT '';

  SELECT COUNT(*), COALESCE(MAX(cc.CHECK_CLAUSE), ''), COALESCE(MAX(tc.ENFORCED), '')
    INTO check_count, check_clause, check_enforced
    FROM information_schema.TABLE_CONSTRAINTS AS tc
    JOIN information_schema.CHECK_CONSTRAINTS AS cc
      ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
     AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
   WHERE tc.CONSTRAINT_SCHEMA = DATABASE()
     AND tc.TABLE_NAME = 'pm_report_revision'
     AND tc.CONSTRAINT_TYPE = 'CHECK'
     AND tc.CONSTRAINT_NAME = 'chk_pm_report_revision_status';

  SET normalized_clause = LOWER(
    REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(
      check_clause, '_utf8mb4', ''), CHAR(92), ''), '`', ''), '(', ''), ')', ''), ' ', '')
  );

  IF check_count > 1 OR (check_count = 1 AND (
       check_enforced <> 'YES'
       OR normalized_clause NOT IN (
         'statusin''none'',''compiling'',''reviewed'',''drafting'',''submitted'',''approved'',''issued'',''archived''',
         'statusin''none'',''rectifying'',''compiling'',''reviewed'',''drafting'',''submitted'',''approved'',''issued'',''archived'''
       ))) THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'report status constraint definition mismatch; no data changed';
  END IF;

  IF check_count = 0 THEN
    ALTER TABLE pm_report_revision
      ADD CONSTRAINT chk_pm_report_revision_status
      CHECK (status IN ('NONE','RECTIFYING','COMPILING','REVIEWED',
        'DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'));
  ELSEIF normalized_clause =
      'statusin''none'',''compiling'',''reviewed'',''drafting'',''submitted'',''approved'',''issued'',''archived''' THEN
    ALTER TABLE pm_report_revision
      DROP CHECK chk_pm_report_revision_status,
      ADD CONSTRAINT chk_pm_report_revision_status
      CHECK (status IN ('NONE','RECTIFYING','COMPILING','REVIEWED',
        'DRAFTING','SUBMITTED','APPROVED','ISSUED','ARCHIVED'));
  END IF;
END;

CALL pm_ensure_report_rectifying();
DROP PROCEDURE pm_ensure_report_rectifying;
