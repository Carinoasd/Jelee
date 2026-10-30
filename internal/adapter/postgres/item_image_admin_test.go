package postgres

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func itemImageUploadInput(item, label string) domain.ItemImageInput {
	return domain.ItemImageInput{ItemID: item, Type: "Primary", Content: itemImageContent(label)}
}

func itemImageUploadDigest(t *testing.T, f jobFixture, item string) []byte {
	t.Helper()
	var digest []byte
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT content_sha256 FROM item_images WHERE item_id=$1::uuid AND source_kind='upload'`, item).Scan(&digest); err != nil {
		t.Fatal("read upload row", err)
	}
	return digest
}

func TestItemImageUploadConfirmAuditAndLock(t *testing.T) {
	f, item := itemImageFixture(t)
	first, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "first"), false)
	if err != nil || !first.Created || first.Image.SourceKind != domain.ImageSourceUpload || !first.Image.Locked || !first.Image.Manual ||
		first.Image.RootID != "" || first.Image.RemoteURL != "" || len(first.Released) != 0 {
		t.Fatal("upload into an empty slot", err)
	}
	if itemImageAuditCount(t, f, "image.added", item) != 1 {
		t.Fatal("upload not audited as image.added")
	}
	// Replacing needs the confirmation and changes nothing without it.
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "second"), false); !errors.Is(err, domain.ErrConfirmationRequired) {
		t.Fatal("replacement without confirmation", err)
	}
	if !bytes.Equal(itemImageUploadDigest(t, f, item), itemImageDigest("first")) || itemImageAuditCount(t, f, "image.replaced", item) != 0 {
		t.Fatal("unconfirmed replacement changed the slot")
	}
	// Any row makes the slot occupied, not only an earlier upload.
	fresh := metadataItem(t, f)
	itemImageUpsert(t, f, itemImageLocal(f, fresh, "poster.jpg"))
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(fresh, "x"), false); !errors.Is(err, domain.ErrConfirmationRequired) {
		t.Fatal("upload over a library image without confirmation", err)
	}

	local := itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	if local.Skipped || local.Image.Locked {
		t.Fatal("library image beside a locked upload")
	}
	// The locked upload wins over the library file.
	if v, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != nil || v.SourceKind != domain.ImageSourceUpload {
		t.Fatal("locked upload does not win", err)
	}
	second, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "second"), true)
	if err != nil || second.Created || len(second.Released) != 1 || !bytes.Equal(second.Released[0][:], itemImageDigest("first")) {
		t.Fatal("confirmed replacement", err)
	}
	var before, after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT before_state::text,after_state::text FROM audit_logs WHERE event='image.replaced' AND target_id=$1::uuid`, item).Scan(&before, &after); err != nil {
		t.Fatal("replacement audit missing", err)
	}
	if !strings.Contains(before, `"sourceKind": "upload"`) || !strings.Contains(before, hex.EncodeToString(itemImageDigest("first"))) ||
		!strings.Contains(after, `"locked": true`) || !strings.Contains(after, `"reference": "store"`) {
		t.Fatal("replacement audit state", before, after)
	}

	// Locking the library file hands the lock over; it then wins.
	if v, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, true); err != nil || !v.Locked {
		t.Fatal("lock library image", err)
	}
	if v, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != nil || v.SourceKind != domain.ImageSourceLocal {
		t.Fatal("locked library image does not win", err)
	}
	var lockedRows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid AND locked`, item).Scan(&lockedRows); err != nil || lockedRows != 1 {
		t.Fatal("slot holds more than one lock", lockedRows, err)
	}
	// A scan refresh never rewrites the locked row.
	moved := itemImageLocal(f, item, "other.jpg")
	if r := itemImageUpsert(t, f, moved); !r.Skipped || r.Image.RelativePath != "movie/poster.jpg" {
		t.Fatal("refresh rewrote a locked image")
	}
	// Unlocked, an upload ranks after every automatic source.
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, false); err != nil {
		t.Fatal(err)
	}
	remote := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/a.jpg")
	remote.Content = itemImageContent("remote")
	itemImageUpsert(t, f, remote)
	sources, err := f.s.ResolveItemImageSources(f.ctx, f.a, item, "Primary", 0)
	if err != nil || len(sources) != 3 || sources[0].SourceKind != domain.ImageSourceLocal || sources[2].SourceKind != domain.ImageSourceUpload {
		t.Fatal("unlocked upload order", len(sources), err)
	}
	// A new upload locks the slot again and releases the library lock audit.
	unlocked := itemImageAuditCount(t, f, "image.unlocked", item)
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "third"), true); err != nil {
		t.Fatal(err)
	}
	if itemImageAuditCount(t, f, "image.unlocked", item) != unlocked+1 {
		t.Fatal("handed-over lock not audited")
	}
	if v, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != nil || v.SourceKind != domain.ImageSourceUpload || !v.Locked {
		t.Fatal("new upload does not hold the lock", err)
	}

	// Content another row still references is not released.
	shared := metadataItem(t, f)
	copyRemote := itemImageRemote(shared, domain.ImageSourceRemote, "https://example.com/b.jpg")
	copyRemote.Content = itemImageContent("third")
	itemImageUpsert(t, f, copyRemote)
	if change, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "fourth"), true); err != nil || len(change.Released) != 0 {
		t.Fatal("released content another item references", err)
	}

	// Uploads need content and stay manual; invalid slots never reach SQL.
	for name, in := range map[string]domain.ItemImageInput{
		"no content": {ItemID: item, Type: "Primary"},
		"bad slot":   {ItemID: item, Type: "Primary", Index: 1, Content: itemImageContent("y")},
		"with path":  {ItemID: item, Type: "Primary", RootID: f.registration.RootID, RelativePath: "a.jpg", Content: itemImageContent("y")},
	} {
		if _, err := f.s.UploadItemImage(f.ctx, f.a, in, true); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, domain.ItemImageInput{ItemID: item, Type: "Primary", SourceKind: domain.ImageSourceUpload, Content: itemImageContent("z")}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("non-manual upload accepted", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind) SELECT id,library_id,'Logo',0,'upload' FROM items WHERE id=$1::uuid`, item); itemImageSQLState(err) != "23514" {
		t.Fatal("upload row without content accepted", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,content_sha256,format,byte_size,fetched_at,manual)
 SELECT id,library_id,'Logo',0,'upload',$2,'png',1,now(),false FROM items WHERE id=$1::uuid`, item, itemImageDigest("m")); itemImageSQLState(err) != "23514" {
		t.Fatal("non-manual upload row accepted", err)
	}
}

func TestItemImageDeleteSlotRows(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	remote := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/a.jpg")
	remote.Content = itemImageContent("remote")
	itemImageUpsert(t, f, remote)
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "upload"), true); err != nil {
		t.Fatal(err)
	}
	other := metadataItem(t, f)
	itemImageUpsert(t, f, itemImageLocal(f, other, "poster.jpg"))

	change, err := f.s.DeleteItemImages(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceRemote)
	if err != nil || len(change.Removed) != 1 || change.Removed[0].SourceKind != domain.ImageSourceRemote ||
		len(change.Released) != 1 || !bytes.Equal(change.Released[0][:], itemImageDigest("remote")) {
		t.Fatal("delete one source", err)
	}
	change, err = f.s.DeleteItemImages(f.ctx, f.a, item, "Primary", 0, "")
	if err != nil || len(change.Removed) != 2 || change.Removed[0].SourceKind != domain.ImageSourceUpload {
		t.Fatal("delete every source, locked first", err)
	}
	// The library row had no stored content; only the upload is released.
	if len(change.Released) != 1 || !bytes.Equal(change.Released[0][:], itemImageDigest("upload")) {
		t.Fatal("released digests", change.Released)
	}
	if itemImageAuditCount(t, f, "image.deleted", item) != 3 {
		t.Fatal("deletions not audited per row")
	}
	if _, err := f.s.DeleteItemImages(f.ctx, f.a, item, "Primary", 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("empty slot deleted", err)
	}
	if rows, err := f.s.ListItemImages(f.ctx, f.a, other); err != nil || len(rows) != 1 {
		t.Fatal("deletion touched another item", err)
	}
	if _, err := f.s.DeleteItemImages(f.ctx, f.a, item, "Primary", 0, "tmdb"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown source accepted", err)
	}

	var digest [32]byte
	copy(digest[:], itemImageDigest("upload"))
	for _, key := range [][32]byte{{1}, {2}} {
		if err := f.s.PutImageVariant(f.ctx, digest, key, 10); err != nil {
			t.Fatal(err)
		}
	}
	var kept [32]byte
	copy(kept[:], itemImageDigest("kept"))
	if err := f.s.PutImageVariant(f.ctx, kept, [32]byte{1}, 10); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.DeleteImageVariantsOf(f.ctx, digest); err != nil || n != 2 {
		t.Fatal("delete variants of one original", n, err)
	}
	var left int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM image_variants WHERE content_sha256=$1`, kept[:]).Scan(&left); err != nil || left != 1 {
		t.Fatal("deleted variants of another original", left, err)
	}
}

