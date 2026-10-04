package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.ItemImageRepository    = (*Store)(nil)
	_ app.ImageVariantIndex      = (*Store)(nil)
	_ app.ItemImageSummaryReader = (*Store)(nil)
)

const itemImageColumns = `g.id::text,g.item_id::text,g.library_id::text,g.image_type,g.image_index,g.source_kind,
 COALESCE(g.root_id::text,''),COALESCE(r.path,''),COALESCE(g.relative_path,''),COALESCE(g.remote_url,''),
 g.source_mtime_unix_nano,g.source_size,g.content_sha256,g.width,g.height,g.format,g.byte_size,g.average_color,g.fetched_at,
 g.locked,g.created_at,g.updated_at`

// G40.10: a user lock wins, then local > nfo > remote > embedded.
const itemImagePriority = `g.locked DESC,CASE g.source_kind WHEN 'local' THEN 0 WHEN 'nfo' THEN 1 WHEN 'remote' THEN 2 ELSE 3 END`

// Reads bind the live web or native session and the library grant in the
// same statement as the image rows (G48.2/G48.8). An invisible item and a
// missing item are indistinguishable.
const itemImageVisibleItem = `FROM items i
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind IN ('web','native')
  AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`

var itemImageVisibleWhere = `i.id=$3::uuid AND ` + itemVisibleSQL("i.library_id", "i.id")

// itemImageRow holds nullable scan targets so the LEFT JOIN listing and the
// direct reads share one decoder.
type itemImageRow struct {
	id, itemID, libraryID, imageType, sourceKind, format *string
	rootID, rootPath, relativePath, remoteURL            string
	mtime, sourceSize, size                              *int64
	index, width, height, color                          *int32
	digest                                               []byte
	fetched, created, updated                            *time.Time
	locked                                               *bool
}

func (r *itemImageRow) targets() []any {
	return []any{&r.id, &r.itemID, &r.libraryID, &r.imageType, &r.index, &r.sourceKind, &r.rootID, &r.rootPath, &r.relativePath,
		&r.remoteURL, &r.mtime, &r.sourceSize, &r.digest, &r.width, &r.height, &r.format, &r.size, &r.color, &r.fetched,
		&r.locked, &r.created, &r.updated}
}

func (r *itemImageRow) value() (domain.ItemImage, error) {
	if r.id == nil || r.itemID == nil || r.libraryID == nil || r.imageType == nil || r.index == nil || r.sourceKind == nil ||
		r.locked == nil || r.created == nil || r.updated == nil {
		return domain.ItemImage{}, domain.ErrDatabase
	}
	value := domain.ItemImage{ID: *r.id, ItemID: *r.itemID, LibraryID: *r.libraryID, Type: *r.imageType, Index: int(*r.index),
		SourceKind: *r.sourceKind, RootID: r.rootID, RootPath: r.rootPath, RelativePath: r.relativePath, RemoteURL: r.remoteURL,
		SourceModifiedUnixNano: r.mtime, SourceSize: r.sourceSize, Locked: *r.locked, CreatedAt: r.created.UTC(), UpdatedAt: r.updated.UTC()}
	if r.digest != nil {
		if r.format == nil || r.size == nil || r.fetched == nil {
			return domain.ItemImage{}, domain.ErrDatabase
		}
		content := &domain.ItemImageContent{SHA256: r.digest, Format: *r.format, Bytes: *r.size, FetchedAt: r.fetched.UTC()}
		if r.width != nil && r.height != nil {
			content.Width, content.Height = int(*r.width), int(*r.height)
		}
		if r.color != nil {
			c := int(*r.color)
			content.AverageColor = &c
		}
		value.Content = content
	}
	return value, nil
}

func scanItemImage(row pgx.Row) (domain.ItemImage, error) {
	var r itemImageRow
	if err := row.Scan(r.targets()...); err != nil {
		return domain.ItemImage{}, err
	}
	return r.value()
}

func readerContext(parent context.Context, actor domain.Actor, item string) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, domain.ErrInvalid
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(item) {
		return nil, nil, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	return ctx, cancel, nil
}

