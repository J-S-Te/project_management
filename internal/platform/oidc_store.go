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

type LoginTransaction struct {
	StateHash          []byte
	TenantID           string
	NonceCipher        []byte
	CodeVerifierCipher []byte
	ReturnPath         string
	ExpiresAt          time.Time
	ConsumedAt         *time.Time
	CreatedAt          time.Time
}

type StoredOIDCSession struct {
	SessionIDHash          []byte
	TenantID               string
	IdentityID             string
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
	RevokedAt              *time.Time
}

type OIDCStore interface {
	SaveLoginTransaction(context.Context, LoginTransaction) error
	ConsumeLoginTransaction(context.Context, []byte, time.Time) (LoginTransaction, error)
	CreateSession(context.Context, StoredOIDCSession) error
	FindSession(context.Context, []byte, time.Time) (StoredOIDCSession, error)
	UpdateSession(context.Context, StoredOIDCSession, time.Time) error
	RevokeSession(context.Context, []byte, time.Time) error
	RevokeSessionsForIdentity(context.Context, string, string, time.Time) error
}

type oidcLoginRecord struct {
	StateHash          []byte `gorm:"column:state_hash;primaryKey;type:binary(32)"`
	TenantID           string `gorm:"column:tenant_id;size:64;not null;index"`
	NonceCipher        []byte `gorm:"column:nonce_ciphertext;type:varbinary(512);not null"`
	CodeVerifierCipher []byte `gorm:"column:code_verifier_ciphertext;type:varbinary(1024);not null"`
	ReturnPath         string `gorm:"column:return_path;size:512;not null"`
	ExpiresAt          time.Time
	ConsumedAt         *time.Time
	CreatedAt          time.Time
}

func (oidcLoginRecord) TableName() string { return "pm_oidc_login_transaction" }

type oidcSessionRecord struct {
	SessionIDHash          []byte `gorm:"column:session_id_hash;primaryKey;type:binary(32)"`
	TenantID               string `gorm:"column:tenant_id;size:64;not null;index"`
	IdentityID             string `gorm:"column:identity_id;size:128;not null;index"`
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
	RevokedAt              *time.Time
}

func (oidcSessionRecord) TableName() string { return "pm_oidc_session" }

type GORMOIDCStore struct{ db *gorm.DB }

func NewGORMOIDCStore(db *gorm.DB) *GORMOIDCStore { return &GORMOIDCStore{db: db} }

func (s *GORMOIDCStore) SaveLoginTransaction(ctx context.Context, value LoginTransaction) error {
	record := oidcLoginRecord{StateHash: value.StateHash, TenantID: value.TenantID, NonceCipher: value.NonceCipher, CodeVerifierCipher: value.CodeVerifierCipher, ReturnPath: value.ReturnPath, ExpiresAt: value.ExpiresAt, ConsumedAt: value.ConsumedAt, CreatedAt: value.CreatedAt}
	return s.db.WithContext(ctx).Create(&record).Error
}

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
	return LoginTransaction{StateHash: record.StateHash, TenantID: record.TenantID, NonceCipher: record.NonceCipher, CodeVerifierCipher: record.CodeVerifierCipher, ReturnPath: record.ReturnPath, ExpiresAt: record.ExpiresAt, ConsumedAt: record.ConsumedAt, CreatedAt: record.CreatedAt}, nil
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

func (s *GORMOIDCStore) UpdateSession(ctx context.Context, value StoredOIDCSession, now time.Time) error {
	record := sessionRecord(value)
	updates := map[string]any{
		"principal_json": record.PrincipalJSON, "id_token_ciphertext": record.IDTokenCipher,
		"oauth_token_ciphertext": record.OAuthTokenCipher, "authorization_revision": record.AuthorizationRevision,
		"authorization_checked_at": record.AuthorizationCheckedAt, "refresh_retry_at": record.RefreshRetryAt,
		"token_expires_at": record.TokenExpiresAt, "last_seen_at": record.LastSeenAt,
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

func (s *GORMOIDCStore) RevokeSession(ctx context.Context, hash []byte, now time.Time) error {
	return s.db.WithContext(ctx).Model(&oidcSessionRecord{}).Where("session_id_hash = ? AND revoked_at IS NULL", hash).Update("revoked_at", now).Error
}

func (s *GORMOIDCStore) RevokeSessionsForIdentity(ctx context.Context, tenantID, identityID string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&oidcSessionRecord{}).
		Where("tenant_id = ? AND identity_id = ? AND revoked_at IS NULL", tenantID, identityID).
		Update("revoked_at", now).Error
}

func sessionRecord(value StoredOIDCSession) oidcSessionRecord {
	var retry *time.Time
	if !value.RefreshRetryAt.IsZero() {
		retryValue := value.RefreshRetryAt
		retry = &retryValue
	}
	return oidcSessionRecord{SessionIDHash: value.SessionIDHash, TenantID: value.TenantID, IdentityID: value.IdentityID, PersonID: value.PersonID, PrincipalJSON: value.PrincipalJSON, IDTokenCipher: value.IDTokenCipher, OAuthTokenCipher: value.OAuthTokenCipher, AuthorizationRevision: value.AuthorizationRevision, AuthorizationCheckedAt: value.AuthorizationCheckedAt, RefreshRetryAt: retry, TokenExpiresAt: value.TokenExpiresAt, SessionExpiresAt: value.SessionExpiresAt, CreatedAt: value.CreatedAt, LastSeenAt: value.LastSeenAt, RevokedAt: value.RevokedAt}
}

func storedSession(record oidcSessionRecord) StoredOIDCSession {
	var retry time.Time
	if record.RefreshRetryAt != nil {
		retry = *record.RefreshRetryAt
	}
	return StoredOIDCSession{SessionIDHash: record.SessionIDHash, TenantID: record.TenantID, IdentityID: record.IdentityID, PersonID: record.PersonID, PrincipalJSON: record.PrincipalJSON, IDTokenCipher: record.IDTokenCipher, OAuthTokenCipher: record.OAuthTokenCipher, AuthorizationRevision: record.AuthorizationRevision, AuthorizationCheckedAt: record.AuthorizationCheckedAt, RefreshRetryAt: retry, TokenExpiresAt: record.TokenExpiresAt, SessionExpiresAt: record.SessionExpiresAt, CreatedAt: record.CreatedAt, LastSeenAt: record.LastSeenAt, RevokedAt: record.RevokedAt}
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
