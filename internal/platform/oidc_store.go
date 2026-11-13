package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errOIDCRecordNotFound = errors.New("OIDC record not found")

var errOIDCBackchannelReplay = errors.New("OIDC back-channel logout token was already consumed")

type LoginTransaction struct {
	CentralTicketCipher []byte
	ForceLogin          bool
	StateHash           []byte
	TenantID            string
	NonceCipher         []byte
	CodeVerifierCipher  []byte
	ReturnPath          string
	ExpiresAt           time.Time
	ConsumedAt          *time.Time
	CreatedAt           time.Time
}

type StoredOIDCSession struct {
	CentralBindingCipher   []byte
	CentralSessionID       string
	SessionIDHash          []byte
	TenantID               string
	IdentityID             string
	OIDCSubject            string
	OIDCSessionID          string
	PersonID               string
	PrincipalJSON          []byte
	IDTokenCipher          []byte
	OAuthTokenCipher       []byte
	AuthorizationRevision  uint64
	AuthorizationCheckedAt time.Time
	RefreshRetryAt         time.Time
	TokenExpiresAt         time.Time
	SessionExpiresAt       time.Time
	CreatedAt              time.Time
	LastSeenAt             time.Time
	// LastInteractiveAt 只由显式活动接口推进；为零值表示旧存量行（NULL），
	// 认证层按 CreatedAt 解释，升级时不会锁死在线会话。
	LastInteractiveAt time.Time
	RevokedAt         *time.Time
}

type OIDCStore interface {
	SaveLoginTransaction(context.Context, LoginTransaction) error
	ConsumeLoginTransaction(context.Context, []byte, time.Time) (LoginTransaction, error)
	CreateSession(context.Context, StoredOIDCSession) error
	FindSession(context.Context, []byte, time.Time) (StoredOIDCSession, error)
	UpdateSession(context.Context, StoredOIDCSession, time.Time) error
	TouchSessionLastSeen(context.Context, []byte, time.Time) error
	// TouchSessionActivity 在会话仍满足绝对期限与空闲截止（idleCutoff）时推进
	// last_interactive_at 与 last_seen_at；已过期/撤销会话不得被复活。
	TouchSessionActivity(context.Context, []byte, time.Time, time.Time) error
	RevokeSession(context.Context, []byte, time.Time) error
	RevokeSessionsForIdentity(context.Context, string, string, time.Time) error
}

type oidcLoginRecord struct {
	CentralTicketCipher []byte
	ForceLogin          bool
	StateHash           []byte `gorm:"column:state_hash;primaryKey;type:binary(32)"`
	TenantID            string `gorm:"column:tenant_id;size:64;not null;index"`
	NonceCipher         []byte `gorm:"column:nonce_ciphertext;type:varbinary(512);not null"`
	CodeVerifierCipher  []byte `gorm:"column:code_verifier_ciphertext;type:varbinary(1024);not null"`
	ReturnPath          string `gorm:"column:return_path;size:512;not null"`
	ExpiresAt           time.Time
	ConsumedAt          *time.Time
	CreatedAt           time.Time
}

func (oidcLoginRecord) TableName() string { return "pm_oidc_login_transaction" }

type oidcSessionRecord struct {
	CentralBindingCipher   []byte
	CentralSessionID       string
	SessionIDHash          []byte `gorm:"column:session_id_hash;primaryKey;type:binary(32)"`
	TenantID               string `gorm:"column:tenant_id;size:64;not null;index"`
	IdentityID             string `gorm:"column:identity_id;size:128;not null;index"`
	OIDCSubject            string `gorm:"column:oidc_subject;size:128"`
	OIDCSessionID          string `gorm:"column:oidc_session_id;size:128"`
	PersonID               string `gorm:"column:person_id;size:64"`
	PrincipalJSON          []byte `gorm:"column:principal_json;type:json;not null"`
	IDTokenCipher          []byte `gorm:"column:id_token_ciphertext;type:mediumblob;not null"`
	OAuthTokenCipher       []byte `gorm:"column:oauth_token_ciphertext;type:mediumblob;not null"`
	AuthorizationRevision  uint64 `gorm:"column:authorization_revision;not null"`
	AuthorizationCheckedAt time.Time
	RefreshRetryAt         *time.Time
	TokenExpiresAt         time.Time
	SessionExpiresAt       time.Time
	CreatedAt              time.Time
	LastSeenAt             time.Time
	// 可空：迁移前的存量行没有显式交互时间，语义是"按 created_at 计算"。
	LastInteractiveAt *time.Time
	RevokedAt         *time.Time
}

