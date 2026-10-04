package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool *pgxpool.Pool
	// webhookEvents makes producers append webhook events to the outbox
	// (G12.3). It is off unless the webhook deliverer is configured, so a
	// deployment without webhooks never accumulates events and statements
	// keep working against schemas without the outbox.
	webhookEvents atomic.Bool
}

// SchemaVersion is the only clean schema accepted by this binary. Adjacent
// releases cannot serve against different cache and job lifecycle contracts.
const SchemaVersion = 79

func Open(ctx context.Context, dsn string, maxConnections int32) (*Store, error) {
	return open(ctx, dsn, maxConnections, nil)
}

func open(ctx context.Context, dsn string, maxConnections int32, log *QueryLog) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
	}
	if log != nil {
		cfg.ConnConfig.Tracer = log
	}
	cfg.MaxConns = maxConnections
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL pool")
	}
	s := &Store{Pool: pool}
	if err = s.Ready(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Ready(ctx context.Context) error {
	if err := s.Pool.Ping(ctx); err != nil {
		return errors.New("PostgreSQL is unavailable")
	}
	var version int
	var dirty bool
	if err := s.Pool.QueryRow(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != SchemaVersion || dirty {
		return errors.New("database migration required or dirty")
	}
	return nil
}

func (s *Store) Authenticate(ctx context.Context, token string) (access.Principal, error) {
	a, err := s.authenticate(ctx, token)
	return a.principal, err
}

// authenticated is one session lookup: the principal, the client labels
// recorded with the session, the client control version (G47) and whether
// the session's last-use record is older than sessionTouchInterval, so
// callers write it only when it is due.
type authenticated struct {
	principal access.Principal
	client    access.SessionClient
	version   int64
	stale     bool
}

func (s *Store) authenticate(ctx context.Context, token string) (authenticated, error) {
	var a authenticated
	if len(token) != 43 {
		return a, domain.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	p := &a.principal
	err := s.Pool.QueryRow(ctx, `SELECT u.id::text,s.id::text,s.client_kind,u.is_admin,u.locale,s.last_seen_at IS NULL OR s.last_seen_at<=clock_timestamp()-$2*interval '1 second',
 COALESCE(s.device_id,''),COALESCE(s.client_name,''),COALESCE(s.client_version,''),s.device_name,s.created_at,(SELECT version FROM client_control_policy),
 COALESCE(u.share_id::text,''),`+guestReadOnlySQL+`
 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND NOT u.disabled AND u.deleted_at IS NULL AND `+guestLiveSQL, hash[:], int64(sessionTouchInterval/time.Second)).Scan(&p.UserID, &p.SessionID, &p.Kind, &p.Admin, &p.Locale, &a.stale,
		&a.client.DeviceID, &a.client.Name, &a.client.Version, &a.client.DeviceName, &a.client.IssuedAt, &a.version, &p.ShareID, &p.ShareReadOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return authenticated{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return authenticated{}, storageError(err)
	}
	return a, nil
}

// Every listing walks libraries, so an invisible large library never forces
// a full ordered scan: a non-administrator its granted libraries, an
// administrator every library, a guest of an item share the shared subtree.
// Each walked library contributes at most one bounded page before the final
// stable merge. The request restriction (G48.5) drops whole libraries before
// their pages are read, so a library hidden from this request costs no item
// reads, and inside each bounded page only the content rules are evaluated
// per row, so a hidden item never takes a slot.
var listItemsSQL = `WITH principal AS MATERIALIZED (
 SELECT ` + visibilityUserColumns + ` FROM users WHERE id=$1::uuid AND NOT disabled AND deleted_at IS NULL
), walked AS (
 SELECT a.library_id FROM principal u JOIN LATERAL (` + grantedLibrariesSQL("u") + `) a ON ` + requestLibrarySQL("$4", "a.library_id") + ` WHERE NOT u.is_admin
 UNION ALL
 SELECT l.id FROM principal u JOIN libraries l ON ` + requestLibrarySQL("$4", "l.id") + ` WHERE u.is_admin
), visible AS (
 SELECT i.* FROM principal u CROSS JOIN walked a
 JOIN LATERAL (
  SELECT it.id,it.library_id,it.title,it.kind FROM items it
  WHERE it.library_id=a.library_id AND it.id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
   AND ` + contentVisibleSQL("it.id") + `
  ORDER BY it.id LIMIT $3
 ) i ON true
 UNION ALL
 SELECT i.* FROM principal u JOIN LATERAL (
  SELECT it.id,it.library_id,it.title,it.kind FROM items it
  WHERE it.id IN (` + sharedItemsSQL("u") + `) AND it.id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
   AND ` + walkedItemVisibleSQL("$4", "it.library_id", "it.id") + `
  ORDER BY it.id LIMIT $3
 ) i ON true WHERE u.share_id IS NOT NULL
) SELECT id::text,library_id::text,title,kind,COALESCE((SELECT parent_id::text FROM item_parent_links p WHERE p.item_id=visible.id),'') FROM visible ORDER BY id LIMIT $3`

func (s *Store) ListItems(ctx context.Context, userID, cursor string, limit int) ([]domain.Item, error) {
	rows, err := s.Pool.Query(ctx, listItemsSQL, userID, cursor, limit, requestScopeArg(ctx))
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	items := make([]domain.Item, 0, limit)
	for rows.Next() {
		var item domain.Item
		if err = rows.Scan(&item.ID, &item.LibraryID, &item.Title, &item.Kind, &item.ParentID); err != nil {
			return nil, storageError(err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return items, nil
}

func (s *Store) GetItem(ctx context.Context, userID, id string) (domain.Item, error) {
	var item domain.Item
	err := s.Pool.QueryRow(ctx, `SELECT i.id::text,i.library_id::text,i.title,i.kind,COALESCE((SELECT parent_id::text FROM item_parent_links p WHERE p.item_id=i.id),'') FROM items i JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL WHERE i.id=$2::uuid AND `+itemVisibleSQL("$3", "i.library_id", "i.id"), userID, id, requestScopeArg(ctx)).Scan(&item.ID, &item.LibraryID, &item.Title, &item.Kind, &item.ParentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, domain.ErrNotFound
	}
	if err != nil {
		return item, storageError(err)
	}
	return item, nil
}

func (s *Store) Resolve(ctx context.Context, p access.Principal, sourceID string) (media.Source, error) {
	if !domain.ValidID(sourceID) {
		return media.Source{}, media.ErrNotFound
	}
	var source media.Source
	// Recheck the live session and ACL in the same query immediately before opening.
	// It also returns the session's device and the user's delivery overrides for stream limits.
	err := s.Pool.QueryRow(ctx, `SELECT r.path,m.relative_path,m.content_type,COALESCE(s.device_id,''),u.max_streams,u.max_kbps,`+shareStreamsSQL+` FROM media_sources m JOIN library_roots r ON r.id=m.root_id JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind='native' AND s.revoked_at IS NULL AND s.expires_at>now() WHERE m.id=$3::uuid AND `+sharePlaybackSQL+` AND `+itemVisibleSQL("$4", "m.library_id", "m.item_id"), p.UserID, p.SessionID, sourceID, requestScopeArg(ctx)).Scan(&source.Root, &source.RelativePath, &source.ContentType, &source.DeviceID, &source.Limits.MaxStreams, &source.Limits.MaxKbps, &source.ShareStreams)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, media.ErrNotFound
	}
	if err != nil {
		return source, storageError(err)
	}
	return source, nil
}

// ResolveTrack binds one external subtitle or audio file for direct delivery
// (G10.9). The live native session, the enabled user and the library grant of
// the source are rechecked in the same statement, like Resolve; the track must
// belong to that source and have the requested kind. Every miss, including a
// track of another source, is ErrNotFound.
func (s *Store) ResolveTrack(ctx context.Context, p access.Principal, sourceID string, kind media.TrackKind, trackID string) (media.Source, error) {
	if !domain.ValidID(sourceID) || !domain.ValidID(trackID) || (kind != media.TrackSubtitle && kind != media.TrackAudio) {
		return media.Source{}, media.ErrNotFound
	}
	var source media.Source
	err := s.Pool.QueryRow(ctx, `SELECT r.path,t.relative_path,COALESCE(t.charset,''),COALESCE(s.device_id,''),u.max_streams,u.max_kbps,`+shareStreamsSQL+`
 FROM media_sidecar_tracks t
 JOIN media_sources m ON m.id=t.source_id AND m.library_id=t.library_id
 JOIN library_roots r ON r.id=t.root_id AND r.library_id=t.library_id
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind='native' AND s.revoked_at IS NULL AND s.expires_at>now()
 WHERE t.id=$4::uuid AND t.source_id=$3::uuid AND t.kind=$5
  AND `+sharePlaybackSQL+` AND `+itemVisibleSQL("$6", "m.library_id", "m.item_id"),
		p.UserID, p.SessionID, sourceID, trackID, string(kind), requestScopeArg(ctx)).Scan(&source.Root, &source.RelativePath, &source.Charset, &source.DeviceID, &source.Limits.MaxStreams, &source.Limits.MaxKbps, &source.ShareStreams)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.Source{}, media.ErrNotFound
	}
	if err != nil {
		return media.Source{}, storageError(err)
	}
	return source, nil
}

// Provision is local administrative bootstrap, not a public authentication endpoint.
func (s *Store) Provision(ctx context.Context, name string, kind access.ClientKind, admin bool) (string, error) {
	if len(name) < 1 || len(name) > 128 || kind != access.ClientNative && kind != access.ClientWeb {
		return "", domain.ErrInvalid
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", storageError(err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", storageError(err)
	}
	defer tx.Rollback(ctx)
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO users(name,is_admin) VALUES($1,$2) RETURNING id::text`, name, admin).Scan(&id); err != nil {
		return "", storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,client_kind,expires_at) VALUES($1,$2,$3,now()+interval '24 hours')`, id, hash[:], kind); err != nil {
		return "", storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "user.provisioned", TargetID: id}); err != nil {
		return "", storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", storageError(err)
	}
	return token, nil
}
