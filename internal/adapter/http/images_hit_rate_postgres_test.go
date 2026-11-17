package httpapi

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
)

// TestImageResizeRequestsHitCachePostgres (G40.13) sends the same resize
// request repeatedly through /images and through the compatibility image
// route, served by the production processor and persistent store: only the
// first request of each size generates a variant, every repeat is a memory
// cache hit, and the counters behind jelee_images_cache_hits_total,
// jelee_images_cache_misses_total and jelee_images_variants_generated_total
// say so.
func TestImageResizeRequestsHitCachePostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	token, err := store.Provision(ctx, "image-hits", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	private := adminImagePrivateBase(t)
	media, scratch := filepath.Join(private, "media"), filepath.Join(private, "scratch")
	for _, dir := range []string{media, scratch, filepath.Join(media, "film")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewGray(image.Rect(0, 0, 80, 120)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "film", "poster.jpg"), original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Hit Films') RETURNING id::text`)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, media)
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Hit Film','Movie') RETURNING id::text`, library)
	compatInsert(t, ctx, store, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,'film/poster.jpg') RETURNING id::text`, item, library, root)

	cfg := leakConfig(t, dsn, 0)
	cfg.Images.TempRoot = scratch
	processor := adminImageProcessor(t, store, scratch, media)
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithRenderer(t, store, cfg, &httpAccountPasswords{}, progress, processor)
	get := func(r *http.Request) string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Body.Len() == 0 {
			t.Fatalf("%s: status %d", r.URL.Path, w.Code)
		}
		return w.Header().Get("ETag")
	}
	native := func(query string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://localhost/images/Primary/"+item+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	compat := func(query string) *http.Request {
		return compatUserRequest(http.MethodGet, "/compat/Items/"+leakWire(item)+"/Images/Primary"+query, "", compatTokenAuth(token))
	}
	type counts struct{ hits, misses, generated uint64 }
	read := func() counts {
		s := processor.Stats()
		return counts{s.CacheHits, s.CacheMisses, s.Generated}
	}

	tag := get(native("?width=40"))
	if got := read(); got != (counts{misses: 1, generated: 1}) {
		t.Fatalf("cold /images request: %+v", got)
	}
	for range 4 {
		if get(native("?width=40")) != tag {
			t.Fatal("repeated /images request changed the representation")
		}
	}
	if got := read(); got != (counts{hits: 4, misses: 1, generated: 1}) {
		t.Fatalf("repeated /images requests did not hit: %+v", got)
	}
	compatTag := get(compat("?maxWidth=24"))
	for range 4 {
		if get(compat("?maxWidth=24")) != compatTag {
			t.Fatal("repeated compatibility request changed the representation")
		}
	}
	if got := read(); got != (counts{hits: 8, misses: 2, generated: 2}) {
		t.Fatalf("repeated compatibility requests did not hit: %+v", got)
	}
	// Hit rate of this run: 8 / (8 + 2).
	if s := processor.Stats(); s.Completed != 10 || s.Decodes != 2 {
		t.Fatalf("requests or decodes: %+v", s)
	}
}