func (oidcSessionRecord) TableName() string { return "pm_oidc_session" }

type GORMOIDCStore struct{ db *gorm.DB }

func NewGORMOIDCStore(db *gorm.DB) *GORMOIDCStore { return &GORMOIDCStore{db: db} }

// SaveLoginTransaction 持久化一次性登录事务及受控桥接材料。
// 参数：ctx为调用上下文与取消边界。
//
//	value为待处理的本地持久记录。
//
// 返回值：无；参数、校验、存储或依赖失败时返回错误。
func (s *GORMOIDCStore) SaveLoginTransaction(ctx context.Context, value LoginTransaction) error {
	record := oidcLoginRecord{CentralTicketCipher: value.CentralTicketCipher, ForceLogin: value.ForceLogin, StateHash: value.StateHash, TenantID: value.TenantID, NonceCipher: value.NonceCipher, CodeVerifierCipher: value.CodeVerifierCipher, ReturnPath: value.ReturnPath, ExpiresAt: value.ExpiresAt, ConsumedAt: value.ConsumedAt, CreatedAt: value.CreatedAt}
	return s.db.WithContext(ctx).Create(&record).Error
}

// ConsumeLoginTransaction 原子消费未过期的登录事务，拒绝重复回调。
// 参数：ctx为调用上下文与取消边界。
//
//	hash为随机Cookie或事务材料摘要。
//	now为本次服务端校验时间。
//
// 返回值：匹配的有效持久记录；不存在、失效或存储失败返回错误。
func (s *GORMOIDCStore) ConsumeLoginTransaction(ctx context.Context, hash []byte, now time.Time) (LoginTransaction, error) {
	var record oidcLoginRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_hash = ? AND consumed_at IS NULL AND expires_at > ?", hash, now).Take(&record).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errOIDCRecordNotFound
		}
		if err != nil {
			return err
		}
		result := tx.Model(&oidcLoginRecord{}).Where("state_hash = ? AND consumed_at IS NULL", hash).Update("consumed_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errOIDCRecordNotFound
		}
		record.ConsumedAt = &now
		return nil
	})
	if err != nil {
		return LoginTransaction{}, err
	}
	return LoginTransaction{CentralTicketCipher: record.CentralTicketCipher, ForceLogin: record.ForceLogin, StateHash: record.StateHash, TenantID: record.TenantID, NonceCipher: record.NonceCipher, CodeVerifierCipher: record.CodeVerifierCipher, ReturnPath: record.ReturnPath, ExpiresAt: record.ExpiresAt, ConsumedAt: record.ConsumedAt, CreatedAt: record.CreatedAt}, nil
}

func (s *GORMOIDCStore) CreateSession(ctx context.Context, value StoredOIDCSession) error {
	record := sessionRecord(value)
	return s.db.WithContext(ctx).Create(&record).Error
}

func (s *GORMOIDCStore) FindSession(ctx context.Context, hash []byte, now time.Time) (StoredOIDCSession, error) {
	var record oidcSessionRecord
	err := s.db.WithContext(ctx).Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", hash, now).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return StoredOIDCSession{}, errOIDCRecordNotFound
	}
	if err != nil {
		return StoredOIDCSession{}, err
	}
	return storedSession(record), nil
}