// ListItemImages returns every image row of a visible item in slot order,
// best source first within a slot. A visible item without images yields an
// empty slice; an invisible or missing item yields ErrNotFound.
func (s *Store) ListItemImages(parent context.Context, actor domain.Actor, item string) ([]domain.ItemImage, error) {
	ctx, cancel, err := readerContext(parent, actor, item)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT i.id IS NOT NULL,g.id IS NOT NULL,`+itemImageColumns+` `+itemImageVisibleItem+`
 LEFT JOIN item_images g ON g.item_id=i.id AND g.library_id=i.library_id
 LEFT JOIN library_roots r ON r.id=g.root_id AND r.library_id=g.library_id
 WHERE `+itemImageVisibleWhere+`
 ORDER BY g.image_type,g.image_index,`+itemImagePriority, actor.UserID, actor.SessionID, item)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	visible := false
	result := make([]domain.ItemImage, 0)
	for rows.Next() {
		var found, present bool
		var r itemImageRow
		if err := rows.Scan(append([]any{&found, &present}, r.targets()...)...); err != nil {
			return nil, storageError(err)
		}
		visible = visible || found
		if !present {
			continue
		}
		value, err := r.value()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !visible {
		return nil, domain.ErrNotFound
	}
	return result, nil
}

// ResolveItemImage selects the best usable source for one slot. Root
// references are always usable; URL references only after their content has
// been fetched into the store.
func (s *Store) ResolveItemImage(parent context.Context, actor domain.Actor, item, imageType string, index int) (domain.ItemImage, error) {
	if !domain.ValidItemImageSlot(imageType, index) {
		return domain.ItemImage{}, domain.ErrNotFound
	}
	ctx, cancel, err := readerContext(parent, actor, item)
	if err != nil {
		return domain.ItemImage{}, err
	}
	defer cancel()
	value, err := scanItemImage(s.Pool.QueryRow(ctx, `SELECT `+itemImageColumns+` `+itemImageVisibleItem+`
 JOIN item_images g ON g.item_id=i.id AND g.library_id=i.library_id AND g.image_type=$4 AND g.image_index=$5
  AND (g.root_id IS NOT NULL OR g.content_sha256 IS NOT NULL)
 LEFT JOIN library_roots r ON r.id=g.root_id AND r.library_id=g.library_id
 WHERE `+itemImageVisibleWhere+`
 ORDER BY `+itemImagePriority+` LIMIT 1`, actor.UserID, actor.SessionID, item, imageType, index))
	if err != nil {
		return domain.ItemImage{}, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.ItemImage{}, err
	}
	return value, nil
}

// ResolveItemImageSources lists every usable source of one slot in
// selection order, so a caller can fall back when the best one cannot be
// read. A slot holds at most one row per source kind.
func (s *Store) ResolveItemImageSources(parent context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error) {
	if !domain.ValidItemImageSlot(imageType, index) {
		return nil, domain.ErrNotFound
	}
	ctx, cancel, err := readerContext(parent, actor, item)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT `+itemImageColumns+` `+itemImageVisibleItem+`
 JOIN item_images g ON g.item_id=i.id AND g.library_id=i.library_id AND g.image_type=$4 AND g.image_index=$5
  AND (g.root_id IS NOT NULL OR g.content_sha256 IS NOT NULL)
 LEFT JOIN library_roots r ON r.id=g.root_id AND r.library_id=g.library_id
 WHERE `+itemImageVisibleWhere+`
 ORDER BY `+itemImagePriority+` LIMIT 4`, actor.UserID, actor.SessionID, item, imageType, index)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]domain.ItemImage, 0, 4)
	for rows.Next() {
		value, err := scanItemImage(rows)
		if err != nil {
			return nil, storageError(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, domain.ErrNotFound
	}
	return result, nil
}

// ItemImageSummaries returns, for every visible item among itemIDs, the
// selected usable source of each image slot in one statement, so a listing
// page costs a single read (no per-item queries). Visibility is userID's
// library grant, evaluated in the same statement as the image rows like the
// catalog listings; invisible and missing items are absent. Chapter slots
// and gallery indexes from galleryMax on are left out.
func (s *Store) ItemImageSummaries(ctx context.Context, userID string, itemIDs []string, galleryMax int) (map[string][]domain.ItemImageSummary, error) {
	if ctx == nil || !domain.ValidID(userID) || len(itemIDs) > domain.ItemImageSummaryItemsMax ||
		galleryMax < 1 || galleryMax > domain.ItemImageSummaryGalleryMax {
		return nil, domain.ErrInvalid
	}
	for _, id := range itemIDs {
		if !domain.ValidID(id) {
			return nil, domain.ErrInvalid
		}
	}
	out := make(map[string][]domain.ItemImageSummary, len(itemIDs))
	if len(itemIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, browsePrincipalSQL+`
SELECT DISTINCT ON (g.item_id,g.image_type,g.image_index) g.item_id::text,g.image_type,g.image_index,g.id::text,g.updated_at,
 g.content_sha256,g.width,g.height,g.source_mtime_unix_nano,g.source_size
 FROM principal u JOIN items i ON i.id=ANY(@ids::uuid[])
 JOIN item_images g ON g.item_id=i.id AND g.library_id=i.library_id AND g.image_type<>'Chapter' AND g.image_index<@gallery
  AND (g.root_id IS NOT NULL OR g.content_sha256 IS NOT NULL)
 WHERE `+itemVisibleSQL("i.library_id", "i.id")+`
 ORDER BY g.item_id,g.image_type,g.image_index,`+itemImagePriority, pgx.NamedArgs{"user": userID, "ids": itemIDs, "gallery": galleryMax})
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.ItemImageSummary
		var width, height *int32
		if err := rows.Scan(&v.ItemID, &v.Type, &v.Index, &v.ImageID, &v.UpdatedAt, &v.ContentSHA256, &width, &height, &v.SourceModifiedUnixNano, &v.SourceSize); err != nil {
			return nil, storageError(err)
		}
		v.UpdatedAt = v.UpdatedAt.UTC()
		if width != nil && height != nil {
			v.Width, v.Height = int(*width), int(*height)
		}
		out[v.ItemID] = append(out[v.ItemID], v)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return out, nil
}

func readItemImageSlot(ctx context.Context, tx pgx.Tx, item, imageType string, index int, kind string, lock bool) (domain.ItemImage, error) {
	query := `SELECT ` + itemImageColumns + ` FROM item_images g LEFT JOIN library_roots r ON r.id=g.root_id AND r.library_id=g.library_id
 WHERE g.item_id=$1::uuid AND g.image_type=$2 AND g.image_index=$3 AND g.source_kind=$4`
	if lock {
		query += ` FOR UPDATE OF g`
	}
	value, err := scanItemImage(tx.QueryRow(ctx, query, item, imageType, index, kind))
	return value, storageError(err)
}

// lockImageItem serializes image writes per item so lock hand-over within a
// slot is atomic. It does not check visibility; writers are authorized first.
func lockImageItem(ctx context.Context, tx pgx.Tx, item string) error {
	var library string
	return storageError(tx.QueryRow(ctx, `SELECT library_id::text FROM items WHERE id=$1::uuid FOR NO KEY UPDATE`, item).Scan(&library))
}

type itemImageAudit struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Index         int    `json:"index"`
	SourceKind    string `json:"sourceKind"`
	Reference     string `json:"reference"`
	Locked        bool   `json:"locked"`
	ContentSHA256 string `json:"contentSha256,omitempty"`
}

