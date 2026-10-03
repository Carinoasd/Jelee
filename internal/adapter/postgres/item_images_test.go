package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func itemImageFixture(t *testing.T) (jobFixture, string) {
	t.Helper()
	f := newJobFixture(t)
	return f, metadataItem(t, f)
}

func itemImageDigest(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func itemImageContent(label string) *domain.ItemImageContent {
	color := 0x336699
	return &domain.ItemImageContent{SHA256: itemImageDigest(label), Width: 640, Height: 960, Format: "jpeg", Bytes: 4096,
		AverageColor: &color, FetchedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
}

func itemImageLocal(f jobFixture, item, name string) domain.ItemImageInput {
	mtime, size := int64(1_700_000_000_000_000_000), int64(2048)
	return domain.ItemImageInput{ItemID: item, Type: "Primary", SourceKind: domain.ImageSourceLocal, RootID: f.registration.RootID,
		RelativePath: "movie/" + name, SourceModifiedUnixNano: &mtime, SourceSize: &size}
}

func itemImageRemote(item, kind, url string) domain.ItemImageInput {
	return domain.ItemImageInput{ItemID: item, Type: "Primary", SourceKind: kind, RemoteURL: url}
}

func itemImageUpsert(t *testing.T, f jobFixture, in domain.ItemImageInput) domain.ItemImageUpsert {
	t.Helper()
	result, err := f.s.UpsertItemImage(f.ctx, f.a, in)
	if err != nil {
		t.Fatalf("upsert item image %s/%s: %v", in.Type, in.SourceKind, err)
	}
	return result
}

func itemImageSQLState(err error) string {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return pgerr.Code
	}
	return ""
}

func itemImageAuditCount(t *testing.T, f jobFixture, event, item string) int {
	t.Helper()
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event=$1 AND target_id=$2::uuid`, event, item).Scan(&count); err != nil {
		t.Fatal("count image audit rows")
	}
	return count
}

func TestItemImageSchemaConstraints(t *testing.T) {
	f, item := itemImageFixture(t)
	other, err := f.s.RegisterLibrary(f.ctx, "foreign", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,format,byte_size,fetched_at,width,height,source_mtime_unix_nano,source_size)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16)`
	type row struct {
		name, imageType string
		index           int
		kind, root      string
		path, url       string
		library         string
		digest          []byte
		format          *string
		size            *int64
		fetched         *time.Time
		width, height   *int
		mtime, srcSize  *int64
		want            string
	}
	root, lib := f.registration.RootID, f.registration.Library.ID
	jpeg, bytesN, now, w, one := "jpeg", int64(10), time.Now(), 10, int64(1)
	long := "https://example.com/" + strings.Repeat("a", 2048)
	for _, c := range []row{
		{name: "type", imageType: "Poster", kind: "local", root: root, path: "a.jpg", want: "23514"},
		{name: "negative index", imageType: "Backdrop", index: -1, kind: "local", root: root, path: "a.jpg", want: "23514"},
		{name: "index above max", imageType: "Backdrop", index: 10000, kind: "local", root: root, path: "a.jpg", want: "23514"},
		{name: "index on single type", imageType: "Primary", index: 1, kind: "local", root: root, path: "a.jpg", want: "23514"},
		{name: "kind", imageType: "Primary", kind: "tmdb", url: "https://example.com/a.jpg", want: "23514"},
		{name: "local with url", imageType: "Primary", kind: "local", root: root, path: "a.jpg", url: "https://example.com/a.jpg", want: "23514"},
		{name: "local without root", imageType: "Primary", kind: "local", want: "23514"},
		{name: "local not image", imageType: "Primary", kind: "local", root: root, path: "a.mkv", want: "23514"},
		{name: "remote with root", imageType: "Primary", kind: "remote", root: root, path: "a.jpg", url: "https://example.com/a.jpg", want: "23514"},
		{name: "remote without url", imageType: "Primary", kind: "remote", want: "23514"},
		{name: "nfo both", imageType: "Primary", kind: "nfo", root: root, path: "a.jpg", url: "https://example.com/a.jpg", want: "23514"},
		{name: "nfo neither", imageType: "Primary", kind: "nfo", want: "23514"},
		{name: "embedded url", imageType: "Primary", kind: "embedded", url: "https://example.com/a.jpg", want: "23514"},
		{name: "http url", imageType: "Primary", kind: "remote", url: "http://example.com/a.jpg", want: "23514"},
		{name: "url credentials", imageType: "Primary", kind: "remote", url: "https://user@example.com/a.jpg", want: "23514"},
		{name: "url whitespace", imageType: "Primary", kind: "remote", url: "https://example.com/a b.jpg", want: "23514"},
		{name: "url too long", imageType: "Primary", kind: "remote", url: long, want: "23514"},
		{name: "path parent", imageType: "Primary", kind: "local", root: root, path: "../a.jpg", want: "23514"},
		{name: "path absolute", imageType: "Primary", kind: "local", root: root, path: "/etc/a.jpg", want: "23514"},
		{name: "path backslash", imageType: "Primary", kind: "local", root: root, path: `a\b.jpg`, want: "23514"},
		{name: "path pair", imageType: "Primary", kind: "local", path: "a.jpg", want: "23514"},
		{name: "foreign root", imageType: "Primary", kind: "local", root: other.RootID, path: "a.jpg", want: "23503"},
		{name: "foreign library", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", library: other.Library.ID, want: "23503"},
		{name: "partial content", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", digest: itemImageDigest("x"), want: "23514"},
		{name: "content without digest", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", format: &jpeg, size: &bytesN, fetched: &now, want: "23514"},
		{name: "half dimensions", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", digest: itemImageDigest("x"), format: &jpeg, size: &bytesN, fetched: &now, width: &w, want: "23514"},
		{name: "short digest", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", digest: []byte{1}, format: &jpeg, size: &bytesN, fetched: &now, want: "23514"},
		{name: "url attributes", imageType: "Primary", kind: "remote", url: "https://example.com/a.jpg", mtime: &one, srcSize: &one, want: "23514"},
		{name: "half attributes", imageType: "Primary", kind: "local", root: root, path: "a.jpg", mtime: &one, want: "23514"},
		{name: "valid local", imageType: "Backdrop", index: 3, kind: "local", root: root, path: "Movie/Fanart3.JPG", mtime: &one, srcSize: &one},
		{name: "valid nfo path", imageType: "Primary", kind: "nfo", root: root, path: "Movie/poster.png"},
		{name: "valid remote", imageType: "Primary", kind: "remote", url: "https://image.example.com/t/p/w500/a.jpg?x=1", digest: itemImageDigest("x"), format: &jpeg, size: &bytesN, fetched: &now, width: &w, height: &w},
		{name: "valid embedded", imageType: "Primary", kind: "embedded", root: root, path: "Movie/Film.mkv"},
	} {
		t.Run(c.name, func(t *testing.T) {
			library := lib
			if c.library != "" {
				library = c.library
			}
			_, err := f.s.Pool.Exec(f.ctx, insert, item, library, c.imageType, c.index, c.kind, c.root, c.path, c.url, c.digest, c.format, c.size, c.fetched, c.width, c.height, c.mtime, c.srcSize)
			if got := itemImageSQLState(err); got != c.want || c.want == "" && err != nil {
				t.Fatalf("constraint state=%q want %q err=%v", got, c.want, err)
			}
		})
	}
	// Unique slot per source kind, and one locked row per slot.
	if _, err := f.s.Pool.Exec(f.ctx, insert, item, lib, "Primary", 0, "nfo", root, "Movie/other.jpg", "", nil, nil, nil, nil, nil, nil, nil, nil); itemImageSQLState(err) != "23505" {
		t.Fatal("duplicate slot/source accepted", err)
	}
	imageRepositoryExec(t, f, `UPDATE item_images SET locked=true WHERE item_id=$1::uuid AND image_type='Primary' AND source_kind='nfo'`, item)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_images SET locked=true WHERE item_id=$1::uuid AND image_type='Primary' AND source_kind='remote'`, item); itemImageSQLState(err) != "23505" {
		t.Fatal("second locked row in one slot accepted", err)
	}
	// Repository validation mirrors the schema before any transaction.
	for _, in := range []domain.ItemImageInput{
		{ItemID: item, Type: "Primary", Index: 1, SourceKind: "local", RootID: root, RelativePath: "a.jpg"},
		{ItemID: item, Type: "Primary", SourceKind: "local", RootID: root, RelativePath: "a/../b.jpg"},
		{ItemID: item, Type: "Primary", SourceKind: "local", RootID: root, RelativePath: "a.txt"},
		{ItemID: item, Type: "Primary", SourceKind: "remote", RemoteURL: "http://example.com/a.jpg"},
		{ItemID: item, Type: "Primary", SourceKind: "remote", RemoteURL: "https://u:p@example.com/a.jpg"},
		{ItemID: item, Type: "Primary", SourceKind: "remote", RemoteURL: long},
		{ItemID: item, Type: "Primary", SourceKind: "nfo"},
		{ItemID: item, Type: "Primary", SourceKind: "remote", RemoteURL: "https://example.com/a.jpg", Locked: true},
		{ItemID: item, Type: "Primary", SourceKind: "remote", RemoteURL: "https://example.com/a.jpg", Content: &domain.ItemImageContent{SHA256: []byte{1}}},
	} {
		if _, err := f.s.UpsertItemImage(f.ctx, f.a, in); err != domain.ErrInvalid {
			t.Fatal("repository accepted invalid image input", err)
		}
	}
	foreign := itemImageLocal(f, item, "poster.jpg")
	foreign.RootID = other.RootID
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, foreign); err != domain.ErrInvalid {
		t.Fatal("repository accepted a root from another library", err)
	}
}

func TestItemImageUpsertMergeAndLock(t *testing.T) {
	f, item := itemImageFixture(t)
	scan := itemImageLocal(f, item, "poster.jpg")
	created := itemImageUpsert(t, f, scan)
	if !created.Created || created.Skipped || created.Image.Locked || created.Image.Content != nil || created.Image.RootPath == "" || created.Image.LibraryID != f.registration.Library.ID {
		t.Fatal("scan observation not created as unlocked reference")
	}
	if text := fmt.Sprintf("%v %#v", created.Image, created.Image); strings.Contains(text, "poster") || strings.Contains(text, created.Image.RootPath) {
		t.Fatal("image diagnostics expose paths")
	}
	scan.Content = itemImageContent("v1")
	fetched := itemImageUpsert(t, f, scan)
	if fetched.Created || fetched.Image.ID != created.Image.ID || fetched.Image.Content == nil || !bytes.Equal(fetched.Image.Content.SHA256, itemImageDigest("v1")) || *fetched.Image.Content.AverageColor != 0x336699 {
		t.Fatal("content not merged into existing row")
	}
	scan.Content = nil
	same := itemImageUpsert(t, f, scan)
	if same.Image.Content == nil || !same.Image.UpdatedAt.Equal(fetched.Image.UpdatedAt) {
		t.Fatal("refresh without content dropped content or rewrote an unchanged row")
	}
	changed := scan
	size := int64(9999)
	changed.SourceSize = &size
	moved := itemImageUpsert(t, f, changed)
	if moved.Image.Content != nil || *moved.Image.SourceSize != size {
		t.Fatal("changed source kept stale content")
	}
	if itemImageAuditCount(t, f, "image.added", item)+itemImageAuditCount(t, f, "image.replaced", item) != 0 {
		t.Fatal("scanner refresh wrote replacement audit")
	}

	// A manual lock survives every later non-manual refresh.
	manual := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/chosen.jpg")
	manual.Content, manual.Manual, manual.Locked = itemImageContent("chosen"), true, true
	chosen := itemImageUpsert(t, f, manual)
	if !chosen.Image.Locked || itemImageAuditCount(t, f, "image.added", item) != 1 {
		t.Fatal("manual locked image not stored or audited")
	}
	refresh := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/tmdb.jpg")
	skipped := itemImageUpsert(t, f, refresh)
	if !skipped.Skipped || skipped.Image.RemoteURL != "https://example.com/chosen.jpg" || !skipped.Image.Locked {
		t.Fatal("non-manual refresh replaced a locked image")
	}
	if _, err := f.s.UpsertItemImage(f.ctx, f.a, domain.ItemImageInput{ItemID: item, Type: "Primary", SourceKind: domain.ImageSourceRemote, RemoteURL: "https://example.com/x.jpg", Locked: true}); err != domain.ErrInvalid {
		t.Fatal("non-manual input set a lock")
	}
	replacement := manual
	replacement.RemoteURL, replacement.Content = "https://example.com/second.jpg", itemImageContent("second")
	replaced := itemImageUpsert(t, f, replacement)
	if replaced.Skipped || replaced.Image.RemoteURL != replacement.RemoteURL || !replaced.Image.Locked || itemImageAuditCount(t, f, "image.replaced", item) != 1 {
		t.Fatal("manual replacement of locked image failed or was not audited")
	}

	// Locking another source hands the slot lock over atomically.
	locked, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, true)
	if err != nil || !locked.Locked {
		t.Fatal("lock local image", err)
	}
	list, err := f.s.ListItemImages(f.ctx, f.a, item)
	if err != nil || len(list) != 2 || list[0].SourceKind != domain.ImageSourceLocal || !list[0].Locked || list[1].Locked {
		t.Fatal("lock hand-over left two locks or wrong order", err)
	}
	if itemImageAuditCount(t, f, "image.locked", item) != 1 || itemImageAuditCount(t, f, "image.unlocked", item) != 1 {
		t.Fatal("lock hand-over not audited")
	}
	again, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, true)
	if err != nil || !again.Locked || itemImageAuditCount(t, f, "image.locked", item) != 1 {
		t.Fatal("idempotent lock wrote audit", err)
	}
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceNFO, true); err != domain.ErrNotFound {
		t.Fatal("lock of missing source", err)
	}
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal, false); err != nil || itemImageAuditCount(t, f, "image.unlocked", item) != 2 {
		t.Fatal("unlock failed or unaudited", err)
	}
	var unaudited int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event LIKE 'image.%' AND (actor_id IS DISTINCT FROM $1::uuid OR before_state::text LIKE '%example.com%' OR after_state::text LIKE '%example.com%' OR after_state::text LIKE '%movie/%')`, f.a.UserID).Scan(&unaudited); err != nil || unaudited != 0 {
		t.Fatal("image audit lacks actor or exposes references", err)
	}
}

func TestItemImageResolvePriority(t *testing.T) {
	f, item := itemImageFixture(t)
	if _, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0); err != domain.ErrNotFound {
		t.Fatal("empty slot resolved", err)
	}
	embedded := domain.ItemImageInput{ItemID: item, Type: "Primary", SourceKind: domain.ImageSourceEmbedded, RootID: f.registration.RootID, RelativePath: "movie/Film.mkv"}
	remote := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/tmdb.jpg")
	nfoURL := itemImageRemote(item, domain.ImageSourceNFO, "https://example.com/nfo.jpg")
	local := itemImageLocal(f, item, "poster.jpg")
	for _, in := range []domain.ItemImageInput{embedded, remote, nfoURL, local} {
		itemImageUpsert(t, f, in)
	}
	expect := func(kind string) {
		t.Helper()
		value, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 0)
		if err != nil || value.SourceKind != kind {
			t.Fatalf("resolved %q want %q: %v", value.SourceKind, kind, err)
		}
	}
	expect(domain.ImageSourceLocal)
	if err := f.s.DeleteItemImage(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal); err != nil {
		t.Fatal(err)
	}
	// Unfetched URL references are not usable; embedded art is.
	expect(domain.ImageSourceEmbedded)
	remote.Content = itemImageContent("tmdb")
	itemImageUpsert(t, f, remote)
	expect(domain.ImageSourceRemote)
	nfoURL.Content = itemImageContent("nfo")
	itemImageUpsert(t, f, nfoURL)
	expect(domain.ImageSourceNFO)
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceEmbedded, true); err != nil {
		t.Fatal(err)
	}
	expect(domain.ImageSourceEmbedded)
	itemImageUpsert(t, f, local)
	expect(domain.ImageSourceEmbedded)
	list, err := f.s.ListItemImages(f.ctx, f.a, item)
	if err != nil || len(list) != 4 {
		t.Fatal("list all sources", err)
	}
	for i, kind := range []string{domain.ImageSourceEmbedded, domain.ImageSourceLocal, domain.ImageSourceNFO, domain.ImageSourceRemote} {
		if list[i].SourceKind != kind {
			t.Fatalf("list order %d=%s want %s", i, list[i].SourceKind, kind)
		}
	}
	// Galleries keep independent indexes.
	for index := range 3 {
		in := itemImageLocal(f, item, fmt.Sprintf("fanart%d.jpg", index))
		in.Type, in.Index = "Backdrop", index
		itemImageUpsert(t, f, in)
	}
	value, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Backdrop", 2)
	if err != nil || value.RelativePath != "movie/fanart2.jpg" {
		t.Fatal("gallery index resolved wrong image", err)
	}
	if _, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Backdrop", 3); err != domain.ErrNotFound {
		t.Fatal("missing gallery index resolved", err)
	}
	if _, err := f.s.ResolveItemImage(f.ctx, f.a, item, "Primary", 1); err != domain.ErrNotFound {
		t.Fatal("invalid slot resolved", err)
	}
}

func TestItemImageAuthorizationFilter(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	empty := metadataItem(t, f)
	for _, kind := range []access.ClientKind{access.ClientWeb, access.ClientNative} {
		reader := imageRepositoryActor(t, f, "item-image-"+string(kind), kind)
		if v, err := f.s.ListItemImages(f.ctx, reader, item); v != nil || err != domain.ErrNotFound {
			t.Fatal("reader without library grant listed images")
		}
		if v, err := f.s.ResolveItemImage(f.ctx, reader, item, "Primary", 0); v.ID != "" || err != domain.ErrNotFound {
			t.Fatal("reader without library grant resolved image")
		}
		imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
		if v, err := f.s.ListItemImages(f.ctx, reader, item); err != nil || len(v) != 1 {
			t.Fatal("granted reader cannot list", err)
		}
		if v, err := f.s.ListItemImages(f.ctx, reader, empty); err != nil || v == nil || len(v) != 0 {
			t.Fatal("visible item without images not an empty list", err)
		}
		if _, err := f.s.ResolveItemImage(f.ctx, reader, item, "Primary", 0); err != nil {
			t.Fatal("granted reader cannot resolve", err)
		}
		// Writes are administrator-only even with a library grant.
		if _, err := f.s.UpsertItemImage(f.ctx, reader, itemImageLocal(f, item, "x.jpg")); err != domain.ErrForbidden {
			t.Fatal("reader wrote an image", err)
		}
		if _, err := f.s.SetItemImageLock(f.ctx, reader, item, "Primary", 0, domain.ImageSourceLocal, true); err != domain.ErrForbidden {
			t.Fatal("reader locked an image", err)
		}
		if err := f.s.DeleteItemImage(f.ctx, reader, item, "Primary", 0, domain.ImageSourceLocal); err != domain.ErrForbidden {
			t.Fatal("reader deleted an image", err)
		}
		imageRepositoryExec(t, f, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, reader.SessionID)
		if _, err := f.s.ListItemImages(f.ctx, reader, item); err != domain.ErrNotFound {
			t.Fatal("revoked session listed images", err)
		}
		if _, err := f.s.ResolveItemImage(f.ctx, reader, item, "Primary", 0); err != domain.ErrNotFound {
			t.Fatal("revoked session resolved image", err)
		}
	}
	for _, id := range []string{"invalid", "10000000-0000-4000-8000-000000000099"} {
		if _, err := f.s.ListItemImages(f.ctx, f.a, id); err != domain.ErrNotFound {
			t.Fatal("missing item listed", err)
		}
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.ListItemImages(cancelled, f.a, item); err != context.Canceled {
		t.Fatal("list ignored cancellation", err)
	}
	if _, err := f.s.ResolveItemImage(nil, f.a, item, "Primary", 0); err != domain.ErrInvalid {
		t.Fatal("resolve accepted nil context", err)
	}
}

func TestItemImageDeleteAuditAndCascade(t *testing.T) {
	f, item := itemImageFixture(t)
	local := itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	remote := itemImageRemote(item, domain.ImageSourceRemote, "https://example.com/a.jpg")
	remote.Content = itemImageContent("a")
	itemImageUpsert(t, f, remote)
	if err := f.s.DeleteItemImage(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceNFO); err != domain.ErrNotFound {
		t.Fatal("deleted a missing source", err)
	}
	if err := f.s.DeleteItemImage(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceLocal); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT before_state::text,after_state::text FROM audit_logs WHERE event='image.deleted' AND target_id=$1::uuid AND actor_id=$2::uuid`, item, f.a.UserID).Scan(&before, &after); err != nil {
		t.Fatal("delete audit missing", err)
	}
	if !strings.Contains(before, local.Image.ID) || !strings.Contains(before, `"reference": "root"`) || strings.Contains(before, "poster") || after != "{}" {
		t.Fatal("delete audit state", before, after)
	}
	// Variants of deleted content stay indexed; eviction owns them.
	var digest [32]byte
	copy(digest[:], itemImageDigest("a"))
	if err := f.s.PutImageVariant(f.ctx, digest, [32]byte{1}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM items WHERE id=$1::uuid`, item); err != nil {
		t.Fatal(err)
	}
	var rows, variants int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM item_images WHERE item_id=$1::uuid),(SELECT count(*) FROM image_variants)`, item).Scan(&rows, &variants); err != nil || rows != 0 || variants != 1 {
		t.Fatal("item delete did not cascade references only", rows, variants, err)
	}
	// Removing a library root drops the references into it.
	second := metadataItem(t, f)
	itemImageUpsert(t, f, itemImageLocal(f, second, "poster.jpg"))
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_roots WHERE id=$1::uuid`, f.registration.RootID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid`, second).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("root delete kept references", rows, err)
	}
}

func TestImageVariantIndex(t *testing.T) {
	f, _ := itemImageFixture(t)
	var a, b, key [32]byte
	copy(a[:], itemImageDigest("a"))
	copy(b[:], itemImageDigest("b"))
	key[0] = 7
	if err := f.s.TouchImageVariant(f.ctx, a, key); err != domain.ErrNotFound {
		t.Fatal("touched missing variant", err)
	}
	for _, digest := range [][32]byte{a, b} {
		if err := f.s.PutImageVariant(f.ctx, digest, key, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.PutImageVariant(f.ctx, a, key, 0); err != domain.ErrInvalid {
		t.Fatal("empty variant accepted", err)
	}
	imageRepositoryExec(t, f, `UPDATE image_variants SET last_access=created_at-interval '1 hour' WHERE content_sha256=$1`, a[:])
	imageRepositoryExec(t, f, `UPDATE image_variants SET last_access=created_at-interval '2 hour' WHERE content_sha256=$1`, b[:])
	oldest, err := f.s.ListOldestImageVariants(f.ctx, 10)
	if err != nil || len(oldest) != 2 || oldest[0].ContentSHA256 != b || oldest[1].ContentSHA256 != a || oldest[0].VariantKey != key || oldest[0].Bytes != 100 {
		t.Fatal("oldest variants order", err)
	}
	if _, err := f.s.ListOldestImageVariants(f.ctx, 0); err != domain.ErrInvalid {
		t.Fatal("unbounded listing accepted", err)
	}
	if err := f.s.TouchImageVariant(f.ctx, b, key); err != nil {
		t.Fatal(err)
	}
	// The touched entry survives a stale eviction; the untouched one goes.
	if removed, err := f.s.DeleteImageVariant(f.ctx, oldest[0]); err != nil || removed {
		t.Fatal("evicted a variant touched after listing", err)
	}
	if removed, err := f.s.DeleteImageVariant(f.ctx, oldest[1]); err != nil || !removed {
		t.Fatal("evict untouched variant", err)
	}
	var touched time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT last_access FROM image_variants WHERE content_sha256=$1`, b[:]).Scan(&touched); err != nil {
		t.Fatal(err)
	}
	// A second touch within the throttle interval does not write.
	if err := f.s.TouchImageVariant(f.ctx, b, key); err != nil {
		t.Fatal(err)
	}
	var again time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT last_access FROM image_variants WHERE content_sha256=$1`, b[:]).Scan(&again); err != nil || !again.Equal(touched) {
		t.Fatal("touch not throttled", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO image_variants(content_sha256,variant_key,byte_size) VALUES($1,$2,1)`, []byte{1}, key[:]); itemImageSQLState(err) != "23514" {
		t.Fatal("short variant digest accepted", err)
	}
}

