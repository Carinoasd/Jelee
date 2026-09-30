package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const credentialColumns = `id::text,name,COALESCE(password_hash,''),auth_version,disabled,deleted_at IS NOT NULL,locked_until`
const sessionColumns = `id::text,user_id::text,client_kind,device_name,created_at,expires_at,revoked_at`

func scanCredentials(row pgx.Row) (domain.Credentials, error) {
	var c domain.Credentials
	err := row.Scan(&c.UserID, &c.Name, &c.PasswordHash, &c.Version, &c.Disabled, &c.Deleted, &c.LockedUntil)
	return c, storageError(err)
}

func (s *Store) Credentials(ctx context.Context, name string) (domain.Credentials, error) {
	if !validText(name, 128, false) {
		return domain.Credentials{}, domain.ErrUnauthenticated
	}
	c, err := scanCredentials(s.Pool.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE lower(name)=lower($1)`, name))
	return c, err
}

func (s *Store) CredentialsFor(ctx context.Context, actor domain.Actor, userID string) (domain.Credentials, error) {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.Credentials{}, err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.Credentials{}, domain.ErrForbidden
	}
	if !domain.ValidID(userID) {
		return domain.Credentials{}, domain.ErrNotFound
	}
	c, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid`, userID))
	if err != nil {
		return c, err
	}
	return c, storageError(tx.Commit(ctx))
}

func validTTL(ttl time.Duration) bool { return ttl >= time.Second && ttl <= 30*24*time.Hour }

func newSession(ctx context.Context, tx pgx.Tx, userID, kind, device string, ttl time.Duration) (domain.Session, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return domain.Session{}, "", domain.ErrDatabase
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	var session domain.Session
	err := tx.QueryRow(ctx, `INSERT INTO sessions(user_id,token_hash,client_kind,device_name,expires_at) VALUES($1::uuid,$2,$3,$4,now()+$5*interval '1 second') RETURNING `+sessionColumns, userID, hash[:], kind, device, int64(ttl/time.Second)).Scan(&session.ID, &session.UserID, &session.ClientKind, &session.DeviceName, &session.CreatedAt, &session.ExpiresAt, &session.RevokedAt)
	return session, token, storageError(err)
}

