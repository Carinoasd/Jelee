package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// G48.3 / G48.10: hidden content must not leak through any registered route.
// Every route that chi reports must be classified below. A new route that is
// neither covered nor explicitly exempted with a reason fails
// TestAccessLeakRouteTableIsComplete, even without a database.

type leakMode int

const (
	// leakByID addresses media by ID for non-administrators. Hidden IDs must
	// be answered exactly like missing IDs with the configured hidden status.
	leakByID leakMode = iota + 1
	// leakList lists content for non-administrators; hidden media must be absent.
	leakList
	// leakAdmin is administrator-only. A viewer is refused before any lookup,
	// so hidden and missing IDs must produce the same 403.
	leakAdmin
	// leakNoMedia carries no media identifiers. It is still requested by the
	// viewer and its whole response is scanned for hidden markers.
	leakNoMedia
	// leakExempt is not requested; reason must explain why.
	leakExempt
)

type leakRoute struct {
	mode leakMode
	// params maps every path parameter to a fixture kind (see leakIDs.value).
	params map[string]string
	// control asks for an administrator request proving the fixture exposes
	// the hidden marker when authorization allows it.
	control bool
	reason  string
}

var (
	itemParam   = map[string]string{"id": "item"}
	sourceParam = map[string]string{"id": "source"}
	libParam    = map[string]string{"id": "library"}
	jobParam    = map[string]string{"id": "job"}
	selfParam   = map[string]string{"id": "self"}
	tmdbParam   = map[string]string{"id": "tmdb"}
	noParams    = map[string]string{}
)