// TouchSessionLastSeen 只推进普通已认证请求的观测时间，避免每个业务请求重写令牌/主体密文。
// 参数：ctx为请求上下文，hash为会话Cookie哈希，now为服务端当前时间。
// 返回值：会话已撤销或绝对到期返回错误；同毫秒 no-op 写入通过复核区分，不误判登出。
func (s *GORMOIDCStore) TouchSessionLastSeen(ctx context.Context, hash []byte, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&oidcSessionRecord{}).
		Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", hash, now).
		Update("last_seen_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row oidcSessionRecord
	err := s.db.WithContext(ctx).Select("session_id_hash").
		Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", hash, now).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errOIDCRecordNotFound
	}
	return err
}

func (s *GORMOIDCStore) UpdateSession(ctx context.Context, value StoredOIDCSession, now time.Time) error {
	record := sessionRecord(value)
	updates := map[string]any{
		"principal_json": record.PrincipalJSON, "id_token_ciphertext": record.IDTokenCipher,
		"oauth_token_ciphertext": record.OAuthTokenCipher, "authorization_revision": record.AuthorizationRevision,
		"authorization_checked_at": record.AuthorizationCheckedAt, "refresh_retry_at": record.RefreshRetryAt,
		"token_expires_at": record.TokenExpiresAt, "last_seen_at": record.LastSeenAt,
		"oidc_subject": record.OIDCSubject, "oidc_session_id": record.OIDCSessionID,
	}
	result := s.db.WithContext(ctx).Model(&oidcSessionRecord{}).Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", value.SessionIDHash, now).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var active struct {
			SessionIDHash []byte `gorm:"column:session_id_hash"`
		}
		err := s.db.WithContext(ctx).Table((oidcSessionRecord{}).TableName()).Select("session_id_hash").Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", value.SessionIDHash, now).Take(&active).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errOIDCRecordNotFound
		}
		return err
	}
	return nil
}

// TouchSessionActivity 用条件更新推进显式交互时间：会话必须未撤销、未过绝对期限，
// 且空闲截止（COALESCE(last_interactive_at, created_at)）仍晚于 idleCutoff。
// 条件不满足返回 errOIDCRecordNotFound，已失效会话不会被写活。
func (s *GORMOIDCStore) TouchSessionActivity(ctx context.Context, hash []byte, now, idleCutoff time.Time) error {
	now = now.UTC()
	idleCutoff = idleCutoff.UTC()
	conditions := "session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ? AND COALESCE(last_interactive_at, created_at) > ?"
	result := s.db.WithContext(ctx).Model(&oidcSessionRecord{}).Where(conditions, hash, now, idleCutoff).
		Updates(map[string]any{"last_seen_at": now, "last_interactive_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// 与 UpdateSession 一致：同一毫秒内的重复活动可能不改变列值，通过存在性
		// 复核区分"无变化"与"会话已失效"。
		var active struct {
			SessionIDHash []byte `gorm:"column:session_id_hash"`
		}
		err := s.db.WithContext(ctx).Table((oidcSessionRecord{}).TableName()).Select("session_id_hash").Where(conditions, hash, now, idleCutoff).Take(&active).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errOIDCRecordNotFound
		}
		return err
	}
	return nil
}

func (s *GORMOIDCStore) RevokeSession(ctx context.Context, hash []byte, now time.Time) error {
	return s.db.WithContext(ctx).Model(&oidcSessionRecord{}).Where("session_id_hash = ? AND revoked_at IS NULL", hash).Update("revoked_at", now).Error
}

func (s *GORMOIDCStore) RevokeSessionsForIdentity(ctx context.Context, tenantID, identityID string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&oidcSessionRecord{}).
		Where("tenant_id = ? AND identity_id = ? AND revoked_at IS NULL", tenantID, identityID).
		Update("revoked_at", now).Error
}

// ConsumeBackchannelLogout 在同一事务中占用 JTI 并撤销命中的 RP 会话。
// 优先使用 sid 精确注销单个 OP 会话；仅当令牌无 sid 时才按标准 sub 撤销该主体全部会话。
func (s *GORMOIDCStore) ConsumeBackchannelLogout(ctx context.Context, value BackchannelLogout, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		replay := map[string]any{
			"jti": value.JTI, "issuer": value.Issuer, "audience": value.Audience,
			"expires_at": value.ExpiresAt.UTC(), "consumed_at": now.UTC(),
		}
		if err := tx.Table("pm_oidc_backchannel_logout_replay").Create(replay).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errOIDCBackchannelReplay
			}
			return err
		}
		query := tx.Model(&oidcSessionRecord{}).
			Where("tenant_id = ? AND revoked_at IS NULL", value.TenantID)
		if value.SessionID != "" {
			query = query.Where("oidc_session_id = ?", value.SessionID)
		} else {
			query = query.Where("oidc_subject = ?", value.Subject)
		}
		return query.Update("revoked_at", now.UTC()).Error
	})
}

