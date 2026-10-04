package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Share links and guest sessions (G48.6). An administrator creates a share
// of a library or of an item and its descendants; the share owns one hidden
// guest account (users.share_id) without a password, whose sessions the
// link issues. What a guest sees is decided by the unified filter
// (visibility.go) from users.share_id; this file only administers shares,
// issues guest sessions and records their use. Every change and every use
// is audited against the share's ID.

// shareColumns reads a share s with its library l and optional item i.
const shareColumns = `s.id::text,s.library_id::text,l.name,COALESCE(s.item_id::text,''),COALESCE(i.title,''),COALESCE(i.kind,''),s.expires_at,s.read_only,
 s.allow_playback,s.max_streams,s.note,COALESCE(s.created_by::text,''),s.created_at,s.revoked_at,
 CASE WHEN s.revoked_at IS NOT NULL THEN 'revoked' WHEN s.expires_at<=now() THEN 'expired' ELSE 'active' END,
 CASE WHEN s.revoked_at IS NULL AND s.expires_at>now() THEN (SELECT count(*) FROM users g JOIN sessions x ON x.user_id=g.id WHERE g.share_id=s.id AND x.revoked_at IS NULL AND x.expires_at>now()) ELSE 0 END::int,
 (SELECT max(COALESCE(x.last_seen_at,x.created_at)) FROM users g JOIN sessions x ON x.user_id=g.id WHERE g.share_id=s.id)`

const shareFrom = ` FROM share_links s JOIN libraries l ON l.id=s.library_id LEFT JOIN items i ON i.id=s.item_id`

func scanShare(row pgx.Row) (domain.Share, error) {
	var v domain.Share
	err := row.Scan(&v.ID, &v.LibraryID, &v.LibraryName, &v.ItemID, &v.ItemTitle, &v.ItemKind, &v.ExpiresAt, &v.ReadOnly, &v.AllowPlayback, &v.MaxStreams, &v.Note,
		&v.CreatedBy, &v.CreatedAt, &v.RevokedAt, &v.State, &v.ActiveSessions, &v.LastUsedAt)
	if err != nil {
		return v, storageError(err)
	}
	v.ExpiresAt, v.CreatedAt = v.ExpiresAt.UTC(), v.CreatedAt.UTC()
	for _, t := range []**time.Time{&v.RevokedAt, &v.LastUsedAt} {
		if *t != nil {
			u := (*t).UTC()
			*t = &u
		}
	}
	return v, nil
}

// shareAudit is the audited form of a share: never the token or its digest.
func shareAudit(v domain.Share) map[string]any {
	return map[string]any{"libraryId": v.LibraryID, "itemId": v.ItemID, "expiresAt": v.ExpiresAt.Format(time.RFC3339), "readOnly": v.ReadOnly,
		"allowPlayback": v.AllowPlayback, "maxStreams": v.MaxStreams, "note": v.Note}
}

func auditShare(ctx context.Context, tx auditExecer, actor domain.Actor, event, share string, before, after any) error {
	return appendAudit(ctx, tx, AuditEntry{Event: event, Actor: actor, TargetID: share, Before: before, After: after})
}

// shareTokenHash is the stored digest of a share token: 43 base64url
// characters of 32 random bytes, like a session token.
func shareTokenHash(token string) ([]byte, bool) {
	if len(token) != 43 {
		return nil, false
	}
	if _, err := base64.RawURLEncoding.DecodeString(token); err != nil {
		return nil, false
	}
	sum := sha256.Sum256([]byte("jelee-share-v1\x00" + token))
	return sum[:], true
}