func TestItemImageManagementAuthorization(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	reader := imageRepositoryActor(t, f, "item-image-admin-reader", access.ClientNative)
	imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
	// A viewer with a library grant is refused before the item is read,
	// for a visible and for a missing item alike.
	for _, id := range []string{item, "10000000-0000-4000-8000-000000000099"} {
		if _, err := f.s.UploadItemImage(f.ctx, reader, itemImageUploadInput(id, "r"), true); !errors.Is(err, domain.ErrForbidden) {
			t.Fatal("viewer uploaded", err)
		}
		if _, err := f.s.DeleteItemImages(f.ctx, reader, id, "Primary", 0, ""); !errors.Is(err, domain.ErrForbidden) {
			t.Fatal("viewer deleted", err)
		}
		if _, err := f.s.SetItemImageLock(f.ctx, reader, id, "Primary", 0, domain.ImageSourceLocal, true); !errors.Is(err, domain.ErrForbidden) {
			t.Fatal("viewer locked", err)
		}
	}
	// An administrator restricted by content rules is answered for a hidden
	// item exactly like for a missing one, and nothing changes.
	imageRepositoryExec(t, f, `UPDATE access_policy SET restrict_admins=true`)
	imageRepositoryExec(t, f, `UPDATE users SET content_filtered=true WHERE id=$1::uuid`, f.a.UserID)
	imageRepositoryExec(t, f, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'hide')`, f.a.UserID, item)
	audits := itemImageAuditCount(t, f, "image.deleted", item) + itemImageAuditCount(t, f, "image.added", item) + itemImageAuditCount(t, f, "image.locked", item)
	for _, id := range []string{item, "10000000-0000-4000-8000-000000000099"} {
		if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(id, "h"), true); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("upload to a hidden or missing item", err)
		}
		if _, err := f.s.DeleteItemImages(f.ctx, f.a, id, "Primary", 0, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("delete on a hidden or missing item", err)
		}
		if _, err := f.s.SetItemImageLock(f.ctx, f.a, id, "Primary", 0, domain.ImageSourceLocal, true); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("lock on a hidden or missing item", err)
		}
		if _, err := f.s.ListItemImages(f.ctx, f.a, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("list of a hidden or missing item", err)
		}
	}
	var rows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid AND NOT locked`, item).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("hidden item changed", rows, err)
	}
	if after := itemImageAuditCount(t, f, "image.deleted", item) + itemImageAuditCount(t, f, "image.added", item) + itemImageAuditCount(t, f, "image.locked", item); after != audits {
		t.Fatal("refused changes were audited")
	}
}

func TestItemImageUploadMigrationRefusesRetainedUploads(t *testing.T) {
	f, item := itemImageFixture(t)
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "kept"), false); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "item_image_uploads", "a retained upload was downgraded")
	var rows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_images WHERE source_kind='upload'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("refused downgrade removed the upload", rows, err)
	}
}

func TestItemImageUploadMigrationRoundTrip(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	want := migrationVersion(t, "item_image_uploads")
	jobMetricMigration(t, f, "down", want-1)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,content_sha256,format,byte_size,fetched_at)
 SELECT id,library_id,'Logo',0,'upload',$2,'png',1,now() FROM items WHERE id=$1::uuid`, item, itemImageDigest("u")); itemImageSQLState(err) != "23514" {
		t.Fatal("schema below uploads accepted an upload row", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if _, err := f.s.UploadItemImage(f.ctx, f.a, itemImageUploadInput(item, "after"), true); err != nil {
		t.Fatal("upload after round trip", err)
	}
	if rows, err := f.s.ListItemImages(f.ctx, f.a, item); err != nil || len(rows) != 2 {
		t.Fatal("round trip lost the library row", err)
	}
}
