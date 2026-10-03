package postgres

import (
	"context"
	"path/filepath"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.ImageSourceRepository = (*Store)(nil)

// ResolveImageSource binds an item to exactly one local media source. Image
// access includes web sessions; the native-only playback resolver is separate.
func (s *Store) ResolveImageSource(parent context.Context, actor domain.Actor, item string) (domain.LocalImageSource, error) {
	if parent == nil {
		return domain.LocalImageSource{}, domain.ErrInvalid
	}
	if err := parent.Err(); err != nil {
		return domain.LocalImageSource{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(item) {
		return domain.LocalImageSource{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT i.id::text,i.library_id::text,m.id::text,r.id::text,r.path,m.relative_path
 FROM items i
 JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
 JOIN library_roots r ON r.id=m.root_id AND r.library_id=i.library_id
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind IN ('web','native')
  AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 WHERE i.id=$3::uuid AND (u.is_admin OR EXISTS(
  SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=i.library_id))
 ORDER BY m.id LIMIT 2`, actor.UserID, actor.SessionID, item)
	if err != nil {
		return domain.LocalImageSource{}, storageError(err)
	}
	defer rows.Close()
	var value domain.LocalImageSource
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&value.ItemID, &value.LibraryID, &value.SourceID, &value.RootID, &value.RootPath, &value.MediaPath); err != nil {
			return domain.LocalImageSource{}, storageError(err)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.LocalImageSource{}, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.LocalImageSource{}, err
	}
	if count != 1 || !filepath.IsAbs(value.RootPath) || domain.ImportVideoContentType(value.MediaPath) == "" {
		return domain.LocalImageSource{}, domain.ErrNotFound
	}
	return value, nil
}
