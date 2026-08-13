CREATE TABLE IF NOT EXISTS pm_oidc_login_transaction (
  state_hash BINARY(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  nonce_ciphertext VARBINARY(512) NOT NULL,
  code_verifier_ciphertext VARBINARY(1024) NOT NULL,
  return_path VARCHAR(512) NOT NULL,
  expires_at DATETIME(3) NOT NULL,
  consumed_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (state_hash),
  KEY idx_pm_oidc_login_tenant_expiry (tenant_id, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS pm_oidc_session (
  session_id_hash BINARY(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  identity_id VARCHAR(128) NOT NULL,
  person_id VARCHAR(64) NOT NULL DEFAULT '',
  principal_json JSON NOT NULL,
  id_token_ciphertext MEDIUMBLOB NOT NULL,
  oauth_token_ciphertext MEDIUMBLOB NOT NULL,
  authorization_revision BIGINT UNSIGNED NOT NULL,
  authorization_checked_at DATETIME(3) NOT NULL,
  refresh_retry_at DATETIME(3) NULL,
  token_expires_at DATETIME(3) NOT NULL,
  session_expires_at DATETIME(3) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  last_seen_at DATETIME(3) NOT NULL,
  revoked_at DATETIME(3) NULL,
  PRIMARY KEY (session_id_hash),
  KEY idx_pm_oidc_session_subject (tenant_id, identity_id, revoked_at),
  KEY idx_pm_oidc_session_expiry (session_expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
