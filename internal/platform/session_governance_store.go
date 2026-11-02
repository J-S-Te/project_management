package platform

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// AttachSessionLink 将票据绑定原始state，重复桥接不覆盖已经保存的票据。
// 参数：ctx为请求，hash为状态摘要，cipher为票据密文，now为服务端时钟。
// 返回值：仍未消费的原登录事务；过期、重放或数据库故障时返回错误。
func (s *GORMOIDCStore) AttachSessionLink(ctx context.Context, hash, cipher []byte, now time.Time) (LoginTransaction, error) {
	var row oidcLoginRecord
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("state_hash = ? AND consumed_at IS NULL AND expires_at > ?", hash, now).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		if len(row.CentralTicketCipher) > 0 {
			return ErrUnauthenticated
		}
		return tx.Model(&oidcLoginRecord{}).Where("state_hash = ?", hash).Update("central_ticket_cipher", cipher).Error
	})
	if err != nil {
		return LoginTransaction{}, err
	}
	return LoginTransaction{StateHash: row.StateHash, TenantID: row.TenantID, NonceCipher: row.NonceCipher,
		CodeVerifierCipher: row.CodeVerifierCipher, ReturnPath: row.ReturnPath, ExpiresAt: row.ExpiresAt,
		CentralTicketCipher: cipher, ForceLogin: row.ForceLogin}, nil
}

// WithSessionLock 对同一Cookie的令牌轮换加数据库行锁，普通读取不覆盖轮换结果。
// 参数：ctx为请求，hash为Cookie摘要，now为时钟，update为有界的刷新与快照核验。
// 返回值：最新持久会话；明确失效提交当前行撤销，暂时失败回滚并返回依赖错误。
func (s *GORMOIDCStore) WithSessionLock(ctx context.Context, hash []byte, now time.Time, update sessionUpdateFunc) (StoredOIDCSession, error) {
	var result StoredOIDCSession
	var rejection error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row oidcSessionRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id_hash = ? AND revoked_at IS NULL AND session_expires_at > ?", hash, now).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		result = storedSession(row)
		updated, changed, err := update(result)
		if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrInvalidAuthorization) {
			rejection = ErrUnauthenticated
			return tx.Model(&oidcSessionRecord{}).Where("session_id_hash = ?", hash).Update("revoked_at", now).Error
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		result = updated
		return tx.Model(&oidcSessionRecord{}).Where("session_id_hash = ?", hash).Updates(map[string]any{
			"principal_json": updated.PrincipalJSON, "id_token_ciphertext": updated.IDTokenCipher,
			"oauth_token_ciphertext": updated.OAuthTokenCipher, "authorization_revision": updated.AuthorizationRevision,
			"authorization_checked_at": updated.AuthorizationCheckedAt, "refresh_retry_at": nil,
			"token_expires_at": updated.TokenExpiresAt, "last_seen_at": now,
			"oidc_subject": updated.OIDCSubject, "oidc_session_id": updated.OIDCSessionID,
		}).Error
	})
	if err != nil {
		return StoredOIDCSession{}, err
	}
	if rejection != nil {
		return StoredOIDCSession{}, rejection
	}
	return result, nil
}
