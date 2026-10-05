package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// compatImageRenderer stands in for the image processor: it serves an
// item_images row from its stored content, with an ETag derived from that
// content and the requested size like the processor's variant key, and
// records every request it renders.
type compatImageRenderer struct {
	mu       sync.Mutex
	requests []domain.ImageRequest
}

func (r *compatImageRenderer) Render(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
	return app.ImageResult{}, domain.ErrNotFound
}

func (r *compatImageRenderer) RenderItemImage(_ context.Context, image domain.ItemImage, request domain.ImageRequest) (app.ImageResult, error) {
	if image.Content == nil {
		return app.ImageResult{}, domain.ErrNotFound
	}
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	var content [32]byte
	copy(content[:], image.Content.SHA256)
	key := sha256.Sum256([]byte(hex.EncodeToString(content[:]) + "|" + request.Type + "|" + strconv.Itoa(request.Index) + "|" + strconv.Itoa(request.Width) + "x" + strconv.Itoa(request.Height) + "q" + strconv.Itoa(request.Quality)))
	data := append([]byte{0xff, 0xd8}, content[:4]...)
	data = append(data, 0xff, 0xd9)
	return app.ImageResult{Body: &httpImageBody{Reader: bytes.NewReader(data)}, ContentType: "image/jpeg", ETag: `"` + hex.EncodeToString(key[:]) + `"`,
		Size: int64(len(data)), Width: 2, Height: 3, ContentSHA256: content}, nil
}

func (r *compatImageRenderer) last() domain.ImageRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

// imageQueryCounter counts the SQL statements that read item_images.
type imageQueryCounter struct {
	mu sync.Mutex
	n  int
}

func (c *imageQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "item_images") {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return ctx
}

