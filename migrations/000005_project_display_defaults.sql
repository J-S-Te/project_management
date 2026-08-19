-- 统一项目未分配字段的数据库默认值与 delivery service 的初始值。
ALTER TABLE pm_project
  MODIFY COLUMN team VARCHAR(128) NOT NULL DEFAULT '未分配',
  MODIFY COLUMN manager VARCHAR(128) NOT NULL DEFAULT '—';