func leakRouteTable() map[string]leakRoute {
	admin := func(p map[string]string) leakRoute { return leakRoute{mode: leakAdmin, params: p} }
	noMedia := func(p map[string]string, reason string) leakRoute {
		return leakRoute{mode: leakNoMedia, params: p, reason: reason}
	}
	exempt := func(reason string) leakRoute { return leakRoute{mode: leakExempt, reason: reason} }
	return map[string]leakRoute{
		// Public service routes.
		"GET /healthz":             noMedia(noParams, "public liveness status"),
		"GET /readyz":              noMedia(noParams, "public readiness status"),
		"GET /api/v1/system":       noMedia(noParams, "public capability flags"),
		"GET /api-docs":            noMedia(noParams, "static documentation page"),
		"GET /api/v1/openapi.json": noMedia(noParams, "generated specification"),

		// Catalog and delivery: the direct media surfaces.
		"GET /api/v1/items":                {mode: leakList, params: noParams, control: true},
		"GET /api/v1/items/{id}":           {mode: leakByID, params: itemParam, control: true},
		"GET /api/v1/sources/{id}/stream":  {mode: leakByID, params: sourceParam, control: true},
		"HEAD /api/v1/sources/{id}/stream": {mode: leakByID, params: sourceParam, control: true},
		"GET /images/{type}/{id}":          {mode: leakByID, params: map[string]string{"type": "image-type", "id": "item"}, control: true},
		"HEAD /images/{type}/{id}":         {mode: leakByID, params: map[string]string{"type": "image-type", "id": "item"}, control: true},

		// Accounts.
		"POST /api/v1/auth/login":                         exempt("credential exchange; takes no media identifiers and returns only a session grant"),
		"POST /api/v1/auth/logout":                        exempt("revokes the caller's own session; carries no media identifiers"),
		"POST /api/v1/auth/rotate":                        exempt("rotates the caller's own token; carries no media identifiers"),
		"GET /api/v1/auth/csrf":                           noMedia(noParams, "returns only a token derived from the caller's own credential"),
		"PUT /api/v1/users/me/profile":                    exempt("mutates the caller's own profile; carries no media identifiers"),
		"PUT /api/v1/users/me/password":                   exempt("mutates the caller's own password; carries no media identifiers"),
		"DELETE /api/v1/users/{id}/sessions":              exempt("revokes the caller's own sessions; carries no media identifiers"),
		"DELETE /api/v1/users/{id}/sessions/{sessionID}":  exempt("revokes one of the caller's sessions; carries no media identifiers"),
		"GET /api/v1/users/me":                            noMedia(noParams, "caller's own account"),
		"GET /api/v1/users/{id}":                          noMedia(selfParam, "caller's own account"),
		"GET /api/v1/users/{id}/sessions":                 noMedia(selfParam, "caller's own sessions"),
		"GET /api/v1/users/{id}/libraries":                {mode: leakList, params: selfParam},
		"GET /api/v1/users":                               admin(noParams),
		"POST /api/v1/users":                              admin(noParams),
		"PUT /api/v1/users/{id}":                          admin(selfParam),
		"DELETE /api/v1/users/{id}":                       admin(selfParam),
		"POST /api/v1/users/{id}/restore":                 admin(selfParam),
		"POST /api/v1/users/{id}/unlock":                  admin(selfParam),
		"PUT /api/v1/users/{id}/libraries":                admin(selfParam),
		"GET /metrics":                                    admin(noParams),
		"GET /api/v1/items/{id}/metadata":                 admin(itemParam),
		"PUT /api/v1/items/{id}/metadata":                 admin(itemParam),
		"POST /api/v1/items/{id}/metadata/nfo":            admin(itemParam),
		"POST /api/v1/items/{id}/metadata/tmdb":           admin(itemParam),
		"DELETE /api/v1/items/{id}/metadata/external":     admin(itemParam),
		"GET /api/v1/libraries/{id}/metadata-preferences": admin(libParam),
		"PUT /api/v1/libraries/{id}/metadata-preferences": admin(libParam),

		// TMDB lookups are administrator-only and keyed by provider IDs.
		"GET /api/v1/metadata/tmdb/movies":                                          admin(noParams),
		"GET /api/v1/metadata/tmdb/movies/{id}":                                     admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/movies/{id}/images":                              admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series":                                          admin(noParams),
		"GET /api/v1/metadata/tmdb/series/{id}":                                     admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series/{id}/images":                              admin(tmdbParam),
		"GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}":                    admin(map[string]string{"id": "tmdb", "season": "tmdb"}),
		"GET /api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}": admin(map[string]string{"id": "tmdb", "season": "tmdb", "episode": "tmdb"}),

		// Library administration and jobs.
		"GET /api/v1/libraries":                                                     admin(noParams),
		"GET /api/v1/libraries/{id}/watch":                                          admin(libParam),
		"GET /api/v1/libraries/{id}/schedule":                                       admin(libParam),
		"PUT /api/v1/libraries/{id}/schedule":                                       admin(libParam),
		"POST /api/v1/libraries/{id}/schedule/run":                                  admin(libParam),
		"POST /api/v1/libraries/{id}/scan":                                          admin(libParam),
		"GET /api/v1/libraries/{id}/catalog-sync":                                   admin(libParam),
		"PUT /api/v1/libraries/{id}/catalog-sync":                                   admin(libParam),
		"POST /api/v1/libraries/{id}/catalog-sync":                                  admin(libParam),
		"GET /api/v1/libraries/{id}/catalog-sync/pending":                           admin(libParam),
		"POST /api/v1/libraries/{id}/probe/rebuild":                                 admin(libParam),
		"POST /api/v1/items/{id}/probe/rebuild":                                     admin(itemParam),
		"GET /api/v1/libraries/{id}/nfo/policy":                                     admin(libParam),
		"PUT /api/v1/libraries/{id}/nfo/policy":                                     admin(libParam),
		"POST /api/v1/libraries/{id}/nfo/validate":                                  admin(libParam),
		"GET /api/v1/libraries/{id}/nfo/current-validations":                        admin(libParam),
		"GET /api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues": admin(map[string]string{"id": "library", "observationId": "opaque"}),
		"GET /api/v1/jobs":                                                          admin(noParams),
		"GET /api/v1/jobs/{id}":                                                     admin(jobParam),
		"GET /api/v1/jobs/{id}/entries":                                             admin(jobParam),
		"GET /api/v1/jobs/{id}/catalog-sync":                                        admin(jobParam),
		"POST /api/v1/jobs/{id}/accept-missing":                                     admin(jobParam),
		"PUT /api/v1/jobs/{id}/entries/{entry}/item":                                admin(map[string]string{"id": "job", "entry": "opaque"}),
		"GET /api/v1/jobs/{id}/imports":                                             admin(jobParam),
		"POST /api/v1/jobs/{id}/imports":                                            admin(jobParam),
		"GET /api/v1/jobs/{id}/ignore":                                              admin(jobParam),
		"GET /api/v1/jobs/{id}/probe":                                               admin(jobParam),
		"GET /api/v1/jobs/{id}/nfo":                                                 admin(jobParam),
		"GET /api/v1/jobs/{id}/images":                                              admin(jobParam),
		"POST /api/v1/jobs/{id}/cancel":                                             admin(jobParam),
		"POST /api/v1/jobs/{id}/retry":                                              admin(jobParam),
	}
}

var leakPathParam = regexp.MustCompile(`\{([^}]+)\}`)

func leakWalk(t *testing.T, handler http.Handler) map[string]bool {
	t.Helper()
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("router does not expose chi routes")
	}
	seen := map[string]bool{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seen
}

