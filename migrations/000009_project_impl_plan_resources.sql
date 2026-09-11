-- 现场实施计划的人员清单与实施准备阶段的设备清单。
-- 两者都与服务项 1:1，随计划行一起读写，因此同表存 JSON 快照：
--   personnel 由「现场实施计划制定」写入，equipment 由「实施准备」写入（含使用时段）。
-- 资质与检定有效期在写入时由服务端从 pm_capability 快照，保留当时的派工依据；
-- 列可空，没有清单时保持 NULL，避免向 JSON 列写入空字符串。
ALTER TABLE pm_impl_plan
  ADD COLUMN personnel JSON NULL AFTER `rollback_plan`,
  ADD COLUMN equipment JSON NULL AFTER `personnel`;