func TestItemImageMigrationDownRefusesRetainedImages(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	var digest [32]byte
	if err := f.s.PutImageVariant(f.ctx, digest, digest, 1); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "item_images")
	nfoMigrationDenied(t, f, itemImagesMigrationFile(t, "down"))
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained item images downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused downgrade lost dirty status", version, dirty, err)
	}
	var rows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM item_images)+(SELECT count(*) FROM image_variants)`).Scan(&rows); err != nil || rows != 2 {
		t.Fatal("refused downgrade removed data", rows, err)
	}
}

func TestItemImageMigrationRoundTrip(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
	var digest [32]byte
	if err := f.s.PutImageVariant(f.ctx, digest, digest, 1); err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f, `DELETE FROM item_images`)
	// The variant index is a rebuildable cache and does not block rollback.
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "item_images")-1)
	var tables int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=current_schema() AND c.relname IN ('item_images','image_variants')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("downgrade left image tables", tables, err)
	}
	var audits int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs`).Scan(&audits); err != nil || audits == 0 {
		t.Fatal("downgrade removed audit history", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("store not ready after round trip", err)
	}
	itemImageUpsert(t, f, itemImageLocal(f, item, "poster.jpg"))
}

func TestItemImageResolveSourcesOrderAndAccess(t *testing.T) {
	f, item := itemImageFixture(t)
	if _, err := f.s.ResolveItemImageSources(f.ctx, f.a, item, "Primary", 0); err != domain.ErrNotFound {
		t.Fatal("slot without rows", err)
	}
	embedded := itemImageLocal(f, item, "Film.mkv")
	embedded.SourceKind, embedded.SourceModifiedUnixNano, embedded.SourceSize = domain.ImageSourceEmbedded, nil, nil
	embedded.Content = itemImageContent("embedded")
	remote := itemImageRemote(item, domain.ImageSourceRemote, "https://image.example/poster.jpg")
	nfoURL := itemImageRemote(item, domain.ImageSourceNFO, "https://image.example/nfo.jpg")
	for _, in := range []domain.ItemImageInput{embedded, remote, nfoURL, itemImageLocal(f, item, "poster.jpg")} {
		itemImageUpsert(t, f, in)
	}
	kinds := func(want ...string) {
		t.Helper()
		rows, err := f.s.ResolveItemImageSources(f.ctx, f.a, item, "Primary", 0)
		if err != nil || len(rows) != len(want) {
			t.Fatalf("resolve sources: %d rows, %v", len(rows), err)
		}
		for i, row := range rows {
			if row.SourceKind != want[i] || row.ItemID != item || row.Type != "Primary" {
				t.Fatalf("source %d=%s want %s", i, row.SourceKind, want[i])
			}
		}
	}
	// URL references without fetched content never take part.
	kinds(domain.ImageSourceLocal, domain.ImageSourceEmbedded)
	remote.Content = itemImageContent("remote")
	itemImageUpsert(t, f, remote)
	nfoURL.Content = itemImageContent("nfo")
	itemImageUpsert(t, f, nfoURL)
	kinds(domain.ImageSourceLocal, domain.ImageSourceNFO, domain.ImageSourceRemote, domain.ImageSourceEmbedded)
	if _, err := f.s.SetItemImageLock(f.ctx, f.a, item, "Primary", 0, domain.ImageSourceRemote, true); err != nil {
		t.Fatal(err)
	}
	kinds(domain.ImageSourceRemote, domain.ImageSourceLocal, domain.ImageSourceNFO, domain.ImageSourceEmbedded)
	rows, _ := f.s.ResolveItemImageSources(f.ctx, f.a, item, "Primary", 0)
	if rows[1].RootPath == "" || rows[1].RelativePath != "movie/poster.jpg" || !rows[0].Locked {
		t.Fatal("resolved row lacks its binding")
	}
	backdrop := itemImageLocal(f, item, "fanart7.jpg")
	backdrop.Type, backdrop.Index = "Backdrop", 7
	itemImageUpsert(t, f, backdrop)
	if rows, err := f.s.ResolveItemImageSources(f.ctx, f.a, item, "Backdrop", 7); err != nil || len(rows) != 1 || rows[0].Index != 7 {
		t.Fatal("gallery slot", err)
	}
	for _, slot := range []struct {
		imageType string
		index     int
	}{{"Backdrop", 6}, {"Logo", 0}, {"Primary", 1}, {"Poster", 0}} {
		if _, err := f.s.ResolveItemImageSources(f.ctx, f.a, item, slot.imageType, slot.index); err != domain.ErrNotFound {
			t.Fatal("unexpected slot resolved", slot, err)
		}
	}
	// A reader sees nothing without a grant, everything with it, and nothing
	// again after revocation or session end.
	reader := imageRepositoryActor(t, f, "item-image-sources", access.ClientWeb)
	if rows, err := f.s.ResolveItemImageSources(f.ctx, reader, item, "Primary", 0); rows != nil || err != domain.ErrNotFound {
		t.Fatal("ungranted reader resolved sources")
	}
	imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
	if rows, err := f.s.ResolveItemImageSources(f.ctx, reader, item, "Primary", 0); err != nil || len(rows) != 4 {
		t.Fatal("granted reader", err)
	}
	imageRepositoryExec(t, f, `DELETE FROM library_acl WHERE user_id=$1::uuid`, reader.UserID)
	if _, err := f.s.ResolveItemImageSources(f.ctx, reader, item, "Primary", 0); err != domain.ErrNotFound {
		t.Fatal("revoked reader resolved sources", err)
	}
	imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, reader.UserID, f.registration.Library.ID)
	imageRepositoryExec(t, f, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, reader.SessionID)
	if _, err := f.s.ResolveItemImageSources(f.ctx, reader, item, "Primary", 0); err != domain.ErrNotFound {
		t.Fatal("revoked session resolved sources", err)
	}
}

func TestItemImageListLibraryRootPaths(t *testing.T) {
	f, _ := itemImageFixture(t)
	var want string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&want); err != nil {
		t.Fatal(err)
	}
	roots, err := f.s.ListLibraryRootPaths(f.ctx, 1024)
	if err != nil || len(roots) != 1 || roots[0] != want {
		t.Fatal("library roots", roots, err)
	}
	if _, err := f.s.RegisterLibrary(f.ctx, "second", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ListLibraryRootPaths(f.ctx, 1); err != domain.ErrInvalid {
		t.Fatal("truncated root list returned", err)
	}
	if _, err := f.s.ListLibraryRootPaths(f.ctx, 0); err != domain.ErrInvalid {
		t.Fatal("zero limit accepted", err)
	}
}

func itemImagesMigrationFile(t *testing.T, direction string) string {
	t.Helper()
	return fmt.Sprintf("%06d_item_images.%s.sql", migrationVersion(t, "item_images"), direction)
}