// leakConfig enables every rollout flag so the walk sees every route.
func leakConfig(t *testing.T, dsn string, hiddenStatus int) config.Config {
	t.Helper()
	cfg := validConfig()
	cfg.DatabaseURL = dsn
	cfg.MaxConnections, cfg.MaxStreams, cfg.RequestTimeoutSeconds = 16, 4, 10
	cfg.EnableCatalog, cfg.EnableDirect, cfg.EnableAccounts, cfg.EnableMetrics, cfg.EnableImages, cfg.EnableJobs = true, true, true, true, true, true
	cfg.Accounts, cfg.Jobs, cfg.Images = config.DefaultAccountsConfig(), config.DefaultJobsConfig(), config.DefaultImagesConfig()
	cfg.Images.TempRoot = t.TempDir()
	cfg.TMDBAPIKey = strings.Repeat("a", 32)
	cfg.Access.HiddenStatus = hiddenStatus
	return cfg
}

type leakRenderer struct{}

func (leakRenderer) Render(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
	data := []byte{0xff, 0xd8, 0xff, 0xd9}
	return app.ImageResult{Body: &httpImageBody{Reader: bytes.NewReader(data)}, ContentType: "image/jpeg", ETag: httpImageETag, Size: int64(len(data)), Width: 1, Height: 1}, nil
}

func (r leakRenderer) RenderItemImage(ctx context.Context, _ domain.ItemImage, request domain.ImageRequest) (app.ImageResult, error) {
	return r.Render(ctx, domain.LocalImageSource{}, request)
}