// Audit rows never carry paths or URLs; the reference kind is enough to tell
// a file from a fetched image without exposing library layout.
func itemImageAuditState(value domain.ItemImage) itemImageAudit {
	state := itemImageAudit{ID: value.ID, Type: value.Type, Index: value.Index, SourceKind: value.SourceKind, Reference: "root", Locked: value.Locked}
	if value.RemoteURL != "" {
		state.Reference = "url"
	}
	if value.Content != nil {
		state.ContentSHA256 = hex.EncodeToString(value.Content.SHA256)
	}
	return state
}

func sameItemImageSource(old domain.ItemImage, in domain.ItemImageInput) bool {
	return old.RootID == in.RootID && old.RelativePath == in.RelativePath && old.RemoteURL == in.RemoteURL &&
		equalInt64(old.SourceModifiedUnixNano, in.SourceModifiedUnixNano) && equalInt64(old.SourceSize, in.SourceSize)
}

func equalInt64(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func equalItemImageContent(a, b *domain.ItemImageContent) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	colors := a.AverageColor == nil && b.AverageColor == nil || a.AverageColor != nil && b.AverageColor != nil && *a.AverageColor == *b.AverageColor
	return bytes.Equal(a.SHA256, b.SHA256) && a.Width == b.Width && a.Height == b.Height && a.Format == b.Format &&
		a.Bytes == b.Bytes && colors && a.FetchedAt.Equal(b.FetchedAt)
}

