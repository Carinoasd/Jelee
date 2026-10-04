package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Second factor and application passwords (G07.8). Every self-service
// change needs a web session: a native session, which an application
// password can obtain, must never manage the factor that application
// passwords exist to bypass.

// requireWebSession refuses an actor whose session is not a web session. The
// caller has already locked and validated the session row.
func requireWebSession(ctx context.Context, tx pgx.Tx, actor domain.Actor) error {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT client_kind FROM sessions WHERE id=$1::uuid`, actor.SessionID).Scan(&kind); err != nil {
		return storageError(err)
	}
	if kind != string(access.ClientWeb) {
		return domain.ErrForbidden
	}
	return nil
}

// selfWebTransaction opens an account transaction for the actor acting on
// their own account through a web session.
func (s *Store) selfWebTransaction(ctx context.Context, actor domain.Actor) (pgx.Tx, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	if err = requireWebSession(ctx, tx, actor); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func validDigest(digest []byte) bool { return len(digest) == sha256.Size }

func validRecoveryDigests(digests [][]byte) bool {
	if len(digests) != domain.RecoveryCodeCount {
		return false
	}
	seen := map[string]bool{}
	for _, d := range digests {
		if !validDigest(d) || seen[string(d)] {
			return false
		}
		seen[string(d)] = true
	}
	return true
}

func replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, userID string, digests [][]byte) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_recovery_codes WHERE user_id=$1::uuid`, userID); err != nil {
		return storageError(err)
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_recovery_codes(user_id,code_digest) SELECT $1::uuid,d FROM unnest($2::bytea[]) d`, userID, digests)
	return storageError(err)
}

// TwoFactorStatus reads a user's second factor state for the user or an
// administrator. Available is left to the caller, which owns the key.
func (s *Store) TwoFactorStatus(ctx context.Context, actor domain.Actor, userID string) (domain.TwoFactorStatus, error) {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.TwoFactorStatus{}, err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.TwoFactorStatus{}, domain.ErrForbidden
	}
	if _, err = userInTransaction(ctx, tx, userID); err != nil {
		return domain.TwoFactorStatus{}, err
	}
	var status domain.TwoFactorStatus
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id=$1::uuid),(SELECT enabled_at FROM user_totp WHERE user_id=$1::uuid),
 (SELECT count(*) FROM user_recovery_codes WHERE user_id=$1::uuid AND used_at IS NULL)`, userID).Scan(&exists, &status.EnabledAt, &status.RecoveryCodesRemaining)
	if err != nil {
		return domain.TwoFactorStatus{}, storageError(err)
	}
	status.Enabled = status.EnabledAt != nil
	status.Pending = exists && !status.Enabled
	if !status.Enabled {
		status.RecoveryCodesRemaining = 0
	}
	return status, storageError(tx.Commit(ctx))
}