func leakHandler(t *testing.T, store *postgres.Store, cfg config.Config) http.Handler {
	t.Helper()
	accounts, err := app.NewAccounts(store, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := app.NewJobs(store, cfg.Jobs.Policy())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.NewMetadata(&httpMovieProvider{})
	if err == nil {
		metadata, err = metadata.WithLibraryPreferences(store)
	}
	if err == nil {
		metadata, err = metadata.WithItemMetadata(store)
	}
	if err != nil {
		t.Fatal(err)
	}
	images, err := app.NewImages(store, leakRenderer{})
	if err == nil {
		images, err = images.WithAssets(store, leakRenderer{})
	}
	if err != nil {
		t.Fatal(err)
	}
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "# metrics\n") })
	handler, err := NewWithImages(cfg, store, app.NewCatalog(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, metadata, metrics, images)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestAccessLeakRouteTableIsComplete(t *testing.T) {
	// Route registration never touches the pool, so an unconnected store is enough.
	handler := leakHandler(t, &postgres.Store{}, leakConfig(t, "postgres://localhost/jelee", 0))
	seen := leakWalk(t, handler)
	table := leakRouteTable()
	var problems []string
	for route := range seen {
		spec, ok := table[route]
		if !ok {
			problems = append(problems, route+": registered but not classified in leakRouteTable; add a leak check or an exemption with a reason")
			continue
		}
		method, pattern, _ := strings.Cut(route, " ")
		switch spec.mode {
		case leakExempt:
			if strings.TrimSpace(spec.reason) == "" {
				problems = append(problems, route+": exemption needs a reason")
			}
			continue
		case leakNoMedia:
			if strings.TrimSpace(spec.reason) == "" || (method != http.MethodGet && method != http.MethodHead) {
				problems = append(problems, route+": no-media checks need a reason and a safe method")
			}
		case leakByID:
			media := false
			for _, kind := range spec.params {
				media = media || kind == "item" || kind == "source" || kind == "library"
			}
			if !media {
				problems = append(problems, route+": by-ID check needs a media parameter")
			}
		case leakList, leakAdmin:
		default:
			problems = append(problems, route+": unknown mode")
		}
		names := leakPathParam.FindAllStringSubmatch(pattern, -1)
		if len(names) != len(spec.params) {
			problems = append(problems, route+": parameter table does not match the pattern")
		}
		for _, name := range names {
			if _, ok := spec.params[name[1]]; !ok {
				problems = append(problems, route+": parameter "+name[1]+" has no fixture kind")
			}
		}
	}
	for route := range table {
		if !seen[route] {
			problems = append(problems, route+": classified but no longer registered; remove the stale entry")
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
	// Default construction keeps the 404 semantics.
	if (config.Config{}).Access.HiddenContentStatus() != http.StatusNotFound {
		t.Fatal("hidden content default changed")
	}
}

type leakIDs struct {
	viewer, adminToken, viewerToken string
	item, source, library, job      [3]string // visible, hidden, missing
	markers                         []string
	visibleItem, visibleLibrary     string
}

const (
	leakVisible = iota
	leakHidden
	leakMissing
)

func (f leakIDs) value(kind string, scenario int) string {
	switch kind {
	case "item":
		return f.item[scenario]
	case "source":
		return f.source[scenario]
	case "library":
		return f.library[scenario]
	case "job":
		return f.job[scenario]
	case "self":
		return f.viewer
	case "image-type":
		return "Primary"
	case "tmdb":
		return "12"
	case "opaque":
		return "00000000-0000-4000-8000-000000000001"
	}
	panic("unknown leak fixture kind " + kind)
}

func leakUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// leakStore owns a fresh random schema in the dedicated jelee_test database.
func leakStore(t *testing.T) (context.Context, *postgres.Store, string) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required access leak PostgreSQL integration is unavailable")
		}
		t.Skip("access leak PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("access leak integration requires the dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated access leak test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("generate schema identifier")
	}
	schema := "jelee_leak_it_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned access leak schema")
	}
	t.Cleanup(func() {
		cleanCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(cleanCtx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error("remove owned access leak schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if version, dirty, e := postgres.Migrate(ctx, u.String(), "up"); e != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate access leak fixture to current schema")
	}
	store, err := postgres.Open(ctx, u.String(), 16)
	if err != nil {
		t.Fatal("open access leak test store")
	}
	t.Cleanup(store.Pool.Close)
	return ctx, store, u.String()
}

func leakFixture(t *testing.T, ctx context.Context, store *postgres.Store) leakIDs {
	t.Helper()
	var f leakIDs
	var err error
	if f.adminToken, err = store.Provision(ctx, "leak-admin", access.ClientNative, true); err != nil {
		t.Fatal(err)
	}
	if f.viewerToken, err = store.Provision(ctx, "leak-viewer", access.ClientNative, false); err != nil {
		t.Fatal(err)
	}
	var adminID, adminSession string
	if err = store.Pool.QueryRow(ctx, `SELECT u.id::text,s.id::text FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.name='leak-admin'`).Scan(&adminID, &adminSession); err != nil {
		t.Fatal(err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT id::text FROM users WHERE name='leak-viewer'`).Scan(&f.viewer); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	for scenario, spec := range map[int]struct{ library, root, title, file string }{
		leakVisible: {"Visible Leak Library", "visible-root", "Visible Leak Probe Title", "visible-file.mkv"},
		leakHidden:  {"Hidden Leak Library Qx7", "hidden-secret-root-Qx7", "Hidden Leak Probe Title Qx7", "hidden-secret-file-Qx7.mkv"},
	} {
		root := filepath.Join(base, spec.root)
		if err = os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, spec.file), []byte("Jelee synthetic leak probe\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var rootID string
		if err = store.Pool.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) RETURNING id::text`, spec.library).Scan(&f.library[scenario]); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, f.library[scenario], root).Scan(&rootID); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, f.library[scenario], spec.title).Scan(&f.item[scenario]); err != nil {
			t.Fatal(err)
		}
		if err = store.Pool.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`, f.item[scenario], f.library[scenario], rootID, spec.file).Scan(&f.source[scenario]); err != nil {
			t.Fatal(err)
		}
		// An asset row exercises the item_images resolver on the image routes.
		if _, err = store.Pool.Exec(ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Primary',0,'local',$3::uuid,$4)`, f.item[scenario], f.library[scenario], rootID, strings.TrimSuffix(spec.file, ".mkv")+"-poster.jpg"); err != nil {
			t.Fatal(err)
		}
		job, _, err := store.SubmitJob(ctx, domain.Actor{UserID: adminID, SessionID: adminSession, IP: "127.0.0.1"}, f.library[scenario], "leak-"+spec.root, domain.JobPriorityManual, config.DefaultJobsConfig().Policy())
		if err != nil {
			t.Fatal(err)
		}
		f.job[scenario] = job.ID
		if scenario == leakHidden {
			f.markers = []string{f.item[scenario], f.source[scenario], f.library[scenario], rootID, job.ID, spec.library, spec.root, spec.title, spec.file, root}
		} else {
			f.visibleItem, f.visibleLibrary = f.item[scenario], f.library[scenario]
		}
	}
	if _, err = store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, f.viewer, f.library[leakVisible]); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []*[3]string{&f.item, &f.source, &f.library, &f.job} {
		slot[leakMissing] = leakUUID(t)
	}
	return f
}

type leakResponse struct {
	status int
	code   string
	text   string
}

