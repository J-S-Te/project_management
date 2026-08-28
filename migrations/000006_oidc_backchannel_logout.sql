ALTER TABLE pm_oidc_session
  ADD COLUMN oidc_subject VARCHAR(128) NULL AFTER identity_id,
  ADD COLUMN oidc_session_id VARCHAR(128) NULL AFTER oidc_subject,
  ADD KEY idx_pm_oidc_session_sid (tenant_id, oidc_session_id, revoked_at),
  ADD KEY idx_pm_oidc_session_oidc_subject (tenant_id, oidc_subject, revoked_at);

CREATE TABLE IF NOT EXISTS pm_oidc_backchannel_logout_replay (
  jti VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  issuer VARCHAR(512) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  audience VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  consumed_at DATETIME(3) NOT NULL,
  PRIMARY KEY (jti),
  KEY idx_pm_oidc_backchannel_replay_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
