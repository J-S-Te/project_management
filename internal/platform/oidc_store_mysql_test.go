package platform

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 由集成 Agent 显式启用。仅接受名称以 _session_idle_test 结尾的一次性隔离库；
// 原生运行绝不连接任何数据库（与 migrations 的隔离 MySQL 测试同一门禁风格）。
func TestOIDCSessionStoreEnforcesIdleAndAbsoluteDeadlinesOnMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PM_SESSION_IDLE_TEST_DSN"))
	if dsn == "" {
		t.Skip("PM_SESSION_IDLE_TEST_DSN is required; no connection attempted")
	}
	parsed, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(parsed.DBName, "_session_idle_test") {
		t.Fatal("requires a dedicated empty *_session_idle_test schema")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("open isolated MySQL failed")
	}
	ctx := context.Background()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	var tables int
	if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tables); err != nil {
		t.Fatal("inspect isolated schema failed")
	}
	if tables != 0 {
		t.Fatal("refusing to modify a nonempty schema")
	}
	if _, err := sqlDB.ExecContext(ctx, `CREATE TABLE pm_oidc_session (
  session_id_hash BINARY(32) NOT NULL,
  tenant_id VARCHAR(64) NOT NULL,
  identity_id VARCHAR(128) NOT NULL,
  oidc_subject VARCHAR(128) NULL,
  oidc_session_id VARCHAR(128) NULL,
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
  last_interactive_at DATETIME(3) NULL,
  revoked_at DATETIME(3) NULL,
  PRIMARY KEY (session_id_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sqlDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS pm_oidc_session") })
	store := NewGORMOIDCStore(db)
	base := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	hash := tokenDigest("mysql-session-1")
	principal := []byte(`{"identity_id":"identity-1","user_id":"identity-1","tenant_id":"tenant-1","authorization_revision":1}`)
	if err := store.CreateSession(ctx, StoredOIDCSession{SessionIDHash: hash, TenantID: "tenant-1", IdentityID: "identity-1", PrincipalJSON: principal, IDTokenCipher: []byte("id"), OAuthTokenCipher: []byte("token"), AuthorizationRevision: 1, AuthorizationCheckedAt: base, TokenExpiresAt: base.Add(time.Hour), SessionExpiresAt: base.Add(time.Hour), CreatedAt: base, LastSeenAt: base, LastInteractiveAt: base}); err != nil {
		t.Fatal(err)
	}

	// 新建会话必须持久化显式交互时间，并且绝对期限到期后不可见。
	stored, err := store.FindSession(ctx, hash, base.Add(5*time.Minute))
	if err != nil || !stored.LastInteractiveAt.Equal(base) || !stored.SessionExpiresAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("stored=%+v error=%v", stored, err)
	}
	if _, err := store.FindSession(ctx, hash, base.Add(2*time.Hour)); !errors.Is(err, errOIDCRecordNotFound) {
		t.Fatalf("absolute-expired find error = %v", err)
	}

	// 显式活动推进 last_interactive_at / last_seen_at。
	if err := store.TouchSessionActivity(ctx, hash, base.Add(10*time.Minute), base.Add(10*time.Minute-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	touched, err := store.FindSession(ctx, hash, base.Add(10*time.Minute))
	if err != nil || !touched.LastInteractiveAt.Equal(base.Add(10*time.Minute)) || !touched.LastSeenAt.Equal(base.Add(10*time.Minute)) {
		t.Fatalf("touched session=%+v error=%v", touched, err)
	}

	// 普通会话更新不得覆盖 last_interactive_at。
	if err := store.UpdateSession(ctx, StoredOIDCSession{SessionIDHash: hash, TenantID: "tenant-1", IdentityID: "identity-1", PrincipalJSON: principal, IDTokenCipher: []byte("id"), OAuthTokenCipher: []byte("token"), AuthorizationRevision: 1, AuthorizationCheckedAt: base.Add(12 * time.Minute), TokenExpiresAt: base.Add(20 * time.Minute), SessionExpiresAt: base.Add(time.Hour), CreatedAt: base, LastSeenAt: base.Add(12 * time.Minute)}, base.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	afterUpdate, _ := store.FindSession(ctx, hash, base.Add(12*time.Minute))
	if !afterUpdate.LastInteractiveAt.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("ordinary update advanced last_interactive_at: %s", afterUpdate.LastInteractiveAt)
	}

	// 空闲截止之后的条件更新必须失败且不写库。
	if err := store.TouchSessionActivity(ctx, hash, base.Add(45*time.Minute), base.Add(15*time.Minute)); !errors.Is(err, errOIDCRecordNotFound) {
		t.Fatalf("idle-expired touch error = %v", err)
	}
	afterIdle, _ := store.FindSession(ctx, hash, base.Add(44*time.Minute))
	if !afterIdle.LastInteractiveAt.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("idle-expired touch rewrote last_interactive_at: %s", afterIdle.LastInteractiveAt)
	}

	// 旧存量行 last_interactive_at 为 NULL：按 created_at 计算空闲截止。
	insertLegacy := func(name string, createdAt time.Time) []byte {
		legacyHash := tokenDigest(name)
		if _, err := sqlDB.ExecContext(ctx, "INSERT INTO pm_oidc_session(session_id_hash,tenant_id,identity_id,principal_json,id_token_ciphertext,oauth_token_ciphertext,authorization_revision,authorization_checked_at,token_expires_at,session_expires_at,created_at,last_seen_at,last_interactive_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)", legacyHash, "tenant-1", "identity-1", principal, []byte("id"), []byte("token"), 1, base, base.Add(time.Hour), base.Add(time.Hour), createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
		return legacyHash
	}
	legacyValidHash := insertLegacy("mysql-session-legacy-valid", base.Add(-10*time.Minute))
	legacy, err := store.FindSession(ctx, legacyValidHash, base)
	if err != nil || !legacy.LastInteractiveAt.IsZero() || !legacy.CreatedAt.Equal(base.Add(-10*time.Minute)) {
		t.Fatalf("legacy session=%+v error=%v", legacy, err)
	}
	// created_at 仍在新窗口内（base-10m > base-30m）：NULL 视作 created_at，可正常续期。
	if err := store.TouchSessionActivity(ctx, legacyValidHash, base, base.Add(-30*time.Minute)); err != nil {
		t.Fatalf("legacy touch within created_at idle window failed: %v", err)
	}
	legacyTouched, _ := store.FindSession(ctx, legacyValidHash, base)
	if !legacyTouched.LastInteractiveAt.Equal(base) {
		t.Fatalf("legacy touch did not record interactive time: %s", legacyTouched.LastInteractiveAt)
	}
	// created_at 早于空闲截止：同一旧行不可被续期。
	legacyExpiredHash := insertLegacy("mysql-session-legacy-expired", base.Add(-40*time.Minute))
	if err := store.TouchSessionActivity(ctx, legacyExpiredHash, base, base.Add(-30*time.Minute)); !errors.Is(err, errOIDCRecordNotFound) {
		t.Fatalf("legacy touch past created_at idle window error = %v", err)
	}

	// 已撤销会话不可续期。
	if err := store.RevokeSession(ctx, hash, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchSessionActivity(ctx, hash, base.Add(15*time.Minute), base.Add(time.Minute)); !errors.Is(err, errOIDCRecordNotFound) {
		t.Fatalf("revoked touch error = %v", err)
	}
}