func leakRequest(t *testing.T, handler http.Handler, method, path, token string) leakResponse {
	t.Helper()
	var body io.Reader
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodDelete {
		body = strings.NewReader("{}")
	}
	r := httptest.NewRequest(method, "http://localhost"+path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Idempotency-Key", "leak-probe")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	var text strings.Builder
	text.WriteString(w.Body.String())
	for name, values := range w.Header() {
		fmt.Fprintf(&text, "\n%s: %s", name, strings.Join(values, ","))
	}
	return leakResponse{status: w.Code, code: envelope.Error.Code, text: text.String()}
}

func leakPath(pattern string, spec leakRoute, f leakIDs, scenario int) string {
	return leakPathParam.ReplaceAllStringFunc(pattern, func(m string) string {
		return f.value(spec.params[m[1:len(m)-1]], scenario)
	})
}

func (f leakIDs) assertNoMarkers(t *testing.T, route string, response leakResponse) {
	t.Helper()
	for _, marker := range f.markers {
		if strings.Contains(response.text, marker) {
			t.Errorf("%s: response leaks hidden marker %q (status %d)", route, marker, response.status)
		}
	}
}

func TestAccessLeakHiddenContentPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := leakFixture(t, ctx, store)
	for _, mode := range []struct {
		name   string
		status int
		code   string
	}{{"default_404", 0, "not_found"}, {"explicit_404", http.StatusNotFound, "not_found"}, {"configured_403", http.StatusForbidden, "forbidden"}} {
		t.Run(mode.name, func(t *testing.T) {
			cfg := leakConfig(t, dsn, mode.status)
			handler := leakHandler(t, store, cfg)
			want := cfg.Access.HiddenContentStatus()
			table := leakRouteTable()
			routes := make([]string, 0, len(table))
			for route := range leakWalk(t, handler) {
				routes = append(routes, route)
			}
			sort.Strings(routes)
			checked := 0
			for _, route := range routes {
				spec, ok := table[route]
				if !ok {
					t.Errorf("%s: registered but not classified", route)
					continue
				}
				method, pattern, _ := strings.Cut(route, " ")
				switch spec.mode {
				case leakExempt:
					continue
				case leakNoMedia:
					response := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					if response.status >= 500 {
						t.Errorf("%s: no-media route failed with %d", route, response.status)
					}
					f.assertNoMarkers(t, route, response)
				case leakList:
					path := leakPath(pattern, spec, f, leakHidden)
					response := leakRequest(t, handler, method, path, f.viewerToken)
					if response.status != http.StatusOK || !strings.Contains(response.text, f.visibleItem) && !strings.Contains(response.text, f.visibleLibrary) {
						t.Errorf("%s: viewer listing lost visible content (status %d)", route, response.status)
					}
					f.assertNoMarkers(t, route, response)
					if spec.control {
						if control := leakRequest(t, handler, method, path, f.adminToken); control.status != http.StatusOK || !strings.Contains(control.text, f.item[leakHidden]) {
							t.Errorf("%s: administrator control does not see the hidden fixture (status %d)", route, control.status)
						}
					}
				case leakAdmin:
					hidden := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					missing := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakMissing), f.viewerToken)
					if hidden.status != http.StatusForbidden || hidden.code != "forbidden" || missing.status != hidden.status || missing.code != hidden.code {
						t.Errorf("%s: administrator route answered a viewer with hidden=%d/%s missing=%d/%s", route, hidden.status, hidden.code, missing.status, missing.code)
					}
					f.assertNoMarkers(t, route, hidden)
				case leakByID:
					visible := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakVisible), f.viewerToken)
					if visible.status != http.StatusOK {
						t.Errorf("%s: visible control returned %d/%s", route, visible.status, visible.code)
					}
					hidden := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.viewerToken)
					missing := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakMissing), f.viewerToken)
					if hidden.status != want || missing.status != want || (method != http.MethodHead && (hidden.code != mode.code || missing.code != mode.code)) {
						t.Errorf("%s: hidden=%d/%s missing=%d/%s, want %d/%s for both", route, hidden.status, hidden.code, missing.status, missing.code, want, mode.code)
					}
					f.assertNoMarkers(t, route, hidden)
					f.assertNoMarkers(t, route, missing)
					if spec.control {
						if control := leakRequest(t, handler, method, leakPath(pattern, spec, f, leakHidden), f.adminToken); control.status != http.StatusOK {
							t.Errorf("%s: administrator control cannot reach the hidden fixture (%d/%s)", route, control.status, control.code)
						}
					}
				}
				checked++
			}
			if checked < 50 {
				t.Fatalf("only %d routes were exercised", checked)
			}
			t.Logf("access leak traversal exercised %d routes with hidden status %d", checked, want)
		})
	}
}