// ListShares lists the newest shares first, at most SharesListMax.
func (s *Store) ListShares(ctx context.Context, actor domain.Actor) ([]domain.Share, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+shareColumns+shareFrom+` ORDER BY s.created_at DESC,s.id DESC LIMIT $1`, domain.SharesListMax)
	if err != nil {
		return nil, storageError(err)
	}
	shares := make([]domain.Share, 0)
	for rows.Next() {
		v, e := scanShare(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		shares = append(shares, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return shares, storageError(tx.Commit(ctx))
}

// GetShare reads one share.
func (s *Store) GetShare(ctx context.Context, actor domain.Actor, id string) (domain.Share, error) {
	if !domain.ValidID(id) {
		return domain.Share{}, domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.Share{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanShare(tx.QueryRow(ctx, `SELECT `+shareColumns+shareFrom+` WHERE s.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	return v, storageError(tx.Commit(ctx))
}

// CreateShare stores a share, its guest account and the token digest, and
// returns the token once.
func (s *Store) CreateShare(ctx context.Context, actor domain.Actor, in domain.ShareInput) (domain.ShareGrant, error) {
	if (in.LibraryID == "") == (in.ItemID == "") || in.MaxStreams < 1 || in.MaxStreams > domain.ShareStreamsMax || !validText(in.Note, domain.ShareNoteMax, true) {
		return domain.ShareGrant{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.ShareGrant{}, err
	}
	defer tx.Rollback(ctx)
	// Live shares are counted under one lock so concurrent creations cannot
	// pass the limit together.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('jelee.share_links'))`); err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	library := in.LibraryID
	if in.ItemID != "" {
		if !domain.ValidID(in.ItemID) {
			return domain.ShareGrant{}, domain.ErrNotFound
		}
		err = tx.QueryRow(ctx, `SELECT library_id::text FROM items WHERE id=$1::uuid`, in.ItemID).Scan(&library)
	} else if domain.ValidID(library) {
		err = tx.QueryRow(ctx, `SELECT id::text FROM libraries WHERE id=$1::uuid`, library).Scan(&library)
	} else {
		err = pgx.ErrNoRows
	}
	if err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	var live int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE revoked_at IS NULL AND expires_at>now()`).Scan(&live); err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	if live >= domain.SharesLiveMax {
		return domain.ShareGrant{}, domain.ErrConflict
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return domain.ShareGrant{}, domain.ErrDatabase
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash, _ := shareTokenHash(token)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO share_links(token_hash,library_id,item_id,expires_at,read_only,allow_playback,max_streams,note,created_by)
 VALUES($1,$2::uuid,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9::uuid) RETURNING id::text`,
		hash, library, in.ItemID, in.ExpiresAt, in.ReadOnly, in.AllowPlayback, in.MaxStreams, in.Note, actor.UserID).Scan(&id)
	if err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	// The guest account has no password and is hidden; its locale follows
	// the administrator who shared.
	if _, err = tx.Exec(ctx, `INSERT INTO users(name,display_name,locale,hidden,allow_native,share_id)
 SELECT 'share:'||$1,'',a.locale,true,$2,$1::uuid FROM users a WHERE a.id=$3::uuid`, id, in.AllowPlayback, actor.UserID); err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	v, err := scanShare(tx.QueryRow(ctx, `SELECT `+shareColumns+shareFrom+` WHERE s.id=$1::uuid`, id))
	if err != nil {
		return domain.ShareGrant{}, err
	}
	if err = auditShare(ctx, tx, actor, "share.created", id, nil, shareAudit(v)); err != nil {
		return domain.ShareGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ShareGrant{}, storageError(err)
	}
	return domain.ShareGrant{Share: v, Token: token}, nil
}

// RevokeShare revokes a share and every session of its guest in one
// transaction. Running streams of those sessions end at their next session
// check (G07.4). Revoking a revoked share changes nothing.
func (s *Store) RevokeShare(ctx context.Context, actor domain.Actor, id string) (domain.Share, error) {
	if !domain.ValidID(id) {
		return domain.Share{}, domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.Share{}, err
	}
	defer tx.Rollback(ctx)
	var revoked bool
	if err = tx.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM share_links WHERE id=$1::uuid FOR UPDATE`, id).Scan(&revoked); err != nil {
		return domain.Share{}, storageError(err)
	}
	if !revoked {
		if _, err = tx.Exec(ctx, `UPDATE share_links SET revoked_at=now(),revoked_by=$2::uuid WHERE id=$1::uuid`, id, actor.UserID); err != nil {
			return domain.Share{}, storageError(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE revoked_at IS NULL AND user_id=(SELECT id FROM users WHERE share_id=$1::uuid)`, id)
		if err != nil {
			return domain.Share{}, storageError(err)
		}
		if err = auditShare(ctx, tx, actor, "share.revoked", id, nil, map[string]any{"sessionsRevoked": tag.RowsAffected()}); err != nil {
			return domain.Share{}, err
		}
	}
	v, err := scanShare(tx.QueryRow(ctx, `SELECT `+shareColumns+shareFrom+` WHERE s.id=$1::uuid`, id))
	if err != nil {
		return v, err
	}
	return v, storageError(tx.Commit(ctx))
}

// RedeemShare exchanges a live share's token for a guest session. Unknown,
// revoked and expired tokens are refused alike; a refusal of a known share
// is audited against it. A native session needs a share that allows
// playback. The session never outlives the share.
func (s *Store) RedeemShare(ctx context.Context, in domain.ShareRedemption) (domain.SessionGrant, error) {
	hash, ok := shareTokenHash(in.Token)
	if !ok {
		return domain.SessionGrant{}, domain.ErrShareUnavailable
	}
	if in.Native && !validNativeClient(in.Client) || !in.Native && in.Client != (domain.NativeClient{}) || !validText(in.DeviceName, 128, true) ||
		in.MaxSessions < 1 || in.MaxSessions > 100 || !validTTL(in.SessionTTL) {
		return domain.SessionGrant{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	var share, guest string
	var revoked, expired, playback bool
	var remaining float64
	err = tx.QueryRow(ctx, `SELECT s.id::text,g.id::text,s.revoked_at IS NOT NULL,s.expires_at<=clock_timestamp(),s.allow_playback,
 EXTRACT(EPOCH FROM s.expires_at-clock_timestamp())::float8
 FROM share_links s JOIN users g ON g.share_id=s.id WHERE s.token_hash=$1 FOR UPDATE OF s`, hash).Scan(&share, &guest, &revoked, &expired, &playback, &remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionGrant{}, domain.ErrShareUnavailable
	}
	if err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	kind := access.ClientWeb
	if in.Native {
		kind = access.ClientNative
	}
	actor := domain.Actor{UserID: guest, IP: in.IP}
	refuse := func(reason string, refusal error) (domain.SessionGrant, error) {
		if err := auditShare(ctx, tx, actor, "share.redeem_refused", share, nil, map[string]any{"reason": reason, "clientKind": string(kind)}); err != nil {
			return domain.SessionGrant{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.SessionGrant{}, storageError(err)
		}
		return domain.SessionGrant{}, refusal
	}
	ttl := in.SessionTTL
	if left := time.Duration(remaining * float64(time.Second)).Truncate(time.Second); left < ttl {
		ttl = left
	}
	switch {
	case revoked:
		return refuse("revoked", domain.ErrShareUnavailable)
	case expired || ttl < time.Second:
		return refuse("expired", domain.ErrShareUnavailable)
	case in.Native && !playback:
		return refuse("playback", domain.ErrSharePlaybackDisabled)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1::uuid AND revoked_at IS NULL AND expires_at>now()`, guest).Scan(&count); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	if count >= in.MaxSessions {
		return refuse("session_limit", domain.ErrSessionLimit)
	}
	session, token, err := newSession(ctx, tx, guest, string(kind), in.DeviceName, in.Client, ttl, "")
	if err != nil {
		return domain.SessionGrant{}, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid`, guest))
	if err != nil {
		return domain.SessionGrant{}, err
	}
	actor.SessionID = session.ID
	if err = auditShare(ctx, tx, actor, "share.redeemed", share, nil, map[string]any{"sessionId": session.ID, "clientKind": string(kind), "deviceName": in.DeviceName,
		"client": in.Client.Name, "deviceId": in.Client.DeviceID}); err != nil {
		return domain.SessionGrant{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.SessionGrant{}, storageError(err)
	}
	return domain.SessionGrant{User: u, Session: session, Token: token}, nil
}

// CurrentShare describes the share of the actor's guest session; any other
// session is answered as not found.
func (s *Store) CurrentShare(ctx context.Context, actor domain.Actor) (domain.GuestShare, error) {
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.GuestShare{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.GuestShare
	err = tx.QueryRow(ctx, `SELECT s.id::text,s.library_id::text,l.name,COALESCE(s.item_id::text,''),COALESCE(i.title,''),COALESCE(i.kind,''),s.expires_at,s.read_only,s.allow_playback
 `+shareFrom+` JOIN users g ON g.share_id=s.id WHERE g.id=$1::uuid AND s.revoked_at IS NULL AND s.expires_at>now()`, actor.UserID).Scan(
		&v.ID, &v.LibraryID, &v.LibraryName, &v.ItemID, &v.ItemTitle, &v.ItemKind, &v.ExpiresAt, &v.ReadOnly, &v.AllowPlayback)
	if err != nil {
		return domain.GuestShare{}, storageError(err)
	}
	v.ExpiresAt = v.ExpiresAt.UTC()
	return v, storageError(tx.Commit(ctx))
}

// RecordShareAccess audits one use of a guest session against its share.
// The HTTP layer calls it at most once per session, route and minute.
func (s *Store) RecordShareAccess(ctx context.Context, a domain.ShareAccess) error {
	if !domain.ValidID(a.ShareID) || !domain.ValidID(a.Actor.UserID) || !domain.ValidID(a.Actor.SessionID) || !validText(a.Route, 256, false) {
		return domain.ErrInvalid
	}
	event := "share.accessed"
	if a.Refused {
		event = "share.access_refused"
	}
	return auditShare(ctx, s.Pool, a.Actor, event, a.ShareID, nil, map[string]any{"sessionId": a.Actor.SessionID, "clientKind": a.ClientKind, "route": a.Route})
}

// ListShareAccess pages the audited events of one share, newest first.
func (s *Store) ListShareAccess(ctx context.Context, actor domain.Actor, id, cursor string, limit int) ([]domain.ShareAccessRecord, string, error) {
	if !domain.ValidID(id) {
		return nil, "", domain.ErrNotFound
	}
	if _, err := s.GetShare(ctx, actor, id); err != nil {
		return nil, "", err
	}
	records, next, err := s.ListAudit(ctx, actor, domain.AuditFilter{Target: id}, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]domain.ShareAccessRecord, 0, len(records))
	for _, r := range records {
		var after struct {
			SessionID  string `json:"sessionId"`
			ClientKind string `json:"clientKind"`
			DeviceName string `json:"deviceName"`
			Route      string `json:"route"`
			Reason     string `json:"reason"`
		}
		_ = json.Unmarshal(r.After, &after)
		out = append(out, domain.ShareAccessRecord{ID: r.ID, Event: r.Event, OccurredAt: r.OccurredAt.UTC(), ActorID: r.ActorID, IP: r.ActorIP,
			SessionID: after.SessionID, ClientKind: after.ClientKind, DeviceName: after.DeviceName, Route: after.Route, Reason: after.Reason})
	}
	return out, next, nil
}

// liveShareOnItem refuses removing an item a live (unrevoked, unexpired)
// share link targets.
func liveShareOnItem(ctx context.Context, tx pgx.Tx, item string) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM share_links WHERE item_id=$1::uuid AND revoked_at IS NULL AND expires_at>clock_timestamp())`, item).Scan(&live); err != nil {
		return storageError(err)
	}
	if live {
		return domain.ErrVersionItemBusy
	}
	return nil
}