// upsertItemImage merges one observation into its (item, type, index,
// source kind) row inside the caller's transaction. Only manual input may
// change a locked row or the lock itself; a refresh without content keeps
// the stored content while the source identity is unchanged. Manual changes
// are audited against actor; scanner callers pass a zero actor.
func upsertItemImage(ctx context.Context, tx pgx.Tx, actor domain.Actor, in domain.ItemImageInput) (domain.ItemImageUpsert, error) {
	if !domain.ValidItemImageInput(in) {
		return domain.ItemImageUpsert{}, domain.ErrInvalid
	}
	if err := lockImageItem(ctx, tx, in.ItemID); err != nil {
		return domain.ItemImageUpsert{}, err
	}
	old, err := readItemImageSlot(ctx, tx, in.ItemID, in.Type, in.Index, in.SourceKind, true)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ItemImageUpsert{}, err
	}
	if exists && old.Locked && !in.Manual {
		return domain.ItemImageUpsert{Image: old, Skipped: true}, nil
	}
	content := in.Content
	if content == nil && exists && sameItemImageSource(old, in) {
		content = old.Content
	}
	locked := in.Locked
	if !in.Manual {
		locked = exists && old.Locked
	}
	if exists && sameItemImageSource(old, in) && equalItemImageContent(old.Content, content) && old.Locked == locked {
		return domain.ItemImageUpsert{Image: old}, nil
	}
	if locked && in.Manual {
		if err := releaseSlotLocks(ctx, tx, actor, in.ItemID, in.Type, in.Index, in.SourceKind); err != nil {
			return domain.ItemImageUpsert{}, err
		}
	}
	var digest []byte
	var width, height, color *int
	var format *string
	var size *int64
	var fetched *time.Time
	if content != nil {
		digest, format, size, fetched = content.SHA256, &content.Format, &content.Bytes, &content.FetchedAt
		if content.Width != 0 {
			width, height = &content.Width, &content.Height
		}
		color = content.AverageColor
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,
 source_mtime_unix_nano,source_size,content_sha256,width,height,format,byte_size,average_color,fetched_at,locked)
 SELECT i.id,i.library_id,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17 FROM items i WHERE i.id=$1::uuid
 ON CONFLICT ON CONSTRAINT item_images_slot DO UPDATE SET root_id=EXCLUDED.root_id,relative_path=EXCLUDED.relative_path,remote_url=EXCLUDED.remote_url,
 source_mtime_unix_nano=EXCLUDED.source_mtime_unix_nano,source_size=EXCLUDED.source_size,content_sha256=EXCLUDED.content_sha256,
 width=EXCLUDED.width,height=EXCLUDED.height,format=EXCLUDED.format,byte_size=EXCLUDED.byte_size,average_color=EXCLUDED.average_color,
 fetched_at=EXCLUDED.fetched_at,locked=EXCLUDED.locked,updated_at=greatest(clock_timestamp(),item_images.created_at)
 RETURNING id::text`, in.ItemID, in.Type, in.Index, in.SourceKind, in.RootID, in.RelativePath, in.RemoteURL,
		in.SourceModifiedUnixNano, in.SourceSize, digest, width, height, format, size, color, fetched, locked).Scan(&id)
	if err != nil {
		return domain.ItemImageUpsert{}, storageError(err)
	}
	value, err := readItemImageSlot(ctx, tx, in.ItemID, in.Type, in.Index, in.SourceKind, false)
	if err != nil {
		return domain.ItemImageUpsert{}, err
	}
	if in.Manual {
		event, before := "image.added", any(nil)
		if exists {
			event, before = "image.replaced", itemImageAuditState(old)
		}
		if err := auditAccount(ctx, tx, actor, event, in.ItemID, before, itemImageAuditState(value)); err != nil {
			return domain.ItemImageUpsert{}, err
		}
	}
	return domain.ItemImageUpsert{Image: value, Created: !exists}, nil
}

// releaseSlotLocks clears the lock of any other source in the slot so the
// partial unique index keeps a single locked row per slot.
func releaseSlotLocks(ctx context.Context, tx pgx.Tx, actor domain.Actor, item, imageType string, index int, keep string) error {
	rows, err := tx.Query(ctx, `SELECT `+itemImageColumns+` FROM item_images g LEFT JOIN library_roots r ON r.id=g.root_id AND r.library_id=g.library_id
 WHERE g.item_id=$1::uuid AND g.image_type=$2 AND g.image_index=$3 AND g.locked AND g.source_kind<>$4 FOR UPDATE OF g`, item, imageType, index, keep)
	if err != nil {
		return storageError(err)
	}
	released := make([]domain.ItemImage, 0, 1)
	for rows.Next() {
		value, err := scanItemImage(rows)
		if err != nil {
			rows.Close()
			return storageError(err)
		}
		released = append(released, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return storageError(err)
	}
	for _, value := range released {
		if _, err := tx.Exec(ctx, `UPDATE item_images SET locked=false,updated_at=greatest(clock_timestamp(),created_at) WHERE id=$1::uuid`, value.ID); err != nil {
			return storageError(err)
		}
		after := value
		after.Locked = false
		if err := auditAccount(ctx, tx, actor, "image.unlocked", item, itemImageAuditState(value), itemImageAuditState(after)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) imageWriter(ctx context.Context, actor domain.Actor) (pgx.Tx, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	return tx, err
}

// UpsertItemImage records a manual or administrator-driven observation.
// Scanner ingestion runs upsertItemImage inside its own fenced transaction.
func (s *Store) UpsertItemImage(ctx context.Context, actor domain.Actor, in domain.ItemImageInput) (domain.ItemImageUpsert, error) {
	if !domain.ValidItemImageInput(in) {
		return domain.ItemImageUpsert{}, domain.ErrInvalid
	}
	tx, err := s.imageWriter(ctx, actor)
	if err != nil {
		return domain.ItemImageUpsert{}, err
	}
	defer tx.Rollback(ctx)
	result, err := upsertItemImage(ctx, tx, actor, in)
	if err != nil {
		return domain.ItemImageUpsert{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// SetItemImageLock locks or unlocks one source of a slot. Locking hands the
// slot lock over from any other source. Changes are audited.
func (s *Store) SetItemImageLock(ctx context.Context, actor domain.Actor, item, imageType string, index int, kind string, locked bool) (domain.ItemImage, error) {
	if !domain.ValidID(item) || !domain.ValidItemImageSlot(imageType, index) || !domain.ValidItemImageSourceKind(kind) {
		return domain.ItemImage{}, domain.ErrInvalid
	}
	tx, err := s.imageWriter(ctx, actor)
	if err != nil {
		return domain.ItemImage{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockImageItem(ctx, tx, item); err != nil {
		return domain.ItemImage{}, err
	}
	old, err := readItemImageSlot(ctx, tx, item, imageType, index, kind, true)
	if err != nil {
		return domain.ItemImage{}, err
	}
	if old.Locked == locked {
		return old, storageError(tx.Commit(ctx))
	}
	if locked {
		if err := releaseSlotLocks(ctx, tx, actor, item, imageType, index, kind); err != nil {
			return domain.ItemImage{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE item_images SET locked=$2,updated_at=greatest(clock_timestamp(),created_at) WHERE id=$1::uuid`, old.ID, locked); err != nil {
		return domain.ItemImage{}, storageError(err)
	}
	value, err := readItemImageSlot(ctx, tx, item, imageType, index, kind, false)
	if err != nil {
		return domain.ItemImage{}, err
	}
	event := "image.unlocked"
	if locked {
		event = "image.locked"
	}
	if err := auditAccount(ctx, tx, actor, event, item, itemImageAuditState(old), itemImageAuditState(value)); err != nil {
		return domain.ItemImage{}, err
	}
	return value, storageError(tx.Commit(ctx))
}