// sessionRecord 将认证会话转换为完整数据库投影。
// 参数：value为待处理的本地持久记录。
// 返回值：包含原始认证时间和服务器端材料的持久投影。
func sessionRecord(value StoredOIDCSession) oidcSessionRecord {
	var retry, interactive *time.Time
	if !value.RefreshRetryAt.IsZero() {
		retryValue := value.RefreshRetryAt
		retry = &retryValue
	}
	if !value.LastInteractiveAt.IsZero() {
		interactiveValue := value.LastInteractiveAt
		interactive = &interactiveValue
	}
	return oidcSessionRecord{CentralBindingCipher: value.CentralBindingCipher, CentralSessionID: value.CentralSessionID, SessionIDHash: value.SessionIDHash, TenantID: value.TenantID, IdentityID: value.IdentityID, OIDCSubject: value.OIDCSubject, OIDCSessionID: value.OIDCSessionID, PersonID: value.PersonID, PrincipalJSON: value.PrincipalJSON, IDTokenCipher: value.IDTokenCipher, OAuthTokenCipher: value.OAuthTokenCipher, AuthorizationRevision: value.AuthorizationRevision, AuthorizationCheckedAt: value.AuthorizationCheckedAt, RefreshRetryAt: retry, TokenExpiresAt: value.TokenExpiresAt, SessionExpiresAt: value.SessionExpiresAt, CreatedAt: value.CreatedAt, LastSeenAt: value.LastSeenAt, LastInteractiveAt: interactive, RevokedAt: value.RevokedAt}
}

// storedSession 从数据库恢复加密令牌与中央绑定。
// 参数：record为数据库中的会话投影。
// 返回值：包含原始认证时间和服务器端材料的持久投影。
func storedSession(record oidcSessionRecord) StoredOIDCSession {
	var retry, interactive time.Time
	if record.RefreshRetryAt != nil {
		retry = *record.RefreshRetryAt
	}
	if record.LastInteractiveAt != nil {
		interactive = *record.LastInteractiveAt
	}
	return StoredOIDCSession{CentralBindingCipher: record.CentralBindingCipher, CentralSessionID: record.CentralSessionID, SessionIDHash: record.SessionIDHash, TenantID: record.TenantID, IdentityID: record.IdentityID, OIDCSubject: record.OIDCSubject, OIDCSessionID: record.OIDCSessionID, PersonID: record.PersonID, PrincipalJSON: record.PrincipalJSON, IDTokenCipher: record.IDTokenCipher, OAuthTokenCipher: record.OAuthTokenCipher, AuthorizationRevision: record.AuthorizationRevision, AuthorizationCheckedAt: record.AuthorizationCheckedAt, RefreshRetryAt: retry, TokenExpiresAt: record.TokenExpiresAt, SessionExpiresAt: record.SessionExpiresAt, CreatedAt: record.CreatedAt, LastSeenAt: record.LastSeenAt, LastInteractiveAt: interactive, RevokedAt: record.RevokedAt}
}

type secretCodec struct{ aead cipher.AEAD }

func newSecretCodec(key []byte) (*secretCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("OIDC session encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretCodec{aead: aead}, nil
}

func (c *secretCodec) encrypt(value []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, value, nil), nil
}

func (c *secretCodec) decrypt(value []byte) ([]byte, error) {
	if len(value) < c.aead.NonceSize() {
		return nil, errors.New("encrypted OIDC secret is malformed")
	}
	return c.aead.Open(nil, value[:c.aead.NonceSize()], value[c.aead.NonceSize():], nil)
}

func tokenDigest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
