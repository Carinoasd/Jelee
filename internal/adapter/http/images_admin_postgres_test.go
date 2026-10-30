package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
)

// adminImagePrivateBase is a directory with native private permissions, as
// the image scratch and library roots need; on Linux the toolchain's TMPDIR
// may be a Windows mount.
func adminImagePrivateBase(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return t.TempDir()
	}
	base, err := os.MkdirTemp("/tmp", "jelee-image-admin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}

// adminImageProcessor is the production image pipeline with a persistent
// store whose variants are indexed in PostgreSQL.
func adminImageProcessor(t *testing.T, store *postgres.Store, tempRoot, mediaRoot string) *imageadapter.Processor {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	pictures, err := imageadapter.OpenStore(context.Background(), imageadapter.StoreOptions{Root: root, MediaRoots: []string{mediaRoot}, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal("open image store", err)
	}
	processor, err := imageadapter.New(context.Background(), imageadapter.Options{TempRoot: tempRoot, MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20,
		MaxOutputBytes: 2 << 20, CacheBytes: 32 << 20, MaxOutputDimension: 1024, CacheEntries: 128, DefaultQuality: 85, Timeout: 15 * time.Second,
		CacheTTL: 300 * time.Second, Store: pictures, Index: store})
	if err != nil {
		t.Fatal("create image processor", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = processor.Shutdown(ctx)
		_ = pictures.Close(ctx)
	})
	return processor
}

func adminImagePNG(t *testing.T, width, height int, shade uint8) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.NRGBA{R: shade, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// TestItemImageAdminHTTPPostgres drives the administrator image API through
// the real store and image pipeline: confirmation, audit, the locked slot,
// the untouched library original, cleared variants, and the same answer for
// a hidden and a missing item. Every status it sees must be documented.
func TestItemImageAdminHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "image-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := store.Provision(ctx, "image-viewer", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	admin := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='image-admin'`)
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='image-viewer'`)
	private := adminImagePrivateBase(t)
	media, scratch := filepath.Join(private, "media"), filepath.Join(private, "scratch")
	for _, dir := range []string{media, scratch} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	poster := filepath.Join(media, "film", "poster.jpg")
	if err := os.MkdirAll(filepath.Dir(poster), 0700); err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewGray(image.Rect(0, 0, 40, 60)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	posterSum := sha256.Sum256(original.Bytes())
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Image Films') RETURNING id::text`)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, media)
	item := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Image Film','Movie') RETURNING id::text`, library)
	hidden := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Hidden Film','Movie') RETURNING id::text`, library)
	for _, id := range []string{item, hidden} {
		compatInsert(t, ctx, store, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,'film/poster.jpg') RETURNING id::text`, id, library, root)
	}
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, library)

	cfg := leakConfig(t, dsn, 0)
	cfg.Images.TempRoot = scratch
	processor := adminImageProcessor(t, store, cfg.Images.TempRoot, media)
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithRenderer(t, store, cfg, &httpAccountPasswords{}, progress, processor)
	spec := Specification(cfg)["paths"].(map[string]any)
	type response struct {
		status int
		code   string
		data   map[string]any
		header http.Header
	}
	send := func(method, pattern, path, contentType string, body []byte, token string) response {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
		if body == nil {
			r = httptest.NewRequest(method, "http://localhost"+path, nil)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var envelope struct {
			Data  json.RawMessage `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &envelope)
		result := response{status: w.Code, code: envelope.Error.Code, header: w.Header()}
		_ = json.Unmarshal(envelope.Data, &result.data)
		// G49: every status an operation answers is in its specification.
		if pattern != "" {
			op, _ := spec[pattern].(map[string]any)[strings.ToLower(method)].(map[string]any)
			if op == nil {
				t.Fatalf("%s %s is not documented", method, pattern)
			}
			if _, ok := op["responses"].(map[string]any)[strconv.Itoa(w.Code)]; !ok {
				t.Fatalf("%s %s answered undocumented %d %s", method, pattern, w.Code, envelope.Error.Code)
			}
		}
		return result
	}
	const (
		list   = "/api/v1/items/{id}/images"
		slot   = "/api/v1/items/{id}/images/{type}"
		lock   = "/api/v1/items/{id}/images/{type}/lock"
		unlock = "/api/v1/items/{id}/images/{type}/unlock"
	)
	base := "/api/v1/items/" + item + "/images"
	upload := adminImagePNG(t, 64, 48, 20)
	uploadSum := sha256.Sum256(upload)
	audits := func(event string) int {
		t.Helper()
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event=$1 AND target_id=$2::uuid AND actor_id=$3::uuid`, event, item, admin).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// Viewers are refused before any lookup, for every route.
	for _, c := range []struct{ method, pattern, path, contentType string }{
		{"GET", list, base, ""},
		{"POST", slot, base + "/Primary?confirm=true", "image/png"},
		{"DELETE", slot, base + "/Primary?confirm=true", ""},
		{"POST", lock, base + "/Primary/lock?source=local", "application/json"},
		{"POST", unlock, base + "/Primary/unlock?source=local", "application/json"},
	} {
		body := upload
		if c.contentType == "application/json" {
			body = []byte("{}")
		} else if c.method != "POST" {
			body = nil
		}
		if r := send(c.method, c.pattern, c.path, c.contentType, body, viewerToken); r.status != 403 || r.code != "forbidden" {
			t.Fatalf("viewer %s %s: %d %s", c.method, c.path, r.status, r.code)
		}
	}

	// Replacing the library poster needs the confirmation; nothing is stored.
	if r := send("POST", slot, base+"/Primary", "image/png", upload, adminToken); r.status != 400 || r.code != "confirmation_required" {
		t.Fatalf("unconfirmed replacement: %d %s", r.status, r.code)
	}
	if audits("image.added")+audits("image.replaced") != 0 {
		t.Fatal("refused replacement audited")
	}
	r := send("POST", slot, base+"/Primary?confirm=true", "image/png", upload, adminToken)
	if r.status != 200 || r.data["sourceKind"] != "upload" || r.data["locked"] != true || r.data["reference"] != "store" ||
		r.data["contentSha256"] != hex.EncodeToString(uploadSum[:]) || r.data["format"] != "png" || r.data["width"] != float64(64) {
		t.Fatalf("confirmed replacement: %d %s %v", r.status, r.code, r.data)
	}
	if audits("image.added") != 1 {
		t.Fatal("upload not audited")
	}
	// The locked upload is what /images serves now, with its immutable tag.
	if r := send("GET", "", "/images/Primary/"+item+"?width=32&tag="+hex.EncodeToString(uploadSum[:]), "", nil, adminToken); r.status != 200 || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
		t.Fatalf("serve upload: %d %s", r.status, r.code)
	}
	var variants int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM image_variants WHERE content_sha256=$1`, uploadSum[:]).Scan(&variants); err != nil || variants != 1 {
		t.Fatal("rendered upload variant not indexed", variants, err)
	}
	// An empty slot takes an upload without confirmation (201); Fanart is Backdrop.
	if r := send("POST", slot, base+"/Fanart?index=2", "application/octet-stream", adminImagePNG(t, 30, 20, 99), adminToken); r.status != 201 || r.data["type"] != "Backdrop" || r.data["index"] != float64(2) {
		t.Fatalf("upload into an empty slot: %d %s %v", r.status, r.code, r.data)
	}
	// Untrusted bodies are refused before anything is stored.
	for _, c := range []struct {
		name, contentType string
		body              []byte
		status            int
		code              string
	}{
		{"declared text", "text/plain", upload, 415, "image_unsupported"},
		{"not an image", "image/png", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 415, "image_unsupported"},
		{"no type", "", upload, 415, "image_unsupported"},
		{"empty", "image/png", []byte{}, 400, "invalid_request"},
	} {
		if r := send("POST", slot, base+"/Logo", c.contentType, c.body, adminToken); r.status != c.status || r.code != c.code {
			t.Fatalf("%s: %d %s", c.name, r.status, r.code)
		}
	}
	big := httptest.NewRequest("POST", "http://localhost"+base+"/Logo", strings.NewReader("x"))
	big.ContentLength = cfg.Images.MaxSourceBytes + 1
	big.Header.Set("Content-Type", "image/png")
	big.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, big)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "image_too_large") {
		t.Fatalf("oversized upload: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{base + "/Primary?index=1", base + "/Poster", base + "/Primary?confirm=yes", base + "/Primary?index=01", base + "/Primary?width=10"} {
		if r := send("POST", slot, path, "image/png", upload, adminToken); r.status != 400 || r.code != "invalid_request" {
			t.Fatalf("%s: %d %s", path, r.status, r.code)
		}
	}

	// The listing shows both sources of the slot, lock first, without paths.
	r = send("GET", list, base, "", nil, adminToken)
	raw, _ := json.Marshal(r.data)
	if r.status != 200 || strings.Contains(string(raw), "poster") || strings.Contains(string(raw), media) {
		t.Fatalf("list: %d %s", r.status, raw)
	}
	listing := httptest.NewRecorder()
	listRequest := httptest.NewRequest("GET", "http://localhost"+base, nil)
	listRequest.Header.Set("Authorization", "Bearer "+adminToken)
	handler.ServeHTTP(listing, listRequest)
	var rows struct {
		Data []struct {
			Type, SourceKind, Reference string
			Locked                      bool
		} `json:"data"`
	}
	if json.Unmarshal(listing.Body.Bytes(), &rows) != nil || len(rows.Data) != 3 || rows.Data[1].SourceKind != "upload" || !rows.Data[1].Locked ||
		rows.Data[2].SourceKind != "local" || rows.Data[2].Reference != "file" || strings.Contains(listing.Body.String(), "poster.jpg") {
		t.Fatalf("listing rows: %s", listing.Body.String())
	}

	// Lock hand-over and release.
	if r := send("POST", lock, base+"/Primary/lock", "application/json", []byte("{}"), adminToken); r.status != 400 {
		t.Fatalf("lock without a source: %d %s", r.status, r.code)
	}
	if r := send("POST", lock, base+"/Primary/lock?source=nfo", "application/json", []byte("{}"), adminToken); r.status != 404 || r.code != "not_found" {
		t.Fatalf("lock a missing source: %d %s", r.status, r.code)
	}
	if r := send("POST", lock, base+"/Primary/lock?source=local", "application/json", []byte("{}"), adminToken); r.status != 200 || r.data["locked"] != true || r.data["sourceKind"] != "local" {
		t.Fatalf("lock local: %d %s", r.status, r.code)
	}
	if audits("image.locked") != 1 || audits("image.unlocked") != 1 {
		t.Fatal("lock hand-over not audited")
	}
	if r := send("POST", unlock, base+"/Primary/unlock?source=local", "application/json", []byte("{}"), adminToken); r.status != 200 || r.data["locked"] != false {
		t.Fatalf("unlock local: %d %s", r.status, r.code)
	}

	// Deleting needs the confirmation and removes only the reference.
	if r := send("DELETE", slot, base+"/Primary?source=upload", "", nil, adminToken); r.status != 400 || r.code != "confirmation_required" {
		t.Fatalf("unconfirmed delete: %d %s", r.status, r.code)
	}
	if r := send("DELETE", slot, base+"/Primary?source=upload&confirm=true", "", nil, adminToken); r.status != 204 {
		t.Fatalf("delete upload: %d %s", r.status, r.code)
	}
	if audits("image.deleted") != 1 {
		t.Fatal("delete not audited")
	}
	// Jelee's variants of the dropped upload are cleared; nothing else is.
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM image_variants WHERE content_sha256=$1`, uploadSum[:]).Scan(&variants); err != nil || variants != 0 {
		t.Fatal("variants of the deleted upload remain", variants, err)
	}
	// The library poster serves again and is byte-for-byte unchanged.
	if r := send("GET", "", "/images/Primary/"+item, "", nil, adminToken); r.status != 200 {
		t.Fatalf("library poster after delete: %d %s", r.status, r.code)
	}
	if r := send("DELETE", slot, base+"/Primary?confirm=true", "", nil, adminToken); r.status != 204 {
		t.Fatalf("delete slot: %d %s", r.status, r.code)
	}
	if r := send("DELETE", slot, base+"/Primary?confirm=true", "", nil, adminToken); r.status != 404 || r.code != "not_found" {
		t.Fatalf("delete an empty slot: %d %s", r.status, r.code)
	}
	if data, err := os.ReadFile(poster); err != nil || sha256.Sum256(data) != posterSum {
		t.Fatal("the library original changed or disappeared", err)
	}

	// An administrator restricted by content rules gets the same answer for
	// a hidden and a missing item, and the hidden item is not changed.
	compatInsert(t, ctx, store, `UPDATE access_policy SET restrict_admins=true RETURNING 'x'`)
	compatInsert(t, ctx, store, `UPDATE users SET content_filtered=true WHERE id=$1::uuid RETURNING id::text`, admin)
	compatInsert(t, ctx, store, `INSERT INTO user_item_access_rules(user_id,item_id,effect) VALUES($1::uuid,$2::uuid,'hide') RETURNING user_id::text`, admin, hidden)
	for _, c := range []struct{ method, pattern, suffix, contentType string }{
		{"GET", list, "", ""},
		{"POST", slot, "/Primary?confirm=true", "image/png"},
		{"DELETE", slot, "/Primary?confirm=true", ""},
		{"POST", lock, "/Primary/lock?source=local", "application/json"},
	} {
		var answers []string
		for _, id := range []string{hidden, "10000000-0000-4000-8000-000000000099"} {
			body := upload
			if c.contentType == "application/json" {
				body = []byte("{}")
			} else if c.method != "POST" {
				body = nil
			}
			r := send(c.method, c.pattern, "/api/v1/items/"+id+"/images"+c.suffix, c.contentType, body, adminToken)
			answers = append(answers, strconv.Itoa(r.status)+"/"+r.code)
		}
		if answers[0] != "404/not_found" || answers[1] != answers[0] {
			t.Fatalf("%s %s hidden and missing differ: %v", c.method, c.pattern, answers)
		}
	}
	var hiddenRows int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM item_images WHERE item_id=$1::uuid AND source_kind='local' AND NOT locked`, hidden).Scan(&hiddenRows); err != nil || hiddenRows != 1 {
		t.Fatal("hidden item changed", hiddenRows, err)
	}
}
