package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// G12.1 session.created and session.ended. Both are raised in the
// transaction that creates or revokes the session; expiry, which changes no
// row, is raised by RaiseExpiredSessions. The payload names the user, the
// client kind and why, never a token, device identifier, name or address.

// session.created reasons.
const (
	sessionCreatedLogin   = "login"
	sessionCreatedRotated = "rotated"
	sessionCreatedShare   = "share"
)

// session.ended reasons.
const (
	sessionEndLogout          = "logout"
	sessionEndRevoked         = "revoked"
	sessionEndRotated         = "rotated"
	sessionEndPasswordChanged = "password_changed"
	sessionEndPasswordReset   = "password_reset"
	sessionEndUserChanged     = "user_changed"
	sessionEndUserDeleted     = "user_deleted"
	sessionEndNativeAccess    = "native_access_removed"
	sessionEndTwoFactor       = "two_factor_enabled"
	sessionEndAppPassword     = "app_password_revoked"
	sessionEndShareRevoked    = "share_revoked"
	sessionEndClientRule      = "client_rule"
	sessionEndClientKicked    = "client_kicked"
	sessionEndExpired         = "expired"
)

func appendSessionCreated(ctx context.Context, tx pgx.Tx, events bool, session domain.Session, reason string) error {
	return appendWebhook(ctx, tx, events, domain.WebhookSessionCreated, session.CreatedAt, domain.WebhookSubject{Kind: domain.WebhookSubjectSession, ID: session.ID},
		map[string]any{"userId": session.UserID, "clientKind": session.ClientKind, "expiresAt": session.ExpiresAt.UTC().Format(time.RFC3339), "reason": reason})
}

func appendSessionEnded(ctx context.Context, tx pgx.Tx, events bool, sessionID, userID, clientKind, reason string) error {
	return appendWebhook(ctx, tx, events, domain.WebhookSessionEnded, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectSession, ID: sessionID},
		map[string]any{"userId": userID, "clientKind": clientKind, "reason": reason})
}

// endedSession is a session endSessions revoked.
type endedSession struct{ id, userID string }

// endSessions revokes every unrevoked session the predicate (over alias s,
// with args as $1..$n) selects. With events it raises session.ended for
// each of them that had not expired yet, in the same statement; an expired
// one already ended (and raised expired) and is only marked revoked.
// Without events the statement does not name the outbox.
func endSessions(ctx context.Context, tx pgx.Tx, events bool, reason, predicate string, args ...any) ([]endedSession, error) {
	query := `WITH ended AS (UPDATE sessions s SET revoked_at=now() WHERE s.revoked_at IS NULL AND (` + predicate + `)
 RETURNING s.id,s.user_id,s.client_kind,s.expires_at>now() AS live)`
	if events {
		args = append(args, reason)
		query += `, event AS (
 INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,data)
 SELECT 'session.ended',clock_timestamp(),'session',e.id::text,jsonb_build_object('userId',e.user_id::text,'clientKind',e.client_kind,'reason',$` + strconv.Itoa(len(args)) + `::text)
 FROM ended e WHERE e.live AND ` + webhookSubscribedSQL("'session.ended'") + `
 RETURNING 1
)`
	}
	rows, err := tx.Query(ctx, query+` SELECT e.id::text,e.user_id::text FROM ended e ORDER BY e.id`, args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []endedSession
	for rows.Next() {
		var e endedSession
		if err = rows.Scan(&e.id, &e.userID); err != nil {
			return nil, storageError(err)
		}
		out = append(out, e)
	}
	return out, storageError(rows.Err())
}

// sessionExpiryHorizon bounds how far back RaiseExpiredSessions looks: a
// session that expired while events were off for longer raises nothing.
const sessionExpiryHorizon = 24 * time.Hour

// RaiseExpiredSessions implements app.SessionExpiryRepository: it raises
// session.ended (reason expired) for at most limit unrevoked sessions that
// expired after the stored watermark, in expiry order, and advances the
// watermark past them in the same transaction. The watermark row lock
// serialises instances, so each expiry is raised once. Expiry within the
// horizon is caught up after downtime; older expiry is skipped.
func (s *Store) RaiseExpiredSessions(ctx context.Context, limit int) (int, error) {
	if ctx == nil || limit < 1 || limit > 10000 {
		return 0, domain.ErrInvalid
	}
	if !s.webhooksOn() {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback(ctx)
	var raised int
	// A session revoked after it expired still expired first, so it counts;
	// one revoked before never reaches its expiry.
	err = tx.QueryRow(ctx, `WITH mark AS (
 SELECT GREATEST(w.through_at,now()-$2*interval '1 microsecond') AS through_at,
  CASE WHEN w.through_at>=now()-$2*interval '1 microsecond' THEN w.through_id END AS through_id
 FROM session_expiry_watermark w FOR UPDATE
), due AS (
 SELECT s.id,s.user_id,s.client_kind,s.expires_at FROM sessions s CROSS JOIN mark m
 WHERE s.expires_at<=now() AND (s.revoked_at IS NULL OR s.revoked_at>s.expires_at)
  AND (s.expires_at>m.through_at OR s.expires_at=m.through_at AND m.through_id IS NOT NULL AND s.id>m.through_id)
 ORDER BY s.expires_at,s.id LIMIT $1
), last AS (
 SELECT expires_at,id,(SELECT count(*) FROM due) AS n FROM due ORDER BY expires_at DESC,id DESC LIMIT 1
), advanced AS (
 -- A full page stops at its last session; a short one covered every
 -- expiry up to now.
 UPDATE session_expiry_watermark w SET through_at=CASE WHEN l.n>=$1 THEN l.expires_at ELSE now() END,through_id=CASE WHEN l.n>=$1 THEN l.id END
 FROM (SELECT 1) one LEFT JOIN last l ON true RETURNING 1
), event AS (
 INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,data)
 SELECT 'session.ended',d.expires_at,'session',d.id::text,jsonb_build_object('userId',d.user_id::text,'clientKind',d.client_kind,'reason','`+sessionEndExpired+`')
 FROM due d WHERE `+webhookSubscribedSQL("'session.ended'")+`
 RETURNING 1
)
SELECT count(*)::int FROM due`, limit, sessionExpiryHorizon.Microseconds()).Scan(&raised)
	if err != nil {
		return 0, storageError(err)
	}
	return raised, storageError(tx.Commit(ctx))
}