func (s *Store) CommitLogin(ctx context.Context, in domain.LoginInput) (domain.SessionGrant, error) {
	if !domain.ValidID(in.Credentials.UserID) {
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	if !validText(in.DeviceName, 128, true) || in.MaxSessions < 1 || in.MaxSessions > 100 || !validTTL(in.SessionTTL) || in.LockAfter < 1 || in.LockAfter > 100 || in.LockFor < time.Second || in.LockFor > 24*time.Hour {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	current, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, in.Credentials.UserID))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.SessionGrant{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if current.Disabled || current.Deleted || current.Version != in.Credentials.Version || current.PasswordHash != in.Credentials.PasswordHash || current.LockedUntil != nil && current.LockedUntil.After(now) {
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	actor := domain.Actor{UserID: current.UserID, IP: in.IP}
	if !in.PasswordOK || current.PasswordHash == "" {
		var failures int
		var locked *time.Time
		err = tx.QueryRow(ctx, `UPDATE users SET failed_login=CASE WHEN locked_until IS NOT NULL AND locked_until<=clock_timestamp() THEN 1 ELSE LEAST(failed_login,2147483646)+1 END, locked_until=CASE WHEN (CASE WHEN locked_until IS NOT NULL AND locked_until<=clock_timestamp() THEN 1 ELSE LEAST(failed_login,2147483646)+1 END)>=$2 THEN clock_timestamp()+$3*interval '1 second' ELSE NULL END WHERE id=$1::uuid RETURNING failed_login,locked_until`, current.UserID, in.LockAfter, int64(in.LockFor/time.Second)).Scan(&failures, &locked)
		if err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		if err = auditAccount(ctx, tx, actor, "login.failed", current.UserID, nil, map[string]any{"failedLogin": failures, "lockedUntil": locked}); err != nil {
			return domain.SessionGrant{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL AND expires_at>now()`, current.UserID).Scan(&count); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if count >= in.MaxSessions {
		return domain.SessionGrant{}, domain.ErrSessionLimit
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET failed_login=0,locked_until=NULL WHERE id=$1::uuid`, current.UserID); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	session, token, err := newSession(ctx, tx, current.UserID, "web", in.DeviceName, in.SessionTTL)
	if err != nil {
		return domain.SessionGrant{}, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid`, current.UserID))
	if err != nil {
		return domain.SessionGrant{}, err
	}
	if err = auditAccount(ctx, tx, actor, "session.created", session.ID, nil, session); err != nil {
		return domain.SessionGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	return domain.SessionGrant{User: u, Session: session, Token: token}, nil
}

func revokeAll(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1::uuid AND revoked_at IS NULL`, userID)
	return storageError(err)
}

func (s *Store) ReplacePassword(ctx context.Context, actor domain.Actor, userID string, expected domain.Credentials, newHash string) error {
	if !validPasswordHash(newHash) {
		return domain.ErrInvalid
	}
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.ErrForbidden
	}
	if !domain.ValidID(userID) {
		return domain.ErrNotFound
	}
	current, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, userID))
	if err != nil {
		return err
	}
	if current.Deleted {
		return domain.ErrNotFound
	}
	if expected.UserID != userID || current.PasswordHash != expected.PasswordHash || current.Version != expected.Version {
		return domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,auth_version=auth_version+1,failed_login=0,locked_until=NULL WHERE id=$1::uuid`, userID, newHash); err != nil {
		return storageError(err)
	}
	if err = revokeAll(ctx, tx, userID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.password_changed", userID, nil, map[string]bool{"passwordChanged": true, "sessionsRevoked": true}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// SetLocalPassword is a trusted database-operator recovery operation. It is not
// exposed through unauthenticated HTTP and never creates a session.
func (s *Store) SetLocalPassword(ctx context.Context, name, newHash string) (domain.User, error) {
	if !validText(name, 128, false) || !validPasswordHash(newHash) {
		return domain.User{}, domain.ErrInvalid
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(name)=lower($1) AND deleted_at IS NULL FOR UPDATE`, name))
	if err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,auth_version=auth_version+1,failed_login=0,locked_until=NULL WHERE id=$1::uuid`, u.ID, newHash); err != nil {
		return domain.User{}, storageError(err)
	}
	if err = revokeAll(ctx, tx, u.ID); err != nil {
		return domain.User{}, err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "user.password_reset_local", u.ID, nil, map[string]bool{"passwordChanged": true, "sessionsRevoked": true}); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

func (s *Store) ListSessions(ctx context.Context, actor domain.Actor, userID string) ([]domain.Session, error) {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return nil, domain.ErrForbidden
	}
	if _, err = userInTransaction(ctx, tx, userID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL AND expires_at>now() ORDER BY created_at,id LIMIT 1001`, userID)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]domain.Session, 0)
	for rows.Next() {
		var item domain.Session
		if err = rows.Scan(&item.ID, &item.UserID, &item.ClientKind, &item.DeviceName, &item.CreatedAt, &item.ExpiresAt, &item.RevokedAt); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		result = append(result, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	if len(result) > 1000 {
		return nil, domain.ErrConflict
	}
	return result, storageError(tx.Commit(ctx))
}

func (s *Store) RevokeSession(ctx context.Context, actor domain.Actor, userID, sessionID string) error {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.ErrForbidden
	}
	if !domain.ValidID(userID) || !domain.ValidID(sessionID) {
		return domain.ErrNotFound
	}
	var revoked *time.Time
	if err = tx.QueryRow(ctx, `SELECT revoked_at FROM sessions WHERE id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, sessionID, userID).Scan(&revoked); err != nil {
		return storageError(err)
	}
	if revoked != nil {
		return storageError(tx.Commit(ctx))
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, sessionID); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "session.revoked", sessionID, nil, map[string]bool{"revoked": true}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) RevokeSessions(ctx context.Context, actor domain.Actor, userID string) error {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.ErrForbidden
	}
	if _, err = userInTransaction(ctx, tx, userID); err != nil {
		return err
	}
	if err = revokeAll(ctx, tx, userID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "sessions.revoked", userID, nil, map[string]bool{"allSessionsRevoked": true}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) RotateSession(ctx context.Context, actor domain.Actor, device string, ttl time.Duration) (domain.SessionGrant, error) {
	if !validText(device, 128, true) || !validTTL(ttl) {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.SessionGrant{}, err
	}
	defer tx.Rollback(ctx)
	var kind, oldDevice string
	if err = tx.QueryRow(ctx, `SELECT client_kind,device_name FROM sessions WHERE id=$1::uuid`, actor.SessionID).Scan(&kind, &oldDevice); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if device == "" {
		device = oldDevice
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, actor.SessionID); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	session, token, err := newSession(ctx, tx, actor.UserID, kind, device, ttl)
	if err != nil {
		return domain.SessionGrant{}, err
	}
	u, err := userInTransaction(ctx, tx, actor.UserID)
	if err != nil {
		return domain.SessionGrant{}, err
	}
	if err = auditAccount(ctx, tx, actor, "session.rotated", session.ID, map[string]string{"sessionId": actor.SessionID}, session); err != nil {
		return domain.SessionGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	return domain.SessionGrant{User: u, Session: session, Token: token}, nil
}