// BeginTOTP stores a pending authenticator secret, replacing an earlier
// pending one, and returns the account name for the otpauth label. An
// enabled factor must be disabled first.
func (s *Store) BeginTOTP(ctx context.Context, actor domain.Actor, sealed []byte) (string, error) {
	if len(sealed) < 40 || len(sealed) > 256 {
		return "", domain.ErrInvalid
	}
	tx, err := s.selfWebTransaction(ctx, actor)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	u, err := userInTransaction(ctx, tx, actor.UserID)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO user_totp(user_id,secret_sealed) VALUES($1::uuid,$2)
 ON CONFLICT(user_id) DO UPDATE SET secret_sealed=EXCLUDED.secret_sealed,created_at=now(),last_step=0 WHERE user_totp.enabled_at IS NULL`, actor.UserID, sealed)
	if err != nil {
		return "", storageError(err)
	}
	if tag.RowsAffected() == 0 {
		return "", domain.ErrConflict
	}
	return u.Name, storageError(tx.Commit(ctx))
}

// verifyTOTP runs verify against the user's locked secret and records the
// accepted step. enabled selects a confirmed factor or a pending one.
func verifyTOTP(ctx context.Context, tx pgx.Tx, userID string, enabled bool, verify domain.TOTPVerifier) error {
	if verify == nil {
		return domain.ErrInvalid
	}
	var sealed []byte
	var lastStep int64
	var enabledAt *time.Time
	err := tx.QueryRow(ctx, `SELECT secret_sealed,last_step,enabled_at FROM user_totp WHERE user_id=$1::uuid FOR UPDATE`, userID).Scan(&sealed, &lastStep, &enabledAt)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (enabledAt != nil) != enabled {
		return domain.ErrConflict
	}
	if err != nil {
		return storageError(err)
	}
	step, err := verify(userID, sealed, lastStep)
	if err != nil {
		return err
	}
	if step <= lastStep {
		return domain.ErrSecondFactorMismatch
	}
	_, err = tx.Exec(ctx, `UPDATE user_totp SET last_step=$2 WHERE user_id=$1::uuid`, userID, step)
	return storageError(err)
}

// ConfirmTOTP enables a pending factor once a code verifies, stores the
// recovery code digests and revokes the user's other sessions: they were
// established with the password alone.
func (s *Store) ConfirmTOTP(ctx context.Context, actor domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error {
	if !validRecoveryDigests(recovery) {
		return domain.ErrInvalid
	}
	tx, err := s.selfWebTransaction(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = verifyTOTP(ctx, tx, actor.UserID, false, verify); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE user_totp SET enabled_at=now() WHERE user_id=$1::uuid`, actor.UserID); err != nil {
		return storageError(err)
	}
	if err = replaceRecoveryCodes(ctx, tx, actor.UserID, recovery); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1::uuid AND id<>$2::uuid AND revoked_at IS NULL`, actor.UserID, actor.SessionID)
	if err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM login_challenges WHERE user_id=$1::uuid`, actor.UserID); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "user.two_factor_enabled", actor.UserID, map[string]bool{"enabled": false},
		map[string]any{"enabled": true, "recoveryCodes": len(recovery), "otherSessionsRevoked": tag.RowsAffected()}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// RegenerateRecoveryCodes replaces every recovery code after a current