func (c *imageQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *imageQueryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

type compatImageItem struct {
	Name                    string            `json:"Name"`
	ID                      string            `json:"Id"`
	ImageTags               map[string]string `json:"ImageTags"`
	BackdropImageTags       []string          `json:"BackdropImageTags"`
	PrimaryImageAspectRatio *float64          `json:"PrimaryImageAspectRatio"`
}

func compatImageDigest(seed string) []byte {
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

// TestCompatImagesPostgres drives the image module through the real catalog
// and item_images tables: the image route serves through the /images
// pipeline with the caller's grant, missing and invisible items look the
// same, a content change changes the tag and the ETag, and a listing reads
// every item's image slots in one statement.
func TestCompatImagesPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := newCompatLibraryFixture(t, ctx, store)
	webToken, err := store.Provision(ctx, "browse-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	var rootA string
	if err := store.Pool.QueryRow(ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.libA).Scan(&rootA); err != nil {
		t.Fatal(err)
	}
	insertImage := func(item, library, root, imageType string, index int, kind, path string, digest []byte, width, height int) {
		t.Helper()
		var w, h any
		if width > 0 {
			w, h = width, height
		}
		var r any
		if root != "" {
			r = root
		}
		var remote any
		if root == "" {
			remote = "https://images.invalid/" + hex.EncodeToString(digest[:4]) + ".jpg"
		}
		var p any
		if path != "" {
			p = path
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,remote_url,content_sha256,width,height,format,byte_size,fetched_at)
 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7,$8,$9,$10,$11,'jpeg',100,now())`, item, library, imageType, index, kind, r, p, remote, digest, w, h); err != nil {
			t.Fatal(err)
		}
	}
	var movies []string
	rows, err := store.Pool.Query(ctx, `SELECT id::text FROM items WHERE library_id=$1::uuid ORDER BY id`, f.libA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		movies = append(movies, id)
	}
	rows.Close()
	if len(movies) != 5 {
		t.Fatalf("fixture movies %v", movies)
	}
	for _, id := range movies {
		insertImage(id, f.libA, "", "Primary", 0, "remote", "", compatImageDigest("primary-"+id), 600, 900)
	}
	// The movie also has a local primary (it wins by G40.10), two backdrops,
	// a clear logo and a backdrop after a gap.
	insertImage(f.movie, f.libA, rootA, "Primary", 0, "local", "echo-poster.jpg", compatImageDigest("local-v1"), 1000, 1500)
	insertImage(f.movie, f.libA, rootA, "Backdrop", 0, "local", "fanart.jpg", compatImageDigest("backdrop-0"), 1920, 1080)
	insertImage(f.movie, f.libA, rootA, "Backdrop", 1, "local", "fanart1.jpg", compatImageDigest("backdrop-1"), 1920, 1080)
	insertImage(f.movie, f.libA, rootA, "Backdrop", 3, "local", "fanart3.jpg", compatImageDigest("backdrop-3"), 1920, 1080)
	insertImage(f.movie, f.libA, rootA, "ClearLogo", 0, "local", "clearlogo.png", compatImageDigest("logo"), 800, 310)
	// User B's series has artwork that user A must never reach.
	insertImage(f.series, f.libB, "", "Primary", 0, "remote", "", compatImageDigest("secret-series"), 680, 1000)

	// A pool with a statement tracer, on the same schema.
	counter := &imageQueryCounter{}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse dedicated test database configuration")
	}
	poolConfig.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("open traced test pool")
	}
	t.Cleanup(pool.Close)
	traced := &postgres.Store{Pool: pool}
	progress, err := app.NewProgress(traced, traced, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	renderer := &compatImageRenderer{}
	handler := leakHandlerWithRenderer(t, traced, leakConfig(t, dsn, 0), &httpAccountPasswords{}, progress, renderer)
	handler403 := leakHandlerWithRenderer(t, traced, leakConfig(t, dsn, http.StatusForbidden), &httpAccountPasswords{}, progress, renderer)
	do := func(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, compatUserRequest(method, target, "", header))
		return w
	}
	get := func(target, token string) *httptest.ResponseRecorder {
		t.Helper()
		return do(handler, http.MethodGet, target, compatTokenAuth(token))
	}
	listing := func(target, token string) []compatImageItem {
		t.Helper()
		w := get(target, token)
		var page struct{ Items []compatImageItem }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
		return page.Items
	}
	movieWire := leakWire(f.movie)
	tagV1 := hex.EncodeToString(compatImageDigest("local-v1"))

	// Listing: every movie carries its primary tag, read in one statement.
	before := counter.count()
	items := listing("/compat/Items?Recursive=true&Fields=PrimaryImageAspectRatio", f.tokenA)
	if reads := counter.count() - before; reads != 1 {
		t.Fatalf("a listing of %d items read item_images %d times", len(items), reads)
	}
	if len(items) != 5 {
		t.Fatalf("listing %+v", items)
	}
	for _, item := range items {
		want := hex.EncodeToString(compatImageDigest("primary-" + strings.Join([]string{item.ID[:8], item.ID[8:12], item.ID[12:16], item.ID[16:20], item.ID[20:]}, "-")))
		if item.ID == movieWire {
			want = tagV1
			if len(item.BackdropImageTags) != 2 || item.BackdropImageTags[0] != hex.EncodeToString(compatImageDigest("backdrop-0")) ||
				item.ImageTags["Logo"] != hex.EncodeToString(compatImageDigest("logo")) || item.PrimaryImageAspectRatio == nil || math.Abs(*item.PrimaryImageAspectRatio-1000.0/1500) > 1e-9 {
				t.Fatalf("movie images %+v", item)
			}
		}
		if item.ImageTags["Primary"] != want {
			t.Fatalf("%s primary tag %q, want %q", item.Name, item.ImageTags["Primary"], want)
		}
	}
	before = counter.count()
	if detail := listing("/compat/Items?Ids="+movieWire, f.tokenA); len(detail) != 1 || detail[0].ImageTags["Primary"] != tagV1 || counter.count()-before != 1 {
		t.Fatalf("ids listing %+v", detail)
	}
	// User B's listing carries B's artwork; A never sees B's tag.
	secretTag := hex.EncodeToString(compatImageDigest("secret-series"))
	if b := get("/compat/Items?Recursive=true&IncludeItemTypes=Series", f.tokenB).Body.String(); !strings.Contains(b, secretTag) {
		t.Fatalf("B's own series has no tag: %s", b)
	}
	for _, target := range []string{"/compat/Items?Recursive=true", "/compat/Items?Ids=" + leakWire(f.series), "/compat/Items/" + movieWire} {
		if strings.Contains(get(target, f.tokenA).Body.String(), secretTag) {
			t.Fatalf("%s leaks user B's image tag", target)
		}
	}

	// The image route: sizes reach the pipeline, the response carries the
	// pipeline's validators and cache policy.
	image := get("/compat/Items/"+movieWire+"/Images/Primary?maxWidth=300&width=200&quality=80&tag="+tagV1, f.tokenA)
	etagV1 := image.Header().Get("ETag")
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/jpeg" || image.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" ||
		strings.Count(image.Header().Get("Vary"), ",") != 3 || strings.Contains(image.Header().Get("Vary"), "Cookie") || etagV1 == "" {
		t.Fatalf("image: %d %v", image.Code, image.Header())
	}
	if got := renderer.last(); got.Type != "Primary" || got.Width != 200 || got.Height != 0 || got.Quality != 80 || got.Format != "jpeg" {
		t.Fatalf("pipeline request %+v", got)
	}
	if w := do(handler, http.MethodHead, "/compat/Items/"+movieWire+"/Images/Primary?maxWidth=300&width=200&quality=80&api_key="+f.tokenA, nil); w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("ETag") != etagV1 {
		t.Fatalf("head with api_key: %d %v", w.Code, w.Header())
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Backdrop/1", f.tokenA); w.Code != http.StatusOK || renderer.last().Index != 1 {
		t.Fatalf("backdrop 1: %d", w.Code)
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Logo", f.tokenA); w.Code != http.StatusOK || renderer.last().Type != "ClearLogo" {
		t.Fatalf("logo: %d", w.Code)
	}
	header := compatTokenAuth(f.tokenA)
	header.Set("If-None-Match", etagV1)
	if w := do(handler, http.MethodGet, "/compat/Items/"+movieWire+"/Images/Primary?maxWidth=300&width=200&quality=80", header); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("revalidation: %d", w.Code)
	}

	// Missing and invisible look the same, in both hidden modes; so does a
	// visible item without that image.
	for _, mode := range []struct {
		h      http.Handler
		status int
	}{{handler, http.StatusNotFound}, {handler403, http.StatusForbidden}} {
		var answers []*httptest.ResponseRecorder
		for _, target := range []string{
			"/compat/Items/" + leakWire(f.series) + "/Images/Primary?maxWidth=100",
			"/compat/Items/" + leakWire(leakUUID(t)) + "/Images/Primary?maxWidth=100",
			"/compat/Items/" + leakWire(f.season) + "/Images/Primary/0",
		} {
			w := do(mode.h, http.MethodGet, target, compatTokenAuth(f.tokenA))
			if w.Code != mode.status || w.Body.Len() != 0 || strings.Contains(w.Header().Get("Content-Type"), "image") {
				t.Fatalf("%s: %d %q", target, w.Code, w.Body)
			}
			w.Header().Del("X-Request-ID")
			answers = append(answers, w)
		}
		for _, w := range answers[1:] {
			if !equalHeaders(w.Header(), answers[0].Header()) {
				t.Fatalf("hidden answers differ: %v vs %v", w.Header(), answers[0].Header())
			}
		}
		if w := do(mode.h, http.MethodGet, "/compat/Items/"+movieWire+"/Images/Banner", compatTokenAuth(f.tokenA)); w.Code != mode.status {
			t.Fatalf("no banner: %d", w.Code)
		}
	}
	// The owner and an administrator reach the series image.
	if w := get("/compat/Items/"+leakWire(f.series)+"/Images/Primary", f.tokenB); w.Code != http.StatusOK {
		t.Fatalf("owner: %d", w.Code)
	}
	if w := get("/compat/Items/"+leakWire(f.series)+"/Images/Primary", f.adminToken); w.Code != http.StatusOK {
		t.Fatalf("admin: %d", w.Code)
	}
	// Only native sessions: no credentials, a web session and a revoked
	// grant are all refused.
	if w := do(handler, http.MethodGet, "/compat/Items/"+movieWire+"/Images/Primary", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Primary", webToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("web session: %d", w.Code)
	}

	// Image sizes are not transformations here, but stay one on streams.
	if w := get("/compat/Videos/"+movieWire+"/stream?static=true&maxWidth=320", f.tokenA); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "transcode_disabled") {
		t.Fatalf("stream with width: %d %s", w.Code, w.Body)
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Primary?maxWidth=320&videoCodec=h264", f.tokenA); w.Code != http.StatusConflict {
		t.Fatalf("image with codec: %d", w.Code)
	}

	// A content refresh changes the tag and the ETag; the old ETag no
	// longer revalidates and the old tag loses the immutable policy.
	if _, err := store.Pool.Exec(ctx, `UPDATE item_images SET content_sha256=$2,updated_at=clock_timestamp() WHERE item_id=$1::uuid AND image_type='Primary' AND source_kind='local'`, f.movie, compatImageDigest("local-v2")); err != nil {
		t.Fatal(err)
	}
	tagV2 := hex.EncodeToString(compatImageDigest("local-v2"))
	if items := listing("/compat/Items?Ids="+movieWire, f.tokenA); items[0].ImageTags["Primary"] != tagV2 {
		t.Fatalf("tag after refresh %+v", items[0])
	}
	w := do(handler, http.MethodGet, "/compat/Items/"+movieWire+"/Images/Primary?maxWidth=300&width=200&quality=80&tag="+tagV1, header)
	if w.Code != http.StatusOK || w.Header().Get("ETag") == etagV1 || w.Header().Get("Cache-Control") != "private, no-cache, must-revalidate" {
		t.Fatalf("after refresh: %d %v", w.Code, w.Header())
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Primary?tag="+tagV2, f.tokenA); w.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("new tag policy %v", w.Header())
	}

	// Revoking A's grant hides the image and the tags at once.
	if _, err := store.Pool.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid`, f.userA); err != nil {
		t.Fatal(err)
	}
	if w := get("/compat/Items/"+movieWire+"/Images/Primary", f.tokenA); w.Code != http.StatusNotFound {
		t.Fatalf("after revoke: %d", w.Code)
	}
	if b := get("/compat/Items?Recursive=true", f.tokenA).Body.String(); strings.Contains(b, tagV2) {
		t.Fatalf("revoked listing still has tags: %s", b)
	}
}

func equalHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if strings.Join(v, "\x00") != strings.Join(b[k], "\x00") {
			return false
		}
	}
	return true
}