// DeleteItemImage removes one reference row and audits it. The referenced
// file and the stored content are never touched (G40.11); unreferenced
// variants age out through the variant index.
func (s *Store) DeleteItemImage(ctx context.Context, actor domain.Actor, item, imageType string, index int, kind string) error {
	if !domain.ValidID(item) || !domain.ValidItemImageSlot(imageType, index) || !domain.ValidItemImageSourceKind(kind) {
		return domain.ErrInvalid
	}
	tx, err := s.imageWriter(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockImageItem(ctx, tx, item); err != nil {
		return err
	}
	old, err := readItemImageSlot(ctx, tx, item, imageType, index, kind, true)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM item_images WHERE id=$1::uuid`, old.ID); err != nil {
		return storageError(err)
	}
	if err := auditAccount(ctx, tx, actor, "image.deleted", item, itemImageAuditState(old), nil); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// The variant index is internal cache bookkeeping keyed by content digest
// and carries no catalog data, so it takes no actor. Callers must have
// authorized the image request that produced or used the variant.

func (s *Store) PutImageVariant(ctx context.Context, content, key [32]byte, size int64) error {
	if ctx == nil || size <= 0 {
		return domain.ErrInvalid
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO image_variants(content_sha256,variant_key,byte_size) VALUES($1,$2,$3)
 ON CONFLICT(content_sha256,variant_key) DO UPDATE SET byte_size=EXCLUDED.byte_size,last_access=greatest(clock_timestamp(),image_variants.last_access)`, content[:], key[:], size)
	return storageError(err)
}