// authenticator code verifies.
func (s *Store) RegenerateRecoveryCodes(ctx context.Context, actor domain.Actor, verify domain.TOTPVerifier, recovery [][]byte) error {
	if !validRecoveryDigests(recovery) {
		return domain.ErrInvalid
	}
	tx, err := s.selfWebTransaction(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = verifyTOTP(ctx, tx, actor.UserID, true, verify); err != nil {
		return err
	}
	if err = replaceRecoveryCodes(ctx, tx, actor.UserID, recovery); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.recovery_codes_regenerated", actor.UserID, nil, map[string]int{"recoveryCodes": len(recovery)}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// spendRecoveryCode marks a recovery code used; ErrSecondFactorMismatch when
// it is unknown or already spent.
func spendRecoveryCode(ctx context.Context, tx pgx.Tx, userID string, digest []byte) error {
	tag, err := tx.Exec(ctx, `UPDATE user_recovery_codes SET used_at=now() WHERE user_id=$1::uuid AND code_digest=$2 AND used_at IS NULL`, userID, digest)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrSecondFactorMismatch
	}
	return nil
}

func clearTwoFactor(ctx context.Context, tx pgx.Tx, userID string) error {
	for _, statement := range []string{
		`DELETE FROM user_recovery_codes WHERE user_id=$1::uuid`,
		`DELETE FROM login_challenges WHERE user_id=$1::uuid`,
		`DELETE FROM user_totp WHERE user_id=$1::uuid`,
	} {
		if _, err := tx.Exec(ctx, statement, userID); err != nil {
			return storageError(err)
		}
	}
	return nil
}

// DisableTOTP removes the user's factor after the caller verified the
// password (expected is the credential snapshot it verified) and a code or
// recovery code verifies here. Application passwords stay; the user revokes
// them separately.
func (s *Store) DisableTOTP(ctx context.Context, actor domain.Actor, expected domain.Credentials, verify domain.TOTPVerifier, recovery []byte) error {
	if verify == nil == (recovery == nil) || recovery != nil && !validDigest(recovery) {
		return domain.ErrInvalid
	}
	tx, err := s.selfWebTransaction(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, actor.UserID))
	if err != nil {
		return err
	}
	if expected.UserID != actor.UserID || current.PasswordHash != expected.PasswordHash || current.Version != expected.Version {
		return domain.ErrConflict
	}
	method := "totp"
	if recovery != nil {
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id=$1::uuid AND enabled_at IS NOT NULL)`, actor.UserID).Scan(&enabled); err != nil {
			return storageError(err)
		}
		if !enabled {
			return domain.ErrConflict
		}
		method = "recovery_code"
		err = spendRecoveryCode(ctx, tx, actor.UserID, recovery)
	} else {
		err = verifyTOTP(ctx, tx, actor.UserID, true, verify)
	}
	if err != nil {
		return err
	}
	if err = clearTwoFactor(ctx, tx, actor.UserID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.two_factor_disabled", actor.UserID, map[string]bool{"enabled": true}, map[string]any{"enabled": false, "method": method}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// ResetTwoFactor is the administrator reset for a user who lost the
// authenticator and the recovery codes. It needs a web session too, so an
// administrator's application password cannot remove the administrator's
// own factor. Nothing to reset is a no-op without audit.
func (s *Store) ResetTwoFactor(ctx context.Context, actor domain.Actor, userID string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = requireWebSession(ctx, tx, actor); err != nil {
		return err
	}
	if _, err = userInTransaction(ctx, tx, userID); err != nil {
		return err
	}
	var enabledAt *time.Time
	err = tx.QueryRow(ctx, `SELECT enabled_at FROM user_totp WHERE user_id=$1::uuid FOR UPDATE`, userID).Scan(&enabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storageError(tx.Commit(ctx))
	}
	if err != nil {
		return storageError(err)
	}
	if err = clearTwoFactor(ctx, tx, userID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.two_factor_reset", userID, map[string]bool{"enabled": enabledAt != nil, "pending": enabledAt == nil}, map[string]bool{"enabled": false}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// ResetLocalTwoFactor is the trusted database-operator recovery for an
// account, such as the only administrator, that cannot complete a web login
// any more. Like SetLocalPassword it is not reachable over HTTP.
func (s *Store) ResetLocalTwoFactor(ctx context.Context, name string) (domain.User, bool, error) {
	if !validText(name, 128, false) {
		return domain.User{}, false, domain.ErrInvalid
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.User{}, false, err
	}
	defer tx.Rollback(ctx)
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE lower(name)=lower($1) AND deleted_at IS NULL FOR UPDATE`, name))
	if err != nil {
		return u, false, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM user_totp WHERE user_id=$1::uuid`, u.ID)
	if err != nil {
		return domain.User{}, false, storageError(err)
	}
	if tag.RowsAffected() == 0 {
		return u, false, storageError(tx.Commit(ctx))
	}
	if err = clearTwoFactor(ctx, tx, u.ID); err != nil {
		return domain.User{}, false, err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "user.two_factor_reset", u.ID, nil, map[string]any{"enabled": false, "local": true}); err != nil {
		return domain.User{}, false, err
	}
	return u, true, storageError(tx.Commit(ctx))
}

// issueLoginChallenge stores a second step for a web login whose password
// verified. A user keeps at most a few live challenges; older and spent
// ones are removed.
func issueLoginChallenge(ctx context.Context, tx pgx.Tx, user domain.Credentials, device string, now time.Time) (domain.LoginChallenge, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return domain.LoginChallenge{}, domain.ErrDatabase
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(token))
	if _, err := tx.Exec(ctx, `DELETE FROM login_challenges WHERE user_id=$1::uuid AND (used_at IS NOT NULL OR expires_at<=$2 OR id NOT IN (SELECT id FROM login_challenges WHERE user_id=$1::uuid AND used_at IS NULL AND expires_at>$2 ORDER BY created_at DESC,id LIMIT 4))`, user.UserID, now); err != nil {
		return domain.LoginChallenge{}, storageError(err)
	}
	expires := now.Add(domain.ChallengeTTL)
	if _, err := tx.Exec(ctx, `INSERT INTO login_challenges(user_id,token_digest,auth_version,device_name,created_at,expires_at) VALUES($1::uuid,$2,$3,$4,$5,$6)`, user.UserID, digest[:], user.Version, device, now, expires); err != nil {
		return domain.LoginChallenge{}, storageError(err)
	}
	return domain.LoginChallenge{Token: token, ExpiresAt: expires}, nil
}

// CommitSecondFactor completes a web login challenge. A wrong code counts
// against the challenge and against the account's shared failure counter
// and lock; the first correct code spends the challenge and issues the
// session.
func (s *Store) CommitSecondFactor(ctx context.Context, in domain.SecondFactorInput) (domain.SessionGrant, error) {
	if !validDigest(in.ChallengeDigest) || in.Verify == nil == (in.RecoveryDigest == nil) ||
		in.MaxSessions < 1 || in.MaxSessions > 100 || !validTTL(in.SessionTTL) || in.LockAfter < 1 || in.LockAfter > 100 || in.LockFor < time.Second || in.LockFor > 24*time.Hour || len(in.IP) > 45 {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	var challengeID, userID, device string
	var version int64
	var expires time.Time
	var used *time.Time
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,auth_version,device_name,expires_at,used_at,attempts FROM login_challenges WHERE token_digest=$1 FOR UPDATE`, in.ChallengeDigest).
		Scan(&challengeID, &userID, &version, &device, &expires, &used, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionGrant{}, domain.ErrChallengeInvalid
	}
	if err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	current, err := scanCredentials(tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, userID))
	if err != nil {
		return domain.SessionGrant{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if used != nil || !expires.After(now) || attempts >= domain.ChallengeAttempts || current.Version != version {
		return domain.SessionGrant{}, domain.ErrChallengeInvalid
	}
	if current.Disabled || current.Deleted || current.PasswordHash == "" || current.LockedUntil != nil && current.LockedUntil.After(now) {
		return domain.SessionGrant{}, domain.ErrUnauthenticated
	}
	actor := domain.Actor{UserID: userID, IP: in.IP}
	method := "totp"
	if in.RecoveryDigest != nil {
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id=$1::uuid AND enabled_at IS NOT NULL)`, userID).Scan(&enabled); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		if !enabled {
			return domain.SessionGrant{}, domain.ErrChallengeInvalid
		}
		method = "recovery_code"
		if digest := in.RecoveryDigest(userID); validDigest(digest) {
			err = spendRecoveryCode(ctx, tx, userID, digest)
		} else {
			err = domain.ErrSecondFactorMismatch
		}
	} else {
		err = verifyTOTP(ctx, tx, userID, true, in.Verify)
	}
	if errors.Is(err, domain.ErrConflict) {
		// The factor was reset or disabled after the challenge was issued.
		return domain.SessionGrant{}, domain.ErrChallengeInvalid
	}
	if errors.Is(err, domain.ErrSecondFactorMismatch) {
		if _, err = tx.Exec(ctx, `UPDATE login_challenges SET attempts=attempts+1 WHERE id=$1::uuid`, challengeID); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		if err = s.recordLoginFailure(ctx, tx, userID, actor, now, in.LockAfter, in.LockFor, "login.second_factor_failed"); err != nil {
			return domain.SessionGrant{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		return domain.SessionGrant{}, domain.ErrSecondFactorMismatch
	}
	if err != nil {
		return domain.SessionGrant{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE login_challenges SET used_at=$2 WHERE id=$1::uuid`, challengeID, now); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if method == "recovery_code" {
		var remaining int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM user_recovery_codes WHERE user_id=$1::uuid AND used_at IS NULL`, userID).Scan(&remaining); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		if err = auditAccount(ctx, tx, actor, "login.recovery_code_used", userID, nil, map[string]int{"recoveryCodesRemaining": remaining}); err != nil {
			return domain.SessionGrant{}, err
		}
	}
	grant, err := s.issueLoginSession(ctx, tx, userID, string(access.ClientWeb), device, domain.NativeClient{}, "", actor, now, in.MaxSessions, in.SessionTTL)
	if err != nil {
		return domain.SessionGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	return grant, nil
}

const appPasswordColumns = `id::text,name,created_at,last_used_at` //nolint:gosec // G101: SQL column list, not a credential

func collectAppPasswords(rows pgx.Rows) ([]domain.AppPassword, error) {
	defer rows.Close()
	result := make([]domain.AppPassword, 0)
	for rows.Next() {
		var p domain.AppPassword
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, storageError(err)
		}
		result = append(result, p)
	}
	return result, storageError(rows.Err())
}

// CreateAppPassword stores an application password digest for the actor
// (web session only), at most AppPasswordLimit per user.
func (s *Store) CreateAppPassword(ctx context.Context, actor domain.Actor, name string, digest []byte) (domain.AppPassword, error) {
	if !domain.ValidAppPasswordName(name) || !validDigest(digest) {
		return domain.AppPassword{}, domain.ErrInvalid
	}
	tx, err := s.selfWebTransaction(ctx, actor)
	if err != nil {
		return domain.AppPassword{}, err
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app_passwords WHERE user_id=$1::uuid`, actor.UserID).Scan(&count); err != nil {
		return domain.AppPassword{}, storageError(err)
	}
	if count >= domain.AppPasswordLimit {
		return domain.AppPassword{}, domain.ErrConflict
	}
	var p domain.AppPassword
	if err = tx.QueryRow(ctx, `INSERT INTO app_passwords(user_id,name,digest) VALUES($1::uuid,$2,$3) RETURNING `+appPasswordColumns, actor.UserID, name, digest).Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
		return domain.AppPassword{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "user.app_password_created", actor.UserID, nil, map[string]string{"appPasswordId": p.ID, "name": p.Name}); err != nil {
		return domain.AppPassword{}, err
	}
	return p, storageError(tx.Commit(ctx))
}

