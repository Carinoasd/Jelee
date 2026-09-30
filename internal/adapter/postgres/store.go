package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

// SchemaVersion is the only clean schema accepted by this binary. Adjacent
// releases cannot serve against different cache and job lifecycle contracts.
const SchemaVersion = 4

func Open(ctx context.Context, dsn string, maxConnections int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL configuration")
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
	if len(token) != 43 {
		return access.Principal{}, domain.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var p access.Principal
	err := s.Pool.QueryRow(ctx, `SELECT u.id::text,s.id::text,s.client_kind,u.is_admin,u.locale FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND NOT u.disabled AND u.deleted_at IS NULL`, hash[:]).Scan(&p.UserID, &p.SessionID, &p.Kind, &p.Admin, &p.Locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.ErrUnauthenticated
	}
	if err != nil {
		return p, storageError(err)
	}
	return p, nil
}

// Split the administrator path from library ACL lookup so an invisible large
// library does not force a full ordered scan. Each allowed library contributes
// at most one bounded page before the final stable merge.
const listItemsSQL = `WITH principal AS MATERIALIZED (
 SELECT id,is_admin FROM users WHERE id=$1::uuid AND NOT disabled AND deleted_at IS NULL
), visible AS (
 SELECT i.* FROM principal u JOIN library_acl a ON a.user_id=u.id
 JOIN LATERAL (
  SELECT id,library_id,title,kind FROM items
  WHERE library_id=a.library_id AND id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
  ORDER BY id LIMIT $3
 ) i ON true WHERE NOT u.is_admin
 UNION ALL
 SELECT i.* FROM principal u JOIN LATERAL (
  SELECT id,library_id,title,kind FROM items
  WHERE id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
  ORDER BY id LIMIT $3
 ) i ON true WHERE u.is_admin
) SELECT id::text,library_id::text,title,kind FROM visible ORDER BY id LIMIT $3`

func (s *Store) ListItems(ctx context.Context, userID, cursor string, limit int) ([]domain.Item, error) {
	rows, err := s.Pool.Query(ctx, listItemsSQL, userID, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	items := make([]domain.Item, 0, limit)
	for rows.Next() {
		var item domain.Item
		if err = rows.Scan(&item.ID, &item.LibraryID, &item.Title, &item.Kind); err != nil {
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
	err := s.Pool.QueryRow(ctx, `SELECT i.id::text,i.library_id::text,i.title,i.kind FROM items i JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL WHERE i.id=$2::uuid AND (u.is_admin OR EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=i.library_id))`, userID, id).Scan(&item.ID, &item.LibraryID, &item.Title, &item.Kind)
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
	err := s.Pool.QueryRow(ctx, `SELECT r.path,m.relative_path,m.content_type FROM media_sources m JOIN library_roots r ON r.id=m.root_id JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind='native' AND s.revoked_at IS NULL AND s.expires_at>now() WHERE m.id=$3::uuid AND (u.is_admin OR EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=m.library_id))`, p.UserID, p.SessionID, sourceID).Scan(&source.Root, &source.RelativePath, &source.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, media.ErrNotFound
	}
	if err != nil {
		return source, storageError(err)
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
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(event,target_id) VALUES('user.provisioned',$1)`, id); err != nil {
		return "", storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", storageError(err)
	}
	return token, nil
}
