-- 统一业务子系统会话（2026-10-30）：空闲 30 分钟 + 绝对 8 小时。
-- 只新增显式交互时间列：last_seen_at 仍表示最近一次业务请求，last_interactive_at 只由
-- POST /api/v1/auth/activity 推进，用于计算空闲期限。
-- 存量行保持 NULL，由代码按 created_at 解释（升级不锁死在线会话）；不修改其它列，
-- 也不回填或改写既有会话的绝对到期时间。
ALTER TABLE pm_oidc_session
  ADD COLUMN last_interactive_at DATETIME(3) NULL AFTER last_seen_at;