// ListAppPasswords lists a user's application passwords for the user or an
// administrator, oldest first.
func (s *Store) ListAppPasswords(ctx context.Context, actor domain.Actor, userID string) ([]domain.AppPassword, error) {
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
	rows, err := tx.Query(ctx, `SELECT `+appPasswordColumns+` FROM app_passwords WHERE user_id=$1::uuid ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, storageError(err)
	}
	result, err := collectAppPasswords(rows)
	if err != nil {
		return nil, err
	}
	return result, storageError(tx.Commit(ctx))
}

// DeleteAppPassword revokes an application password and every session it
// issued. The user needs a web session; an administrator may revoke any
// user's passwords.
func (s *Store) DeleteAppPassword(ctx context.Context, actor domain.Actor, userID, id string) error {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return domain.ErrForbidden
	}
	if !admin {
		if err = requireWebSession(ctx, tx, actor); err != nil {
			return err
		}
	}
	if !domain.ValidID(userID) || !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	var name string
	if err = tx.QueryRow(ctx, `SELECT name FROM app_passwords WHERE id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, id, userID).Scan(&name); err != nil {
		return storageError(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE app_password_id=$1::uuid AND revoked_at IS NULL`, id)
	if err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM app_passwords WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "user.app_password_revoked", userID, map[string]string{"appPasswordId": id, "name": name}, map[string]int64{"sessionsRevoked": tag.RowsAffected()}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