// TouchImageVariant refreshes last_access at most once a minute, matching
// the store's mtime throttle so hot variants do not write on every hit.
func (s *Store) TouchImageVariant(ctx context.Context, content, key [32]byte) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	var found bool
	err := s.Pool.QueryRow(ctx, `WITH touched AS (UPDATE image_variants SET last_access=clock_timestamp()
 WHERE content_sha256=$1 AND variant_key=$2 AND last_access<clock_timestamp()-interval '1 minute' RETURNING 1)
 SELECT EXISTS(SELECT 1 FROM image_variants WHERE content_sha256=$1 AND variant_key=$2)`, content[:], key[:]).Scan(&found)
	if err != nil {
		return storageError(err)
	}
	if !found {
		return domain.ErrNotFound
	}
	return nil
}

// ListOldestImageVariants returns the least recently used entries first.
func (s *Store) ListOldestImageVariants(ctx context.Context, limit int) ([]domain.ImageVariant, error) {
	if ctx == nil || !domain.ValidImageVariantLimit(limit) {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT content_sha256,variant_key,byte_size,created_at,last_access FROM image_variants
 ORDER BY last_access,content_sha256,variant_key LIMIT $1`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]domain.ImageVariant, 0, min(limit, 64))
	for rows.Next() {
		var content, key []byte
		var value domain.ImageVariant
		if err := rows.Scan(&content, &key, &value.Bytes, &value.CreatedAt, &value.LastAccess); err != nil {
			return nil, storageError(err)
		}
		if len(content) != 32 || len(key) != 32 {
			return nil, domain.ErrDatabase
		}
		copy(value.ContentSHA256[:], content)
		copy(value.VariantKey[:], key)
		value.CreatedAt, value.LastAccess = value.CreatedAt.UTC(), value.LastAccess.UTC()
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}

// DeleteImageVariant removes an index entry only if it has not been used
// since it was listed, so eviction never drops a variant touched meanwhile.
// It reports whether the entry was removed; the caller then deletes the file.
func (s *Store) DeleteImageVariant(ctx context.Context, value domain.ImageVariant) (bool, error) {
	if ctx == nil {
		return false, domain.ErrInvalid
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM image_variants WHERE content_sha256=$1 AND variant_key=$2 AND last_access=$3`,
		value.ContentSHA256[:], value.VariantKey[:], value.LastAccess)
	if err != nil {
		return false, storageError(err)
	}
	return tag.RowsAffected() == 1, nil
}
