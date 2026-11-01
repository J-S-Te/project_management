ALTER TABLE pm_oidc_login_transaction
    ADD COLUMN central_ticket_cipher VARBINARY(1024) NULL,
    ADD COLUMN force_login BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pm_oidc_session
    ADD COLUMN central_binding_cipher VARBINARY(1024) NULL,
    ADD COLUMN central_session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;
