package postgres

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const userColumns = `id::text,name,display_name,locale,hidden,is_admin,disabled,allow_native,deleted_at,created_at`

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505":
			return domain.ErrConflict
		case "23503", "23514", "22P02", "22001":
			return domain.ErrInvalid
		}
	}
	return domain.ErrDatabase
}

func scanUser(row pgx.Row) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Locale, &u.Hidden, &u.Admin, &u.Disabled, &u.AllowNative, &u.DeletedAt, &u.CreatedAt)
	return u, storageError(err)
}

func validText(s string, maximum int, empty bool) bool {
	if !utf8.ValidString(s) || !empty && s == "" || len(s) > maximum {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func normalizeUserInput(in domain.UserInput, password bool) (domain.UserInput, error) {
	if in.Locale == "" {
		in.Locale = "en-US"
	}
	if !validText(in.Name, 128, false) || strings.TrimSpace(in.Name) != in.Name || !validText(in.DisplayName, 128, true) || !validLocale(in.Locale) || password && !validPasswordHash(in.PasswordHash) {
		return in, domain.ErrInvalid
	}
	return in, nil
}

func validLocale(locale string) bool {
	return locale == "en-US" || locale == "zh-CN" || locale == "zh-TW" || locale == "ja-JP"
}

func validPasswordHash(hash string) bool {
	return len(hash) >= 30 && len(hash) <= 1024 && strings.HasPrefix(hash, "$argon2id$v=19$")
}

// Account administration is intentionally serialized. Together with row locks,
// this prevents concurrent demotion/deletion of the last two administrators.
func (s *Store) accountTransaction(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='2000ms'; SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481203)`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	return tx, nil
}

func (s *Store) authorizedTransaction(ctx context.Context, actor domain.Actor, adminOnly bool) (pgx.Tx, bool, error) {
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return nil, false, domain.ErrUnauthenticated
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return nil, false, err
	}
	admin, err := authorizeActorInTransaction(ctx, tx, actor, adminOnly)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, false, err
	}
	return tx, admin, nil
}

// Callers acquire their advisory locks before locking the live actor rows.
// Keeping the same validation inside the transaction preserves revocation and
// administrator checks while allowing jobs to establish a consistent order.
func authorizeActorInTransaction(ctx context.Context, tx pgx.Tx, actor domain.Actor, adminOnly bool) (bool, error) {
	var admin bool
	err := tx.QueryRow(ctx, `SELECT u.is_admin FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1::uuid AND s.id=$2::uuid AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now() FOR UPDATE OF u,s`, actor.UserID, actor.SessionID).Scan(&admin)
	if err != nil || adminOnly && !admin {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, domain.ErrUnauthenticated
		}
		if err != nil {
			return false, storageError(err)
		}
		return false, domain.ErrForbidden
	}
	return admin, nil
}

// auditAccount records a change against a UUID target through appendAudit.
func auditAccount(ctx context.Context, tx pgx.Tx, actor domain.Actor, event, target string, before, after any) error {
	if target == "" {
		return domain.ErrInvalid
	}
	return appendAudit(ctx, tx, AuditEntry{Event: event, Actor: actor, TargetID: target, Before: before, After: after})
}

func userInTransaction(ctx context.Context, tx pgx.Tx, id string) (domain.User, error) {
	if !domain.ValidID(id) {
		return domain.User{}, domain.ErrNotFound
	}
	return scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, id))
}

func protectLastAdmin(ctx context.Context, tx pgx.Tx, before, after domain.User) error {
	if !before.Admin || before.Disabled || before.DeletedAt != nil || after.Admin && !after.Disabled && after.DeletedAt == nil {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL`).Scan(&count); err != nil {
		return storageError(err)
	}
	if count <= 1 {
		return domain.ErrLastAdmin
	}
	return nil
}

