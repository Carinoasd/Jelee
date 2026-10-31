package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.FavoriteRepository = (*Store)(nil)

// Favourites (G24.2). user_favorites holds one row per user and logical
// item. Every statement here binds the item to the caller through
// itemVisibleSQL in the same statement (G48.3): a hidden item is never
// listed or counted, and marking or unmarking one changes nothing and is
// answered like a missing item. Rows of items the user can no longer see
// stay, so a restored grant brings them back, and go with the user or item.

// favoriteSQL reports whether the principal u marked the item in column as
// a favourite. Callers have already bound the item to u.
func favoriteSQL(item string) string {
	return `EXISTS(SELECT 1 FROM user_favorites uf WHERE uf.user_id=u.id AND uf.item_id=` + item + `)`
}

// SetFavorite marks (favorite true) or unmarks a visible item for userID
// and returns the user's data of the item. Marking twice keeps the first
// time; unmarking an item that is not a favourite changes nothing.
func (s *Store) SetFavorite(ctx context.Context, userID, itemID string, favorite bool) (domain.UserItemData, error) {
	if ctx == nil {
		return domain.UserItemData{}, domain.ErrInvalid
	}
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	// The data-modifying statements run whatever the final SELECT reads;
	// the final SELECT sees the snapshot from before them, so the reported
	// state is the requested one.
	var d domain.UserItemData
	err := s.Pool.QueryRow(ctx, browsePrincipalSQL+`, item AS (
 SELECT i.id,i.library_id,u.id AS user_id FROM principal u JOIN items i ON i.id=@item::uuid WHERE `+itemVisibleSQL("@rq", "i.library_id", "i.id")+`
), added AS (
 INSERT INTO user_favorites(user_id,item_id,library_id) SELECT user_id,id,library_id FROM item WHERE @favorite::boolean
 ON CONFLICT (user_id,item_id) DO NOTHING RETURNING 1
), removed AS (
 DELETE FROM user_favorites f USING item WHERE NOT @favorite::boolean AND f.user_id=item.user_id AND f.item_id=item.id RETURNING 1
)
SELECT item.id::text,`+userItemDataColumns+`,@favorite::boolean FROM item LEFT JOIN user_item_data d ON d.user_id=item.user_id AND d.item_id=item.id`,
		pgx.NamedArgs{"user": userID, "item": itemID, "favorite": favorite, "rq": requestScopeArg(ctx)}).Scan(&d.ItemID, &d.ResumeTicks, &d.Played, &d.PlayCount, &d.LastPlayedAt, &d.IsFavorite)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.UserItemData{}, storageError(err)
	}
	return d, nil
}

// ListFavorites pages the visible favourites of userID, most recently
// marked first.
func (s *Store) ListFavorites(ctx context.Context, userID string, q domain.FavoriteQuery) (domain.FavoritePage, error) {
	if ctx == nil || !domain.ValidID(userID) || !domain.ValidFavoriteQuery(q) {
		return domain.FavoritePage{}, domain.ErrInvalid
	}
	args := pgx.NamedArgs{"user": userID, "kinds": append([]string{}, q.Kinds...), "library": q.LibraryID, "limit": q.Limit, "offset": q.Offset, "rq": requestScopeArg(ctx)}
	matched := browsePrincipalSQL + `, matched AS (
 SELECT i.id,i.library_id,i.kind,i.title,f.created_at FROM principal u
 JOIN user_favorites f ON f.user_id=u.id
 JOIN items i ON i.id=f.item_id
 WHERE ` + itemVisibleSQL("@rq", "i.library_id", "i.id") + `
  AND (@library::text='' OR i.library_id=NULLIF(@library::text,'')::uuid)
  AND (cardinality(@kinds::text[])=0 OR i.kind=ANY(@kinds::text[]))
)`
	rows, err := s.Pool.Query(ctx, matched+`
SELECT i.id::text,i.library_id::text,COALESCE(p.parent_id,i.library_id)::text,i.kind,i.title,COALESCE(fs.value,''),COALESCE(NULLIF(fd.value,''),''),COALESCE(`+browseYearSQL+`,0),
 i.created_at,count(*) OVER()
 FROM matched i`+browseMetadataSQL+`
 ORDER BY i.created_at DESC,i.id DESC LIMIT @limit OFFSET @offset`, args)
	if err != nil {
		return domain.FavoritePage{}, storageError(err)
	}
	defer rows.Close()
	page := domain.FavoritePage{Items: make([]domain.FavoriteEntry, 0, min(q.Limit, 64))}
	for rows.Next() {
		var e domain.FavoriteEntry
		item := &e.Item
		if err = rows.Scan(&item.ID, &item.LibraryID, &item.ParentID, &item.Kind, &item.Title, &item.SortTitle, &item.PremiereDate, &item.Year, &e.FavoritedAt, &page.Total); err != nil {
			return domain.FavoritePage{}, storageError(err)
		}
		e.FavoritedAt = e.FavoritedAt.UTC().Truncate(time.Microsecond)
		page.Items = append(page.Items, e)
	}
	if err = rows.Err(); err != nil {
		return domain.FavoritePage{}, storageError(err)
	}
	rows.Close()
	if len(page.Items) == 0 && q.Offset > 0 {
		// The window count is only seen with at least one row.
		if err = s.Pool.QueryRow(ctx, matched+` SELECT count(*) FROM matched`, args).Scan(&page.Total); err != nil {
			return domain.FavoritePage{}, storageError(err)
		}
	}
	return page, nil
}
