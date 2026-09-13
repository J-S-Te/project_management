-- 乐观并发控制的版本号：服务项与项目各加一列。
--
-- 背景：事件写路径已在事务内对目标行加 FOR UPDATE，单字段覆盖不会发生；
-- 但客户端此前拿不到任何"我基于的是旧版本"的信号——两人同时改同一对象时，
-- 后提交者的意图会静默覆盖前者，双方都收到成功。
--
-- 本列让读取方拿到版本、写入方可以带上期望版本做条件更新（不匹配即 409 冲突）。
-- 存量行从 1 起算；新行由创建的写入路径写入初值。
ALTER TABLE pm_service_item ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 1;
ALTER TABLE pm_project ADD COLUMN version BIGINT UNSIGNED NOT NULL DEFAULT 1;
