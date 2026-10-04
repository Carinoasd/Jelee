package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/netip"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const credentialColumns = `id::text,name,COALESCE(password_hash,''),auth_version,disabled,deleted_at IS NOT NULL,locked_until` //nolint:gosec // G101: SQL column list, not a credential
const sessionColumns = `id::text,user_id::text,client_kind,device_name,COALESCE(client_name,''),COALESCE(device_id,''),COALESCE(client_version,''),created_at,expires_at,last_seen_at,COALESCE(last_ip,''),revoked_at`

func scanSession(row pgx.Row) (domain.Session, error) {
	var session domain.Session
	err := row.Scan(&session.ID, &session.UserID, &session.ClientKind, &session.DeviceName, &session.Client, &session.DeviceID, &session.Version, &session.CreatedAt, &session.ExpiresAt, &session.LastSeenAt, &session.LastIP, &session.RevokedAt)
	return session, storageError(err)
}

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

// validNativeClient mirrors the application check so the store never writes
// an unbounded or control-character label even when called directly.
func validNativeClient(c domain.NativeClient) bool {
	return validText(c.Name, domain.NativeClientNameMax, false) && validText(c.DeviceID, domain.NativeDeviceIDMax, false) &&
		validText(c.Device, domain.NativeDeviceNameMax, true) && validText(c.Version, domain.NativeClientVersionMax, true)
}

// newSession stores the client labels as NULL when absent. Web sessions carry
// none of them.
func newSession(ctx context.Context, tx pgx.Tx, userID, kind, device string, client domain.NativeClient, ttl time.Duration) (domain.Session, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return domain.Session{}, "", domain.ErrDatabase
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	session, err := scanSession(tx.QueryRow(ctx, `INSERT INTO sessions(user_id,token_hash,client_kind,device_name,client_name,device_id,client_version,expires_at) VALUES($1::uuid,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),now()+$8*interval '1 second') RETURNING `+sessionColumns, userID, hash[:], kind, device, client.Name, client.DeviceID, client.Version, int64(ttl/time.Second)))
	return session, token, err
}

