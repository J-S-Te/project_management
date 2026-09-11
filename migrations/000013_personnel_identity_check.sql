-- 人员资质档案与基础平台负责人目录的复核结果。
-- 资质由本系统维护，但"这个人是否真实存在（在职）"只能由基础平台回答：
-- 每次复核写入 identity_status 与 identity_checked_at，界面据此区分
-- 在职人员 / 已离职或查无此人 / 历史未关联档案。
ALTER TABLE pm_capability ADD COLUMN identity_status VARCHAR(16) NOT NULL DEFAULT 'UNLINKED';
ALTER TABLE pm_capability ADD COLUMN identity_checked_at DATETIME(3) NULL;