func insertUser(ctx context.Context, tx pgx.Tx, in domain.UserInput) (domain.User, error) {
	return scanUser(tx.QueryRow(ctx, `INSERT INTO users(name,display_name,locale,hidden,is_admin,disabled,password_hash) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+userColumns, in.Name, in.DisplayName, in.Locale, in.Hidden, in.Admin, in.Disabled, in.PasswordHash))
}

func (s *Store) BootstrapAdmin(ctx context.Context, input domain.UserInput) (domain.User, error) {
	input.Admin, input.Disabled = true, false
	in, err := normalizeUserInput(input, true)
	if err != nil {
		return domain.User{}, err
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL)`).Scan(&exists); err != nil {
		return domain.User{}, storageError(err)
	}
	if exists {
		return domain.User{}, domain.ErrConflict
	}
	u, err := insertUser(ctx, tx, in)
	if err != nil {
		return u, err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "user.bootstrapped", u.ID, nil, u); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

func (s *Store) ListUsers(ctx context.Context, actor domain.Actor, cursor string, limit int, includeDeleted bool) ([]domain.User, error) {
	if cursor != "" && !domain.ValidID(cursor) || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+userColumns+` FROM users WHERE ($1 OR deleted_at IS NULL) AND id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $3`, includeDeleted, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	users := make([]domain.User, 0, limit)
	for rows.Next() {
		u, e := scanUser(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		users = append(users, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return users, storageError(tx.Commit(ctx))
}

func (s *Store) GetUser(ctx context.Context, actor domain.Actor, userID string) (domain.User, error) {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	if !admin && userID != actor.UserID {
		return domain.User{}, domain.ErrForbidden
	}
	u, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return u, err
	}
	return u, storageError(tx.Commit(ctx))
}

// The actor/key pair identifies a creation result. Replays return that resource
// and never modify its password; no password fingerprint is persisted.
func (s *Store) CreateUser(ctx context.Context, actor domain.Actor, input domain.UserInput, key string) (domain.User, bool, error) {
	in, err := normalizeUserInput(input, true)
	if err != nil || !validText(key, 128, false) {
		return domain.User{}, false, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.User{}, false, err
	}
	defer tx.Rollback(ctx)
	var existing string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM user_creation_keys WHERE actor_id=$1::uuid AND key=$2`, actor.UserID, key).Scan(&existing)
	if err == nil {
		u, e := userInTransaction(ctx, tx, existing)
		if e != nil {
			return u, false, e
		}
		return u, true, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, false, storageError(err)
	}
	u, err := insertUser(ctx, tx, in)
	if err != nil {
		return u, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_creation_keys(actor_id,key,user_id) VALUES($1::uuid,$2,$3::uuid)`, actor.UserID, key, u.ID); err != nil {
		return domain.User{}, false, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "user.created", u.ID, nil, u); err != nil {
		return domain.User{}, false, err
	}
	return u, false, storageError(tx.Commit(ctx))
}

func (s *Store) UpdateUser(ctx context.Context, actor domain.Actor, userID string, input domain.UserInput) (domain.User, error) {
	in, err := normalizeUserInput(input, false)
	if err != nil {
		return domain.User{}, err
	}
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
	proposed := old
	proposed.Admin, proposed.Disabled = in.Admin, in.Disabled
	if err = protectLastAdmin(ctx, tx, old, proposed); err != nil {
		return domain.User{}, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET name=$2,display_name=$3,locale=$4,hidden=$5,is_admin=$6,disabled=$7,auth_version=auth_version+1 WHERE id=$1::uuid RETURNING `+userColumns, userID, in.Name, in.DisplayName, in.Locale, in.Hidden, in.Admin, in.Disabled))
	if err != nil {
		return u, err
	}
	if old.Admin != u.Admin || u.Disabled {
		if err = revokeAll(ctx, tx, userID); err != nil {
			return domain.User{}, err
		}
	}
	if err = auditAccount(ctx, tx, actor, "user.updated", userID, old, u); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

func (s *Store) UpdateProfile(ctx context.Context, actor domain.Actor, input domain.ProfileInput) (domain.User, error) {
	if input.Locale == "" {
		input.Locale = "en-US"
	}
	if !validText(input.DisplayName, 128, true) || !validLocale(input.Locale) {
		return domain.User{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	old, err := userInTransaction(ctx, tx, actor.UserID)
	if err != nil {
		return old, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET display_name=$2,locale=$3,hidden=$4,auth_version=auth_version+1 WHERE id=$1::uuid RETURNING `+userColumns, actor.UserID, input.DisplayName, input.Locale, input.Hidden))
	if err != nil {
		return u, err
	}
	if err = auditAccount(ctx, tx, actor, "user.profile_changed", u.ID, old, u); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

func (s *Store) DeleteUser(ctx context.Context, actor domain.Actor, userID string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	old, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	if old.DeletedAt != nil {
		return storageError(tx.Commit(ctx))
	}
	after := old
	after.Disabled = true
	if err = protectLastAdmin(ctx, tx, old, after); err != nil {
		return err
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET deleted_at=now(),auth_version=auth_version+1 WHERE id=$1::uuid RETURNING `+userColumns, userID))
	if err != nil {
		return err
	}
	if err = revokeAll(ctx, tx, userID); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, actor, "user.deleted", userID, old, u); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) RestoreUser(ctx context.Context, actor domain.Actor, userID string) (domain.User, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	old, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return old, err
	}
	if old.DeletedAt == nil {
		return old, storageError(tx.Commit(ctx))
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET deleted_at=NULL,auth_version=auth_version+1 WHERE id=$1::uuid RETURNING `+userColumns, userID))
	if err != nil {
		return u, err
	}
	if err = auditAccount(ctx, tx, actor, "user.restored", userID, old, u); err != nil {
		return domain.User{}, err
	}
	return u, storageError(tx.Commit(ctx))
}

func (s *Store) UnlockUser(ctx context.Context, actor domain.Actor, userID string) error {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	if u.DeletedAt != nil {
		return domain.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET failed_login=0,locked_until=NULL,auth_version=auth_version+1 WHERE id=$1::uuid`, userID); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "user.unlocked", userID, nil, map[string]bool{"unlocked": true}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