func (s *Store) CommitLogin(ctx context.Context, in domain.LoginInput) (domain.SessionGrant, error) {
	if !domain.ValidID(in.Credentials.UserID) {
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	if in.Native && !validNativeClient(in.Client) || !in.Native && in.Client != (domain.NativeClient{}) {
		return domain.SessionGrant{}, domain.ErrInvalid
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
		user := domain.WebhookSubject{Kind: domain.WebhookSubjectUser, ID: current.UserID}
		if err = appendWebhook(ctx, tx, s.webhooksOn(), domain.WebhookUserLoginFailed, now, user, map[string]any{"failedLogins": failures}); err != nil {
			return domain.SessionGrant{}, err
		}
		// A locked account refuses logins before counting them, so a lock
		// is reported exactly once, by the failure that set it.
		if locked != nil {
			if err = appendWebhook(ctx, tx, s.webhooksOn(), domain.WebhookUserLocked, now, user, map[string]any{"failedLogins": failures, "lockedUntil": locked.UTC().Format(time.RFC3339)}); err != nil {
				return domain.SessionGrant{}, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	kind := string(access.ClientWeb)
	if in.Native {
		// Checked only after the password matched, so the answer never tells an
		// unauthenticated caller whether an account allows native devices.
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT allow_native FROM users WHERE id=$1::uuid`, current.UserID).Scan(&allowed); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		if !allowed {
			if err = auditAccount(ctx, tx, actor, "login.native_denied", current.UserID, nil, map[string]string{"client": in.Client.Name, "deviceId": in.Client.DeviceID, "version": in.Client.Version, "deviceName": in.DeviceName}); err != nil {
				return domain.SessionGrant{}, err
			}
			if err = tx.Commit(ctx); err != nil {
				return domain.SessionGrant{}, storageError(err)
			}
			return domain.SessionGrant{}, domain.ErrNativeLoginDisabled
		}
		kind = string(access.ClientNative)
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
	session, token, err := newSession(ctx, tx, current.UserID, kind, in.DeviceName, in.Client, in.SessionTTL)
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
	if err = appendWebhook(ctx, tx, s.webhooksOn(), domain.WebhookUserLogin, now, domain.WebhookSubject{Kind: domain.WebhookSubjectUser, ID: current.UserID},
		map[string]any{"clientKind": kind}); err != nil {
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
	result, err := collectSessions(rows)
	if err != nil {
		return nil, err
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
	old, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1::uuid`, actor.SessionID))
	if err != nil {
		return domain.SessionGrant{}, err
	}
	if device == "" {
		device = old.DeviceName
	}
	// The replacement keeps the reported client identity; only the device
	// label may change on rotation.
	client := domain.NativeClient{Name: old.Client, Device: device, DeviceID: old.DeviceID, Version: old.Version}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, actor.SessionID); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	session, token, err := newSession(ctx, tx, actor.UserID, old.ClientKind, device, client, ttl)
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

func collectSessions(rows pgx.Rows) ([]domain.Session, error) {
	defer rows.Close()
	result := make([]domain.Session, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}

// ListAllSessions is the administrator view of every active session, paged
// by session ID so a large installation never needs an unbounded result.
func (s *Store) ListAllSessions(ctx context.Context, actor domain.Actor, cursor string, limit int) ([]domain.Session, error) {
	if cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE revoked_at IS NULL AND expires_at>now() AND id>COALESCE(NULLIF($1,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	result, err := collectSessions(rows)
	if err != nil {
		return nil, err
	}
	return result, storageError(tx.Commit(ctx))
}

// SetNativeAccess changes whether password login may issue native sessions
// to a user. Withdrawing the right also revokes the user's active native
// sessions, including CLI-provisioned ones, so the change takes effect at
// once instead of when the tokens expire. An unchanged value is a no-op.
func (s *Store) SetNativeAccess(ctx context.Context, actor domain.Actor, userID string, allow bool) (domain.User, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	old, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return old, err
	}
	if old.DeletedAt != nil {
		return domain.User{}, domain.ErrNotFound
	}
	if old.AllowNative == allow {
		return old, storageError(tx.Commit(ctx))
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET allow_native=$2 WHERE id=$1::uuid RETURNING `+userColumns, userID, allow))
	if err != nil {
		return u, err
	}
	var revoked int64
	if !allow {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1::uuid AND client_kind='native' AND revoked_at IS NULL`, userID)
		if err != nil {
			return domain.User{}, storageError(err)
		}
		revoked = tag.RowsAffected()
	}
	if err = auditAccount(ctx, tx, actor, "user.native_access_changed", userID, map[string]bool{"allowNative": old.AllowNative}, map[string]any{"allowNative": u.AllowNative, "nativeSessionsRevoked": revoked}); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

// sessionTouchInterval throttles last-use bookkeeping: a session row is
// written at most once per interval however many requests it authenticates.
const sessionTouchInterval = 60 * time.Second

// AuthenticateFrom authenticates like Authenticate and records the session's
// last use and client address, at most once per sessionTouchInterval. The
// bookkeeping is best effort: failing to record it never fails the request.
func (s *Store) AuthenticateFrom(ctx context.Context, token, ip string) (access.Principal, error) {
	p, _, _, err := s.AuthenticateClient(ctx, token, ip)
	return p, err
}

// AuthenticateClient is AuthenticateFrom that also returns the client labels
// recorded with the session and the current client control version (G47),
// read by the same statement so the request gate costs no extra round trip.
func (s *Store) AuthenticateClient(ctx context.Context, token, ip string) (access.Principal, access.SessionClient, int64, error) {
	a, err := s.authenticate(ctx, token)
	if err != nil || !a.stale {
		return a.principal, a.client, a.version, err
	}
	p := a.principal
	var address *string
	if parsed, perr := netip.ParseAddr(ip); perr == nil {
		normalized := parsed.Unmap().WithZone("").String()
		address = &normalized
	}
	// The repeated staleness test makes concurrent requests write once, and
	// SKIP LOCKED keeps a request from waiting behind an account transaction
	// that holds the session row; that use is recorded by a later request.
	_, _ = s.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=clock_timestamp(),last_ip=COALESCE($2,last_ip) WHERE id=(SELECT id FROM sessions WHERE id=$1::uuid AND (last_seen_at IS NULL OR last_seen_at<=clock_timestamp()-$3*interval '1 second') FOR UPDATE SKIP LOCKED)`, p.SessionID, address, int64(sessionTouchInterval/time.Second))
	return p, a.client, a.version, nil
}
