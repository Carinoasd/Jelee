package outbound_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/jackc/pgx/v5"
)

func metadataApplyStore(t *testing.T) (context.Context, *postgres.Store, domain.SessionGrant, domain.LibraryRegistration) {
	t.Helper()
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required metadata database unavailable")
		}
		t.Skip("metadata TLS/HTTP/PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/jelee_test" || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		t.Fatal("metadata fixture requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated metadata database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "jelee_metadata_it_" + hex.EncodeToString(random[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("create owned metadata schema")
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(clean, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("remove owned metadata schema")
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate owned metadata schema")
	}
	store, err := postgres.Open(ctx, u.String(), 16)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Pool.Close)
	const hash = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdC1maXh0dXJl$dGVzdC1kaWdlc3QtZml4dHVyZQ"
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "metadata-admin", DisplayName: "Metadata admin", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	credentials, err := store.Credentials(ctx, "metadata-admin")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.CommitLogin(ctx, domain.LoginInput{Credentials: credentials, PasswordOK: true, DeviceName: "metadata-fixture", IP: "127.0.0.1", MaxSessions: 8, SessionTTL: time.Hour, LockAfter: 3, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	library, err := store.RegisterLibrary(ctx, "metadata-fixture", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, grant, library
}

type metadataHTTPResult struct {
	status int
	body   []byte
	err    error
}

func TestTMDBMetadataThroughTLSHTTPAndPostgres(t *testing.T) {
	ctx, store, grant, library := metadataApplyStore(t)
	actor := domain.Actor{UserID: grant.User.ID, SessionID: grant.Session.ID, IP: "127.0.0.1"}
	key := strings.Repeat("a", 32)
	cert, roots := providerCertificate(t)
	var movieSearch, seriesSearch, movieDetails, seriesDetails, allCalls, retries atomic.Int32
	var fusionRetries atomic.Int32
	var providerActions sync.Map
	cancelStarted, cancelFinished := make(chan struct{}), make(chan struct{})
	fusionCancelStarted, fusionCancelFinished := make(chan struct{}), make(chan struct{})
	fallbackCancelStarted, fallbackCancelFinished := make(chan struct{}), make(chan struct{})
	blocked, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allCalls.Add(1)
		if r.Host != "api.themoviedb.org" || r.TLS == nil || r.TLS.ServerName != "api.themoviedb.org" || r.URL.Query().Get("api_key") != key || !domain.ValidMetadataLanguage(r.URL.Query().Get("language")) {
			t.Error("governed provider request differs")
		}
		resource := "movie"
		if strings.Contains(r.URL.Path, "/tv") {
			resource = "series"
		}
		search := strings.HasPrefix(r.URL.Path, "/3/search/")
		var id int
		if search {
			q := r.URL.Query().Get("query")
			index := strings.LastIndexByte(q, '_')
			if index < 0 {
				w.WriteHeader(400)
				return
			}
			id, _ = strconv.Atoi(q[index+1:])
		} else {
			parts := strings.Split(r.URL.Path, "/")
			id, _ = strconv.Atoi(parts[len(parts)-1])
		}
		if id <= 0 {
			w.WriteHeader(404)
			return
		}
		if action, ok := providerActions.Load(id); ok {
			action.(func())()
		}
		if id < 1000 {
			if search {
				if resource == "movie" {
					movieSearch.Add(1)
				} else {
					seriesSearch.Add(1)
				}
			} else {
				if resource == "movie" {
					movieDetails.Add(1)
				} else {
					seriesDetails.Add(1)
				}
			}
		}
		if id == 4006 || id == 4751 || id == 4850 {
			started, finished := cancelStarted, cancelFinished
			if id == 4751 {
				started, finished = fusionCancelStarted, fusionCancelFinished
			}
			if id == 4850 {
				started, finished = fallbackCancelStarted, fallbackCancelFinished
			}
			close(started)
			<-r.Context().Done()
			close(finished)
			return
		}
		if id == 4007 {
			close(blocked)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if id == 4005 && retries.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if id == 4750 && fusionRetries.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if id == 4004 {
			fmt.Fprint(w, `{"id":4004,"title":null}`)
			return
		}
		title := fmt.Sprintf("Film_%d", id)
		nameKey, originalKey, dateKey := "title", "original_title", "release_date"
		if resource == "series" {
			title = fmt.Sprintf("Series_%d", id)
			nameKey, originalKey, dateKey = "name", "original_name", "first_air_date"
		}
		overview := "Provider overview"
		if id == 4003 && r.URL.Query().Get("language") != "en-US" {
			overview = ""
		}
		data := map[string]any{"id": id, nameKey: title, originalKey: "Original " + title, "overview": overview, dateKey: "2024-02-29"}
		w.Header().Set("Content-Type", "application/json")
		if search {
			yearKey := "primary_release_year"
			if resource == "series" {
				yearKey = "first_air_date_year"
			}
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("include_adult") != "false" || r.URL.Query().Get(yearKey) != "2024" {
				t.Error("search constraints differ")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"page": 1, "total_pages": 1, "total_results": 1, "results": []any{data}})
		} else {
			_ = json.NewEncoder(w).Encode(data)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client, err := outbound.NewMappedTestClient(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Error("provider dial was not pinned")
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := metadata.NewTMDBWithClient(key, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	service, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	service, err = service.WithLibraryPreferences(store)
	if err != nil {
		t.Fatal(err)
	}
	service, err = service.WithItemMetadata(store)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	service, err = service.WithNFOItemFields(reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWith(func(name string) (string, bool) {
		if name == "JELEE_DATABASE_URL" {
			return store.Pool.Config().ConnString(), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnableAccounts = true
	cfg.EnableCatalog = true
	cfg.TMDBAPIKey = key
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(store, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, store, app.NewCatalog(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	api.Client().Timeout = 20 * time.Second
	do := func(callCtx context.Context, method, path, body, token string) metadataHTTPResult {
		r, err := http.NewRequestWithContext(callCtx, method, api.URL+path, strings.NewReader(body))
		if err != nil {
			return metadataHTTPResult{err: err}
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := api.Client().Do(r)
		if err != nil {
			return metadataHTTPResult{err: err}
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if len(data) > 1<<20 {
			return metadataHTTPResult{status: response.StatusCode, err: errors.New("metadata fixture response exceeds bound")}
		}
		return metadataHTTPResult{status: response.StatusCode, body: data, err: err}
	}
	request := func(method, path, body string) metadataHTTPResult {
		result := do(ctx, method, path, body, grant.Token)
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result
	}
	newItem := func(kind string) string {
		var id string
		if err := store.Pool.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Initial fixture',$2) RETURNING id::text`, library.Library.ID, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	apply := func(item, resource string, id int, revision int64) metadataHTTPResult {
		return request("POST", "/api/v1/items/"+item+"/metadata/tmdb", fmt.Sprintf(`{"resource":%q,"providerId":%d,"expectedRevision":%d,"confirmed":true,"replaceExistingTitle":true}`, resource, id, revision))
	}
	decode := func(response metadataHTTPResult) domain.MetadataApplyResult {
		t.Helper()
		var body struct {
			Data domain.MetadataApplyResult `json:"data"`
		}
		if response.status != 200 || json.Unmarshal(response.body, &body) != nil {
			t.Fatalf("provider apply status=%d validJSON=%v responseBytes=%d", response.status, json.Valid(response.body), len(response.body))
		}
		return body.Data
	}

	invalidItem := newItem("Movie")
	invalidPath := "/api/v1/items/" + invalidItem + "/metadata/tmdb"
	validBody := `{"resource":"movie","providerId":3999,"expectedRevision":1,"confirmed":true}`
	if result := do(ctx, "POST", invalidPath, validBody, ""); result.err != nil || result.status != 401 {
		t.Fatal("anonymous provider apply", result.err, result.status)
	}
	const userHash = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdC1maXh0dXJl$dGVzdC1kaWdlc3QtZml4dHVyZQ"
	if _, _, err = store.CreateUser(ctx, actor, domain.UserInput{Name: "metadata-viewer", DisplayName: "Viewer", Locale: "en-US", PasswordHash: userHash}, "metadata-viewer"); err != nil {
		t.Fatal(err)
	}
	viewerCredentials, err := store.Credentials(ctx, "metadata-viewer")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.CommitLogin(ctx, domain.LoginInput{Credentials: viewerCredentials, PasswordOK: true, DeviceName: "viewer", IP: "127.0.0.1", MaxSessions: 8, SessionTTL: time.Hour, LockAfter: 3, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if result := do(ctx, "POST", invalidPath, validBody, viewer.Token); result.err != nil || result.status != 403 {
		t.Fatal("nonadmin provider apply", result.err, result.status)
	}
	for _, body := range []string{`{"resource":"movie","providerId":3999,"expectedRevision":1}`, `{"resource":"movie","providerId":3999,"expectedRevision":1,"confirmed":false}`, `{"resource":"movie","providerId":0,"expectedRevision":1,"confirmed":true}`, `{"resource":"movie","providerId":3999,"expectedRevision":1,"confirmed":true,"language":null}`, `{"resource":"movie","providerId":3999,"expectedRevision":1,"confirmed":true,"replaceExistingTitle":null}`, `{"resource":"movie","providerId":3999,"expectedRevision":1,"confirmed":true,"sourceUrl":"https://example.com"}`} {
		if result := request("POST", invalidPath, body); result.status != 400 {
			t.Fatal("invalid provider intent", result.status)
		}
	}
	if allCalls.Load() != 0 {
		t.Fatal("denied provider intent reached network")
	}

	// Exercise actual NFO fusion before the large matrix so source-priority
	// mutations fail in this full HTTP/TLS/files/database path immediately.
	var rootID, rootPath string
	if err := store.Pool.QueryRow(ctx, `SELECT id::text,path FROM library_roots WHERE library_id=$1::uuid`, library.Library.ID).Scan(&rootID, &rootPath); err != nil {
		t.Fatal(err)
	}
	newNFOItem := func(name, kind, document string) (string, string) {
		t.Helper()
		item := newItem(kind)
		file := filepath.Join(rootPath, name+".nfo")
		if err := os.WriteFile(file, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootPath, name+".mkv"), []byte("original media fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, item, library.Library.ID, rootID, name+".mkv"); err != nil {
			t.Fatal(err)
		}
		return item, file
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, library.Library.ID); err != nil {
		t.Fatal(err)
	}
	document := `<movie><title>Local NFO title</title><premiered>2024-01-01</premiered><lockedfields>Name</lockedfields></movie>`
	// Production filename selection runs through the same HTTP/TLS/database path.
	genericMovie := filepath.Join(rootPath, "MOVIE.NFO")
	genericDocument := `<movie><title>Generic directory movie</title></movie>`
	if err := os.WriteFile(genericMovie, []byte(genericDocument), 0600); err != nil {
		t.Fatal(err)
	}
	specific, specificFile := newNFOItem("selector", "Movie", `<movie><title>Specific selected movie</title></movie>`)
	uppercaseSpecific := filepath.Join(rootPath, "SELECTOR.NFO")
	if err := os.Rename(specificFile, uppercaseSpecific); err != nil {
		t.Fatal(err)
	}
	if result := decode(apply(specific, "movie", 4800, 1)); result.Metadata.Fields[0].Value != "Specific selected movie" || result.Metadata.Fields[0].Source != "nfo" || result.Metadata.Revision != 2 {
		t.Fatal("HTTP NFO selection ignored specific filename priority")
	}
	generic, genericAdjacent := newNFOItem("generic-selector", "Movie", document)
	if err := os.Remove(genericAdjacent); err != nil {
		t.Fatal(err)
	}
	if result := decode(apply(generic, "movie", 4801, 1)); result.Metadata.Fields[0].Value != "Generic directory movie" || result.Metadata.Fields[0].NFOOrigin == nil {
		t.Fatal("HTTP conventional movie NFO was not applied")
	}
	genericSeries := filepath.Join(rootPath, "TVSHOW.NFO")
	seriesDocument := `<tvshow><title>Generic directory series</title></tvshow>`
	if err := os.WriteFile(genericSeries, []byte(seriesDocument), 0600); err != nil {
		t.Fatal(err)
	}
	seriesItem, seriesAdjacent := newNFOItem("series-selector", "Series", seriesDocument)
	if err := os.Remove(seriesAdjacent); err != nil {
		t.Fatal(err)
	}
	if result := decode(apply(seriesItem, "series", 4802, 1)); result.Metadata.Fields[0].Value != "Generic directory series" || result.Metadata.Fields[0].Source != "nfo" {
		t.Fatal("HTTP conventional tvshow NFO was not applied")
	}
	appearing, appearingFile := newNFOItem("appearing-selector", "Movie", document)
	if err := os.Remove(appearingFile); err != nil {
		t.Fatal(err)
	}
	providerActions.Store(4803, func() {
		if err := os.WriteFile(appearingFile, []byte(document), 0600); err != nil {
			t.Error(err)
		}
	})
	if result := apply(appearing, "movie", 4803, 1); result.status != 409 {
		t.Fatal("higher-priority NFO appeared during lookup but committed", result.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, appearing); err != nil || value.Revision != 1 || value.Fields[0].Source != "existing" {
		t.Fatal("selection conflict left partial write", err)
	}
	if err := os.Remove(genericMovie); err != nil {
		t.Fatal(err)
	}
	candidateChange, _ := newNFOItem("candidate-change", "Movie", document)
	providerActions.Store(4804, func() {
		if err := os.WriteFile(genericMovie, []byte(genericDocument), 0600); err != nil {
			t.Error(err)
		}
	})
	if result := apply(candidateChange, "movie", 4804, 1); result.status != 409 {
		t.Fatal("NFO candidate set changed during lookup but committed", result.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, candidateChange); err != nil || value.Revision != 1 {
		t.Fatal("candidate change left partial write", err)
	}
	if err := os.Remove(genericMovie); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(genericSeries); err != nil {
		t.Fatal(err)
	}
	collision, collisionFile := newNFOItem("case-collision", "Movie", document)
	collisionUpper := filepath.Join(rootPath, "CASE-COLLISION.NFO")
	if err := os.WriteFile(collisionUpper, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	beforeCollisionCalls := allCalls.Load()
	if result := apply(collision, "movie", 4805, 1); result.status != 503 || allCalls.Load() != beforeCollisionCalls {
		t.Fatal("ambiguous case-fold NFO reached provider", result.status)
	}
	if err := os.Remove(collisionUpper); err != nil {
		t.Fatal(err)
	}
	if original, err := os.ReadFile(collisionFile); err != nil || string(original) != document {
		t.Fatal("ambiguous NFO original changed", err)
	}
	for offset, directive := range []string{`<lockdata>true</lockdata>`, `<lockedfields>Overview|Unknown</lockedfields>`} {
		lockDocument := `<movie><title>Partial locked NFO</title>` + directive + `</movie>`
		item, file := newNFOItem(fmt.Sprintf("independent-lock-%d", offset), "Movie", lockDocument)
		result := decode(apply(item, "movie", 4900+offset, 1))
		if offset == 0 && (len(result.TMDB.Applied) != 0 || len(result.TMDB.Skipped) != 4) {
			t.Fatal("HTTP global NFO lock failed to protect absent fields")
		}
		if offset == 1 && len(result.TMDB.Applied) != 2 {
			t.Fatal("HTTP named NFO lock did not preserve unrelated provider fields")
		}
		stored, err := store.ItemMetadata(ctx, actor, item)
		if err != nil || stored.Revision != 2 {
			t.Fatal("HTTP lock persistence unavailable", err)
		}
		for _, field := range stored.Fields {
			locked := offset == 0 || field.Field == "overview"
			if locked && (field.NFOLockOrigin == nil || field.NFOLockOrigin.Stamp.SHA256 != stored.LastConfirmedNFOObservation.Stamp.SHA256) {
				t.Fatal("HTTP NFO lock proof was not persisted", field.Field)
			}
			if locked && field.Field != "title" && (field.Value != "" || field.Source != "existing" || field.NFOOrigin != nil || field.ProviderOrigin != nil) {
				t.Fatal("HTTP absent-field lock invented text provenance", field.Field)
			}
		}
		if original, err := os.ReadFile(file); err != nil || string(original) != lockDocument {
			t.Fatal("lock review altered original NFO", err)
		}
	}
	for offset, mode := range []string{"missing", "invalid"} {
		item, file := newNFOItem("state-guard-"+mode, "HomeVideo", `<movie><title>broken`)
		if mode == "missing" {
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}
		beforeCalls := allCalls.Load()
		if response := request("POST", "/api/v1/items/"+item+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`); response.status != 503 {
			t.Fatal("nonvalid NFO reached NFO-only write", mode, response.status)
		}
		if value, err := store.ItemMetadata(ctx, actor, item); err != nil || value.Revision != 1 || value.Kind != "HomeVideo" || value.Fields[0].Source != "existing" {
			t.Fatal("nonvalid NFO guard changed catalog", mode, err)
		}
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.tmdb_metadata_applied','item.nfo_metadata_applied')`, item).Scan(&count); err != nil || count != 0 {
			t.Fatal("nonvalid NFO guard left audit", mode, err)
		}
		status := domain.NFOItemObservedMissing
		if mode == "invalid" {
			status = domain.NFOItemObservedInvalid
		}
		response := apply(item, "movie", 4830+offset, 1)
		result := decode(response)
		confirmed := result.Metadata.LastConfirmedNFOObservation
		if allCalls.Load() != beforeCalls+1 || result.Metadata.Revision != 2 || result.Metadata.Kind != "Movie" || result.NFO == nil || result.NFO.Status != status || len(result.NFO.Applied) != 0 || len(result.TMDB.Applied) != 4 || confirmed == nil || confirmed.Status != status || confirmed.AcceptedRevision != 2 || !domain.ValidLastConfirmedNFOObservation(*confirmed) {
			t.Fatal("trusted NFO fallback did not atomically preserve observation", mode)
		}
		if mode == "missing" && confirmed.Stamp != nil {
			t.Fatal("missing fallback invented NFO stamp")
		}
		if mode == "invalid" {
			hash := sha256.Sum256([]byte(`<movie><title>broken`))
			if confirmed.Stamp == nil || confirmed.Stamp.SHA256 != hex.EncodeToString(hash[:]) {
				t.Fatal("invalid fallback lost actual original bytes")
			}
		}
		for _, field := range result.Metadata.Fields {
			if field.Source != "tmdb" || field.NFOOrigin != nil {
				t.Fatal("missing/invalid fallback invented NFO fields", mode)
			}
		}
		if strings.Contains(string(response.body), file) || strings.Contains(string(response.body), rootPath) || strings.Contains(string(response.body), `<movie><title>broken`) {
			t.Fatal("fallback exposed private NFO data")
		}
		var local int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE event='item.nfo_metadata_applied') FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.tmdb_metadata_applied','item.nfo_metadata_applied')`, item).Scan(&count, &local); err != nil || count != 1 || local != 0 {
			t.Fatal("fallback left separate observation/NFO audit", mode, err)
		}
		if mode == "invalid" {
			if original, err := os.ReadFile(file); err != nil || string(original) != `<movie><title>broken` {
				t.Fatal("invalid NFO original changed", err)
			}
		}
	}
	assertNoObservation := func(item string) {
		t.Helper()
		if value, err := store.ItemMetadata(ctx, actor, item); err != nil || value.Revision != 1 || value.Kind != "HomeVideo" || value.Fields[0].Source != "existing" || value.LastConfirmedNFOObservation != nil {
			t.Fatal("failed fallback left partial observation or metadata", err)
		}
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.tmdb_metadata_applied','item.nfo_metadata_applied')`, item).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed fallback left apply audit", err)
		}
	}
	for offset, mode := range []string{"invalid-bytes", "missing-appears", "invalid-repaired"} {
		item, file := newNFOItem("state-change-"+mode, "HomeVideo", `<movie><title>broken`)
		if mode == "missing-appears" {
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}
		providerActions.Store(4840+offset, func() {
			changed := document
			var stamp time.Time
			if mode == "invalid-bytes" {
				info, err := os.Stat(file)
				if err != nil {
					t.Error(err)
					return
				}
				stamp, changed = info.ModTime(), `<movie><title>broKen`
			}
			if err := os.WriteFile(file, []byte(changed), 0600); err != nil {
				t.Error(err)
			}
			if !stamp.IsZero() {
				if err := os.Chtimes(file, stamp, stamp); err != nil {
					t.Error(err)
				}
			}
		})
		if response := apply(item, "movie", 4840+offset, 1); response.status != 409 {
			t.Fatal("changed fallback observation committed", mode, response.status)
		}
		assertNoObservation(item)
	}
	for offset, directive := range []string{`<lockdata>true</lockdata>`, `<lockedfields>Overview|Unknown</lockedfields>`} {
		lockOnlyDocument := `<movie>` + directive + `</movie>`
		item, file := newNFOItem(fmt.Sprintf("lock-only-%d", offset), "HomeVideo", lockOnlyDocument)
		beforeCalls := allCalls.Load()
		local := decode(request("POST", "/api/v1/items/"+item+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`))
		if allCalls.Load() != beforeCalls || len(local.Applied) != 0 || local.Metadata.Kind != "HomeVideo" || local.Metadata.Revision != 2 || local.NFO == nil || local.NFO.Status != domain.NFOItemObservedValid || local.Metadata.LastConfirmedNFOObservation.Status != domain.NFOItemObservedValid {
			t.Fatal("HTTP lock-only NFO invented text, classification or fallback")
		}
		for _, field := range local.Metadata.Fields {
			if field.NFOLockOrigin != nil && (field.NFOLockOrigin.Projection != domain.NFOItemLockFieldsVersion || field.Source != "existing" || field.NFOOrigin != nil || field.ProviderOrigin != nil || field.UpdatedAt != nil) {
				t.Fatal("HTTP lock-only NFO invented text provenance")
			}
		}
		fused := decode(apply(item, "movie", 4910+offset, 2))
		if fused.NFO.Status != domain.NFOItemObservedValid || len(fused.NFO.Applied) != 0 || fused.Metadata.Revision != 3 || allCalls.Load() != beforeCalls+1 {
			t.Fatal("HTTP lock-only fusion state or provider calls differ")
		}
		if offset == 0 && (len(fused.TMDB.Applied) != 0 || len(fused.TMDB.Skipped) != 4 || fused.Metadata.Kind != "HomeVideo") {
			t.Fatal("HTTP lock-only global intent did not protect provider fields")
		}
		if offset == 1 && (len(fused.TMDB.Applied) != 3 || len(fused.TMDB.Skipped) != 1 || fused.TMDB.Skipped[0].Field != "overview" || fused.Metadata.Kind != "Movie") {
			t.Fatal("HTTP lock-only named intent blocked unrelated fields or lost lock")
		}
		stored, err := store.ItemMetadata(ctx, actor, item)
		if err != nil || stored.Revision != 3 {
			t.Fatal("HTTP lock-only persistence unavailable", err)
		}
		var auditCount int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.nfo_metadata_applied','item.tmdb_metadata_applied')`, item).Scan(&auditCount); err != nil || auditCount != 2 {
			t.Fatal("lock-only reviews did not commit one audit each", err)
		}
		if original, err := os.ReadFile(file); err != nil || string(original) != lockOnlyDocument {
			t.Fatal("lock-only HTTP review changed original NFO", err)
		}
	}
	changedLockDocument := `<movie><lockedfields>Overview</lockedfields></movie>`
	changedLockItem, changedLockFile := newNFOItem("lock-only-changed", "HomeVideo", changedLockDocument)
	changedLockInfo, err := os.Stat(changedLockFile)
	if err != nil {
		t.Fatal(err)
	}
	providerActions.Store(4912, func() {
		if err := os.WriteFile(changedLockFile, []byte(strings.Replace(changedLockDocument, "Overview", "Name    ", 1)), 0600); err != nil {
			t.Error(err)
		}
		if err := os.Chtimes(changedLockFile, changedLockInfo.ModTime(), changedLockInfo.ModTime()); err != nil {
			t.Error(err)
		}
	})
	changedLockResponse := apply(changedLockItem, "movie", 4912, 1)
	if err := os.WriteFile(changedLockFile, []byte(changedLockDocument), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(changedLockFile, changedLockInfo.ModTime(), changedLockInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if changedLockResponse.status != 409 {
		t.Fatal("changed lock-only intent was committed", changedLockResponse.status)
	}
	assertNoObservation(changedLockItem)
	lockOnlyProviderError, _ := newNFOItem("lock-only-provider-error", "HomeVideo", `<movie><lockdata>true</lockdata></movie>`)
	if response := apply(lockOnlyProviderError, "movie", 4004, 1); response.status != 503 {
		t.Fatal("provider failure saved lock-only observation", response.status)
	}
	assertNoObservation(lockOnlyProviderError)
	t.Log("actual HTTP/TLS/NFO/PostgreSQL lock-only: NFO-only and fusion, global and named locks, preserved text provenance/classification, one audit per review, changed intent409 and provider error rollback PASS")
	for offset, aliasDocument := range []string{
		`<movie><title>First</title><name>Second</name></movie>`,
		`<movie><title>First</title><name/><lockdata>true</lockdata></movie>`,
		`<movie><title>First</title><premiered>2024-01-01</premiered><releasedate>2025-01-01</releasedate></movie>`,
	} {
		item, file := newNFOItem(fmt.Sprintf("ambiguous-alias-%d", offset), "HomeVideo", aliasDocument)
		beforeCalls := allCalls.Load()
		if response := request("POST", "/api/v1/items/"+item+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`); response.status != 503 {
			t.Fatal("HTTP conflicting aliases reached NFO-only write", response.status)
		}
		if response := apply(item, "movie", 4930+offset, 1); response.status != 503 || allCalls.Load() != beforeCalls {
			t.Fatal("HTTP conflicting aliases triggered provider fallback", response.status)
		}
		assertNoObservation(item)
		if raw, err := os.ReadFile(file); err != nil || string(raw) != aliasDocument {
			t.Fatal("ambiguous original changed", err)
		}
	}
	aliasItem, _ := newNFOItem("single-alias", "HomeVideo", `<movie><localtitle>Alias title</localtitle><releasedate>2024-05-06</releasedate><actor><name>Actor one</name></actor><actor><name>Actor two</name></actor></movie>`)
	aliasResult := decode(apply(aliasItem, "movie", 4938, 1))
	if len(aliasResult.NFO.Applied) != 2 || aliasResult.Metadata.Fields[0].Value != "Alias title" || aliasResult.Metadata.Fields[3].Value != "2024-05-06" || aliasResult.Metadata.Fields[0].Source != "nfo" || aliasResult.Metadata.Fields[3].Source != "nfo" {
		t.Fatal("HTTP single aliases or nested names lost compatibility")
	}
	t.Log("actual HTTP/TLS/NFO/PostgreSQL aliases: conflicting title/date aliases and empty alias rejected503 before provider/writes; single aliases and nested actor names remain supported PASS")
	sortDocument := `<movie><title>Display title</title><originaltitle>Original title</originaltitle><plot>Local plot</plot><premiered>2024-05-06</premiered><sortname>Sorting title</sortname><lockedfields>SortName</lockedfields></movie>`
	sortItem, sortFile := newNFOItem("sort-title", "HomeVideo", sortDocument)
	sortResult := decode(apply(sortItem, "movie", 4939, 1))
	if sortResult.Metadata.Revision != 2 || len(sortResult.Metadata.Fields) != 5 || len(sortResult.NFO.Applied) != 5 || len(sortResult.TMDB.Applied) != 0 {
		t.Fatal("HTTP NFO sort title was dropped from fusion")
	}
	sortField := sortResult.Metadata.Fields[4]
	if sortField.Field != "sortTitle" || sortField.Value != "Sorting title" || sortField.Source != "nfo" || sortField.NFOOrigin == nil || sortField.NFOOrigin.Projection != domain.NFOItemSortFieldsVersion || sortField.NFOLockOrigin == nil || !sortField.NFOOrigin.Locked {
		t.Fatal("HTTP sort title lost NFO provenance or lock")
	}
	cleared := request("PUT", "/api/v1/items/"+sortItem+"/metadata", `{"expectedRevision":2,"fields":[{"field":"sortTitle","value":""}]}`)
	if cleared.status != 200 {
		t.Fatal("HTTP manual sort title clear failed", cleared.status)
	}
	sortResult = decode(apply(sortItem, "movie", 4940, 3))
	sortField = sortResult.Metadata.Fields[4]
	if sortField.Value != "" || sortField.Source != "manual" || sortField.NFOOrigin != nil || sortField.NFOLockOrigin == nil {
		t.Fatal("HTTP NFO overwrote manual sort title clear")
	}
	if raw, err := os.ReadFile(sortFile); err != nil || string(raw) != sortDocument {
		t.Fatal("sort title review changed original NFO", err)
	}
	conflictSortItem, _ := newNFOItem("sort-title-alias-conflict", "HomeVideo", `<movie><sorttitle>First</sorttitle><sortname>Second</sortname></movie>`)
	beforeSortCalls := allCalls.Load()
	if response := apply(conflictSortItem, "movie", 4941, 1); response.status != 503 || allCalls.Load() != beforeSortCalls {
		t.Fatal("HTTP sort title alias conflict triggered provider or write", response.status)
	}
	assertNoObservation(conflictSortItem)
	sortLockItem, _ := newNFOItem("sort-title-lock-only", "HomeVideo", `<movie><lockedfields>SortName</lockedfields></movie>`)
	sortLockResult := decode(request("POST", "/api/v1/items/"+sortLockItem+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`))
	if len(sortLockResult.Applied) != 0 || sortLockResult.Metadata.Kind != "HomeVideo" || len(sortLockResult.Metadata.Fields) != 2 || sortLockResult.Metadata.Fields[1].Field != "sortTitle" || sortLockResult.Metadata.Fields[1].NFOLockOrigin == nil || sortLockResult.Metadata.Fields[1].NFOOrigin != nil {
		t.Fatal("HTTP sort title lock-only review invented text or lost intent")
	}
	t.Log("actual HTTP/TLS/NFO/PostgreSQL sortTitle: five NFO fields fused with provenance/locks, manual clear preserved, conflicting aliases rejected before provider, absent-value lock-only accepted without text PASS")
	textDocument := `<movie><title>Display title</title><originaltitle>Original title</originaltitle><plot>Long plot</plot><premiered>2024-05-06</premiered><sorttitle>Sorting title</sorttitle><tagline>Short tagline</tagline><outline>Short outline</outline><mpaa>PG-13</mpaa><certification>TW:12</certification><lockdata>true</lockdata></movie>`
	textItem, textFile := newNFOItem("extended-text", "HomeVideo", textDocument)
	textResult := decode(apply(textItem, "movie", 4942, 1))
	if len(textResult.NFO.Applied) != 9 || len(textResult.Metadata.Fields) != 9 || len(textResult.TMDB.Applied) != 0 || textResult.Metadata.Revision != 2 {
		t.Fatal("HTTP extended text did not fuse nine fields")
	}
	for _, field := range textResult.Metadata.Fields {
		if field.Source != "nfo" || field.NFOOrigin == nil || field.NFOOrigin.Projection != domain.NFOItemTextFieldsVersion || field.NFOLockOrigin == nil {
			t.Fatal("HTTP extended text lost field provenance or lock", field.Field)
		}
	}
	maxTextPatches := []domain.ItemMetadataPatch{}
	for _, field := range textResult.Metadata.Fields {
		value := strings.Repeat("<", 1024)
		if field.Field == "overview" || field.Field == "outline" {
			value = strings.Repeat("<", 16384)
		}
		if field.Field == "date" {
			value = "2024-05-06"
		}
		maxTextPatches = append(maxTextPatches, domain.ItemMetadataPatch{Field: field.Field, Value: &value})
	}
	maxTextBody, err := json.Marshal(map[string]any{"expectedRevision": 2, "fields": maxTextPatches})
	if err != nil {
		t.Fatal(err)
	}
	if response := request("PUT", "/api/v1/items/"+textItem+"/metadata", string(maxTextBody)); response.status != 200 {
		t.Fatal("HTTP nine valid maximum text fields exceeded request envelope", response.status)
	}
	textResult = decode(apply(textItem, "movie", 4943, 3))
	if len(textResult.Applied) != 0 || textResult.Metadata.Revision != 4 {
		t.Fatal("extended NFO overwrote manual maximum values")
	}
	for _, field := range textResult.Metadata.Fields {
		if field.Source != "manual" || field.NFOOrigin != nil || field.NFOLockOrigin == nil {
			t.Fatal("manual text ownership or renewed lock changed", field.Field)
		}
	}
	ratingLockItem, _ := newNFOItem("official-rating-lock-only", "HomeVideo", `<movie><lockedfields>OfficialRating</lockedfields></movie>`)
	ratingLock := decode(request("POST", "/api/v1/items/"+ratingLockItem+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`))
	if len(ratingLock.Applied) != 0 || ratingLock.Metadata.Kind != "HomeVideo" || len(ratingLock.Metadata.Fields) != 3 {
		t.Fatal("HTTP official rating lock-only invented text or classification")
	}
	for _, field := range ratingLock.Metadata.Fields[1:] {
		if field.NFOLockOrigin == nil || field.NFOOrigin != nil || field.UpdatedAt != nil || field.Source != "existing" {
			t.Fatal("HTTP missing rating value lost independent intent")
		}
	}
	duplicateTextItem, _ := newNFOItem("duplicate-tagline", "HomeVideo", `<movie><tagline>First</tagline><TAGLINE>Second</TAGLINE></movie>`)
	beforeTextCalls := allCalls.Load()
	if response := apply(duplicateTextItem, "movie", 4944, 1); response.status != 503 || allCalls.Load() != beforeTextCalls {
		t.Fatal("HTTP duplicate text reached provider or write", response.status)
	}
	assertNoObservation(duplicateTextItem)
	duplicateYearItem, duplicateYearFile := newNFOItem("duplicate-year", "HomeVideo", `<movie><title>Unconfirmed</title><year>2024</year><YEAR>2025</YEAR></movie>`)
	beforeYearCalls := allCalls.Load()
	if response := apply(duplicateYearItem, "movie", 4945, 1); response.status != 503 || allCalls.Load() != beforeYearCalls {
		t.Fatal("HTTP ambiguous year reached provider or write", response.status)
	}
	assertNoObservation(duplicateYearItem)
	if response := request("POST", "/api/v1/items/"+duplicateYearItem+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`); response.status != 503 {
		t.Fatal("HTTP ambiguous year reached NFO-only write", response.status)
	}
	assertNoObservation(duplicateYearItem)
	for offset, content := range []string{
		`<runtime>92 min</runtime><RUNTIME>93 minutes</RUNTIME>`,
		`<rating>8.5</rating><communityrating>9</communityrating>`,
		`<userrating>8</userrating><USERRATING>9</USERRATING>`,
	} {
		item, _ := newNFOItem(fmt.Sprintf("duplicate-numeric-%d", offset), "HomeVideo", `<movie><title>Unconfirmed</title>`+content+`</movie>`)
		beforeCalls := allCalls.Load()
		if response := apply(item, "movie", 4946+offset, 1); response.status != 503 || allCalls.Load() != beforeCalls {
			t.Fatal("HTTP ambiguous numeric content reached provider or write", response.status)
		}
		if response := request("POST", "/api/v1/items/"+item+"/metadata/nfo", `{"expectedRevision":1,"confirmed":true}`); response.status != 503 {
			t.Fatal("HTTP ambiguous numeric content reached NFO-only write", response.status)
		}
		assertNoObservation(item)
	}
	if raw, err := os.ReadFile(duplicateYearFile); err != nil || string(raw) != `<movie><title>Unconfirmed</title><year>2024</year><YEAR>2025</YEAR></movie>` {
		t.Fatal("ambiguous year review changed original NFO", err)
	}
	if raw, err := os.ReadFile(textFile); err != nil || string(raw) != textDocument {
		t.Fatal("extended text review changed original NFO", err)
	}
	t.Log("actual HTTP/TLS/NFO/PostgreSQL extended text: nine fields fused with source/locks, maximum valid nine-field manual body accepted, original bytes preserved PASS")
	for offset, mode := range []string{"unsafe", "empty", "permission", "unknown-lock", "false-lock"} {
		content := `<!DOCTYPE movie [<!ENTITY unsafe SYSTEM "file:///private">]><movie><title>&unsafe;</title></movie>`
		if mode == "empty" {
			content = `<movie/>`
		}
		if mode == "unknown-lock" {
			content = `<movie><lockedfields>Unknown</lockedfields></movie>`
		}
		if mode == "false-lock" {
			content = `<movie><lockdata>false</lockdata></movie>`
		}
		item, file := newNFOItem("state-unavailable-"+mode, "HomeVideo", content)
		if mode == "permission" {
			if err := os.Chmod(file, 0000); err != nil {
				t.Fatal(err)
			}
		}
		beforeCalls := allCalls.Load()
		response := apply(item, "movie", 4860+offset, 1)
		if mode == "permission" {
			if err := os.Chmod(file, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if response.status != 503 || allCalls.Load() != beforeCalls {
			t.Fatal("unsafe or unavailable input triggered fallback", mode, response.status)
		}
		assertNoObservation(item)
	}
	failedFallback, failedFallbackFile := newNFOItem("state-provider-error", "HomeVideo", document)
	if err := os.Remove(failedFallbackFile); err != nil {
		t.Fatal(err)
	}
	if response := apply(failedFallback, "movie", 4004, 1); response.status != 503 {
		t.Fatal("provider failure accepted missing fallback", response.status)
	}
	assertNoObservation(failedFallback)
	cancelFallback, cancelFallbackFile := newNFOItem("state-provider-cancel", "HomeVideo", document)
	if err := os.Remove(cancelFallbackFile); err != nil {
		t.Fatal(err)
	}
	fallbackCtx, cancelFallbackRequest := context.WithCancel(ctx)
	fallbackDone := make(chan metadataHTTPResult, 1)
	go func() {
		fallbackDone <- do(fallbackCtx, "POST", "/api/v1/items/"+cancelFallback+"/metadata/tmdb", `{"resource":"movie","providerId":4850,"expectedRevision":1,"confirmed":true}`, grant.Token)
	}()
	select {
	case <-fallbackCancelStarted:
	case <-time.After(3 * time.Second):
		cancelFallbackRequest()
		t.Fatal("missing fallback cancellation did not reach provider")
	}
	cancelFallbackRequest()
	if response := <-fallbackDone; !errors.Is(response.err, context.Canceled) {
		t.Fatal("missing fallback request cancellation lost", response.err)
	}
	select {
	case <-fallbackCancelFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("missing fallback cancellation did not reach provider context")
	}
	assertNoObservation(cancelFallback)
	t.Log("actual HTTP/TLS/NFO/PostgreSQL fallback: missing and corrupt original bytes, one revision/audit and retained safe status; appearance/repair/same-size-mtime byte change conflict; unsafe/lock-only/permission/provider failure leave no observation PASS")
	t.Log("actual HTTP/TLS/NFO/PostgreSQL selection: specific and conventional movie/tvshow names, case folding, higher-priority appearance, secondary candidate change, ambiguous names denied before provider PASS")
	for offset, mode := range []string{"root", "parent", "media", "nfo"} {
		name := "identity-" + mode
		if mode == "parent" {
			name = "identity-parent/film"
			if err := os.Mkdir(filepath.Join(rootPath, "identity-parent"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		item, nfoFile := newNFOItem(name, "HomeVideo", document)
		mediaFile := filepath.Join(rootPath, name+".mkv")
		nfoInfo, err := os.Stat(nfoFile)
		if err != nil {
			t.Fatal(err)
		}
		mediaInfo, err := os.Stat(mediaFile)
		if err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(t.TempDir(), "backup")
		target := rootPath
		switch mode {
		case "parent":
			target = filepath.Dir(nfoFile)
		case "media":
			target = mediaFile
		case "nfo":
			target = nfoFile
		}
		var moved atomic.Bool
		restore := func() {
			if moved.Swap(false) {
				// target was created solely by this owned fixture replacement.
				if err := os.RemoveAll(target); err != nil {
					t.Error(err)
				}
				if err := os.Rename(backup, target); err != nil {
					t.Error(err)
				}
			}
		}
		t.Cleanup(restore)
		providerID := 4820 + offset
		providerActions.Store(providerID, func() {
			if err := os.Rename(target, backup); err != nil {
				t.Error(err)
				return
			}
			moved.Store(true)
			if mode == "root" || mode == "parent" {
				if err := os.MkdirAll(filepath.Dir(nfoFile), 0700); err != nil {
					t.Error(err)
					return
				}
			}
			if mode != "media" {
				if err := os.WriteFile(nfoFile, []byte(document), 0600); err != nil {
					t.Error(err)
					return
				}
				if err := os.Chtimes(nfoFile, nfoInfo.ModTime(), nfoInfo.ModTime()); err != nil {
					t.Error(err)
					return
				}
			}
			if mode != "nfo" {
				if err := os.WriteFile(mediaFile, []byte("original media fixture"), 0600); err != nil {
					t.Error(err)
					return
				}
				if err := os.Chtimes(mediaFile, mediaInfo.ModTime(), mediaInfo.ModTime()); err != nil {
					t.Error(err)
				}
			}
		})
		response := apply(item, "movie", providerID, 1)
		restore()
		if response.status != 409 {
			t.Fatal("physical NFO ownership replacement committed", mode, response.status)
		}
		if value, err := store.ItemMetadata(ctx, actor, item); err != nil || value.Revision != 1 || value.Kind != "HomeVideo" || value.Fields[0].Source != "existing" {
			t.Fatal("physical ownership conflict left partial metadata", mode, err)
		}
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.tmdb_metadata_applied','item.nfo_metadata_applied')`, item).Scan(&count); err != nil || count != 0 {
			t.Fatal("physical ownership conflict left audit", mode, count, err)
		}
		if original, err := os.ReadFile(nfoFile); err != nil || string(original) != document {
			t.Fatal("original NFO did not survive owned replacement fixture", mode, err)
		}
		if original, err := os.ReadFile(mediaFile); err != nil || string(original) != "original media fixture" {
			t.Fatal("original media did not survive owned replacement fixture", mode, err)
		}
	}
	disappearing, disappearingFile := newNFOItem("identity-disappearing", "HomeVideo", document)
	disappearingBackup := filepath.Join(t.TempDir(), "original.nfo")
	providerActions.Store(4824, func() {
		if err := os.Rename(disappearingFile, disappearingBackup); err != nil {
			t.Error(err)
		}
	})
	disappearance := apply(disappearing, "movie", 4824, 1)
	if err := os.Rename(disappearingBackup, disappearingFile); err != nil {
		t.Fatal(err)
	}
	if disappearance.status != 409 {
		t.Fatal("valid NFO disappearance did not conflict", disappearance.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, disappearing); err != nil || value.Revision != 1 || value.Kind != "HomeVideo" {
		t.Fatal("NFO disappearance left partial update", err)
	}
	t.Log("actual HTTP/TLS/NFO/PostgreSQL physical ownership: root, parent, media and NFO replaced with same bytes/size/mtime; 409, unchanged revision/kind/fields and zero audits PASS")
	fused, fusedFile := newNFOItem("fusion", "HomeVideo", document)
	if response := request("PUT", "/api/v1/items/"+fused+"/metadata", `{"expectedRevision":1,"fields":[{"field":"overview","value":""}]}`); response.status != 200 {
		t.Fatal("fusion manual clear", response.status)
	}
	fusion := decode(apply(fused, "movie", 4700, 2))
	if fusion.Metadata.Revision != 3 || fusion.Metadata.Kind != "Movie" || fusion.NFO == nil || fusion.TMDB == nil || len(fusion.Applied) != 3 || len(fusion.Skipped) != 1 || len(fusion.NFO.Applied) != 2 || len(fusion.TMDB.Applied) != 1 || fusion.Metadata.Fields[0].Value != "Local NFO title" || fusion.Metadata.Fields[0].Source != "nfo" || !fusion.Metadata.Fields[0].NFOOrigin.Locked || fusion.Metadata.Fields[1].Source != "tmdb" || fusion.Metadata.Fields[2].Value != "" || fusion.Metadata.Fields[2].Source != "manual" || fusion.Metadata.Fields[3].Source != "nfo" {
		t.Fatal("HTTP TMDB fusion overwrote NFO or manual fields")
	}
	if original, err := os.ReadFile(fusedFile); err != nil || string(original) != document {
		t.Fatal("fusion modified original NFO")
	}
	if original, err := os.ReadFile(filepath.Join(rootPath, "fusion.mkv")); err != nil || string(original) != "original media fixture" {
		t.Fatal("fusion modified original media")
	}
	var totalAudits, localAudits int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE event='item.nfo_metadata_applied') FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.nfo_metadata_applied','item.tmdb_metadata_applied')`, fused).Scan(&totalAudits, &localAudits); err != nil || totalAudits != 1 || localAudits != 0 {
		t.Fatal("HTTP fusion committed separately", err, totalAudits, localAudits)
	}
	if response := apply(fused, "movie", 4700, 2); response.status != 409 {
		t.Fatal("stale fusion accepted", response.status)
	}

	changed, changedFile := newNFOItem("fusion-changed", "Movie", document)
	providerActions.Store(4701, func() {
		if err := os.WriteFile(changedFile, []byte(strings.Replace(document, "Local NFO title", "Changed NFO title", 1)), 0600); err != nil {
			t.Error(err)
		}
	})
	if response := apply(changed, "movie", 4701, 1); response.status != 409 {
		t.Fatal("NFO changed during provider lookup committed", response.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, changed); err != nil || value.Revision != 1 || value.Fields[0].Value != "Initial fixture" {
		t.Fatal("changed fusion left NFO writes", err)
	}

	concurrentFusion, _ := newNFOItem("fusion-manual", "Movie", document)
	providerActions.Store(4702, func() {
		manualCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		response := do(manualCtx, "PUT", "/api/v1/items/"+concurrentFusion+"/metadata", `{"expectedRevision":1,"fields":[{"field":"title","value":"Manual during fusion"}]}`, grant.Token)
		if response.err != nil || response.status != 200 {
			t.Errorf("fusion held DB locks during network: status=%d error=%v", response.status, response.err)
		}
	})
	if response := apply(concurrentFusion, "movie", 4702, 1); response.status != 409 {
		t.Fatal("fusion overwrote concurrent manual edit", response.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, concurrentFusion); err != nil || value.Revision != 2 || value.Fields[0].Value != "Manual during fusion" || value.Fields[0].NFOOrigin != nil {
		t.Fatal("concurrent fusion manual value lost", err)
	}

	generationFusion, _ := newNFOItem("fusion-generation", "Movie", document)
	providerActions.Store(4703, func() {
		if _, err := store.Pool.Exec(ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, library.Library.ID); err != nil {
			t.Error(err)
		}
	})
	if response := apply(generationFusion, "movie", 4703, 1); response.status != 409 {
		t.Fatal("fusion ignored NFO generation change", response.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, generationFusion); err != nil || value.Revision != 1 {
		t.Fatal("generation conflict left fused write", err)
	}
	invalidFusion, _ := newNFOItem("fusion-invalid", "Movie", document)
	if response := apply(invalidFusion, "movie", 4004, 1); response.status != 503 {
		t.Fatal("invalid upstream committed local NFO", response.status)
	}
	if value, err := store.ItemMetadata(ctx, actor, invalidFusion); err != nil || value.Revision != 1 || value.Fields[0].Source != "existing" {
		t.Fatal("upstream failure left NFO half-write", err)
	}
	rootFusion, _ := newNFOItem("fusion-root", "Movie", document)
	replacedRoot := t.TempDir()
	providerActions.Store(4704, func() {
		if _, err := store.Pool.Exec(ctx, `UPDATE library_roots SET path=$2 WHERE id=$1::uuid`, rootID, replacedRoot); err != nil {
			t.Error(err)
		}
	})
	if response := apply(rootFusion, "movie", 4704, 1); response.status != 409 {
		t.Fatal("fusion ignored trusted root change", response.status)
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE library_roots SET path=$2 WHERE id=$1::uuid`, rootID, rootPath); err != nil {
		t.Fatal(err)
	}
	if value, err := store.ItemMetadata(ctx, actor, rootFusion); err != nil || value.Revision != 1 {
		t.Fatal("root conflict left fused write", err)
	}
	retryFusion, _ := newNFOItem("fusion-retry", "Movie", document)
	if result := decode(apply(retryFusion, "movie", 4750, 1)); result.Metadata.Revision != 2 || result.NFO == nil || fusionRetries.Load() != 2 {
		t.Fatal("429 fusion did not commit once")
	}
	cancelFusion, _ := newNFOItem("fusion-cancel", "Movie", document)
	fusionCtx, cancelFusionRequest := context.WithCancel(ctx)
	fusionDone := make(chan metadataHTTPResult, 1)
	go func() {
		fusionDone <- do(fusionCtx, "POST", "/api/v1/items/"+cancelFusion+"/metadata/tmdb", `{"resource":"movie","providerId":4751,"expectedRevision":1,"confirmed":true}`, grant.Token)
	}()
	select {
	case <-fusionCancelStarted:
	case <-time.After(3 * time.Second):
		cancelFusionRequest()
		t.Fatal("fusion cancellation fixture not reached")
	}
	cancelFusionRequest()
	if result := <-fusionDone; !errors.Is(result.err, context.Canceled) {
		t.Fatal("fusion incoming cancellation lost", result.err)
	}
	select {
	case <-fusionCancelFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("fusion cancellation did not reach provider")
	}
	if value, err := store.ItemMetadata(ctx, actor, cancelFusion); err != nil || value.Revision != 1 || value.Fields[0].Source != "existing" {
		t.Fatal("cancelled provider left NFO half-write", err)
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE libraries SET nfo_mode='off',nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, library.Library.ID); err != nil {
		t.Fatal(err)
	}
	t.Log("actual HTTP/TLS/NFO/PostgreSQL fusion: mixed sources, manual clear, NFO lock, one revision/audit, changed file, concurrent manual, generation/root conflict, 429, cancellation and provider failure PASS")
	// Run the lock case first so the production-guard negative test fails before
	// spending a minute on the full governed fixture matrix.
	protected := newItem("Movie")
	first := decode(apply(protected, "movie", 4001, 1))
	lockBody := `{"expectedRevision":2,"fields":[{"field":"title","locked":true},{"field":"overview","value":""}]}`
	if response := request("PUT", "/api/v1/items/"+protected+"/metadata", lockBody); response.status != 200 {
		t.Fatal("HTTP lock update failed", response.status)
	}
	second := decode(apply(protected, "movie", 4002, 3))
	if second.Metadata.Fields[0].Value != first.Metadata.Fields[0].Value || !second.Metadata.Fields[0].Locked || second.Metadata.Fields[0].ProviderOrigin.ProviderID != 4001 || second.Metadata.Fields[2].Value != "" || second.Metadata.Fields[2].Source != "manual" || second.Metadata.Fields[2].ProviderOrigin != nil || len(second.Applied) != 2 || len(second.Skipped) != 2 {
		t.Fatal("HTTP TMDB overwrote locked or manual field")
	}

	for _, resource := range []string{"movie", "series"} {
		count, kind, prefix, path := 100, "Movie", "Film", "movies"
		if resource == "series" {
			count, kind, prefix, path = 20, "Series", "Series", "series"
		}
		for id := 1; id <= count; id++ {
			item := newItem(kind)
			title := fmt.Sprintf("%s_%d", prefix, id)
			query := url.Values{"query": {title}, "year": {"2024"}, "libraryId": {library.Library.ID}}
			search := request("GET", "/api/v1/metadata/tmdb/"+path+"?"+query.Encode(), "")
			var matches struct {
				Data struct {
					Candidates []struct {
						Movie             domain.MovieCandidate  `json:"movie"`
						Series            domain.SeriesCandidate `json:"series"`
						ExactTitle        bool                   `json:"exactTitle"`
						ExactYear         bool                   `json:"exactYear"`
						NeedsConfirmation bool                   `json:"needsConfirmation"`
					} `json:"candidates"`
				} `json:"data"`
			}
			if search.status != 200 || json.Unmarshal(search.body, &matches) != nil || len(matches.Data.Candidates) != 1 {
				t.Fatal("matrix candidate query failed", resource, id, search.status)
			}
			candidate := matches.Data.Candidates[0]
			selected := candidate.Movie.ProviderID
			if resource == "series" {
				selected = candidate.Series.ProviderID
			}
			if selected != int32(id) || !candidate.ExactTitle || !candidate.ExactYear || !candidate.NeedsConfirmation {
				t.Fatal("matrix matching or confirmation differs", resource, id)
			}
			before, err := store.ItemMetadata(ctx, actor, item)
			if err != nil || before.Revision != 1 {
				t.Fatal("search automatically wrote item", err)
			}
			result := decode(apply(item, resource, int(selected), 1))
			if result.Metadata.Revision != 2 || result.Metadata.Kind != kind || len(result.Applied) != 4 || len(result.Skipped) != 0 || result.Metadata.Fields[0].Value != title {
				t.Fatal("matrix metadata write differs", resource, id)
			}
			for _, field := range result.Metadata.Fields {
				if field.Source != "tmdb" || field.ProviderOrigin == nil || field.ProviderOrigin.ProviderID != int32(id) || field.ProviderOrigin.Resource != resource || field.ProviderOrigin.SourceURL != domain.TMDBSourceURL(resource, int32(id)) || field.ProviderOrigin.RequestedLanguage != "zh-CN" || field.ProviderOrigin.FetchedAt.IsZero() {
					t.Fatal("matrix field provenance differs", resource, id)
				}
			}
			catalog, err := store.GetItem(ctx, actor.UserID, item)
			if err != nil || catalog.Title != title {
				t.Fatal("matrix actual catalog differs", err)
			}
		}
	}
	if movieSearch.Load() != 100 || movieDetails.Load() != 100 || seriesSearch.Load() != 20 || seriesDetails.Load() != 20 {
		t.Fatal("matrix did not use actual search and detail endpoints")
	}
	t.Log(`matrix: movies=100 series=20 exactTitleAndYear=120 confirmedWrites=120 writeFailures=0; synthetic matching fixtures, original-media inventory worker remains outside this matrix`)

	fallback := decode(apply(newItem("Movie"), "movie", 4003, 1))
	if fallback.Metadata.Fields[0].ProviderOrigin.RequestedLanguage != "zh-CN" || fallback.Metadata.Fields[2].ProviderOrigin.RequestedLanguage != "en-US" {
		t.Fatal("fallback provenance collapsed")
	}
	malformed := newItem("Movie")
	if response := apply(malformed, "movie", 4004, 1); response.status != 503 {
		t.Fatal("invalid provider response persisted", response.status)
	}
	unchanged, err := store.ItemMetadata(ctx, actor, malformed)
	if err != nil || unchanged.Revision != 1 {
		t.Fatal("invalid response changed state", err)
	}
	decode(apply(newItem("Movie"), "movie", 4005, 1))
	if retries.Load() != 2 {
		t.Fatal("429 recovery did not retry")
	}

	cancelItem := newItem("Movie")
	callCtx, cancel := context.WithCancel(ctx)
	done := make(chan metadataHTTPResult, 1)
	go func() {
		done <- do(callCtx, "POST", "/api/v1/items/"+cancelItem+"/metadata/tmdb", `{"resource":"movie","providerId":4006,"expectedRevision":1,"confirmed":true}`, grant.Token)
	}()
	select {
	case <-cancelStarted:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("cancel fixture did not start")
	}
	cancel()
	if result := <-done; !errors.Is(result.err, context.Canceled) {
		t.Fatal("real HTTP request did not cancel", result.err)
	}
	select {
	case <-cancelFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request left running after cancellation")
	}
	unchanged, err = store.ItemMetadata(ctx, actor, cancelItem)
	if err != nil || unchanged.Revision != 1 {
		t.Fatal("cancelled provider write changed item", err)
	}

	concurrent := newItem("Movie")
	go func() {
		done <- do(ctx, "POST", "/api/v1/items/"+concurrent+"/metadata/tmdb", `{"resource":"movie","providerId":4007,"expectedRevision":1,"confirmed":true,"replaceExistingTitle":true}`, grant.Token)
	}()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent fixture did not start")
	}
	manualDone := make(chan metadataHTTPResult, 1)
	go func() {
		manualDone <- do(ctx, "PUT", "/api/v1/items/"+concurrent+"/metadata", `{"expectedRevision":1,"fields":[{"field":"title","value":"Manual during fetch"}]}`, grant.Token)
	}()
	select {
	case result := <-manualDone:
		if result.err != nil || result.status != 200 {
			t.Fatal("manual edit during fetch failed", result.err, result.status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider fetch held a database lock")
	}
	releaseOnce.Do(func() { close(release) })
	if result := <-done; result.err != nil || result.status != 409 {
		t.Fatal("network completion overwrote a newer revision", result.err, result.status)
	}
	unchanged, err = store.ItemMetadata(ctx, actor, concurrent)
	if err != nil || unchanged.Revision != 2 || unchanged.Fields[0].Value != "Manual during fetch" || unchanged.Fields[0].Source != "manual" {
		t.Fatal("concurrent manual value lost", err)
	}

	// A read-only library without a unique trusted item NFO source must still
	// reject the write before fetching a provider candidate.
	if _, err = store.Pool.Exec(ctx, `UPDATE libraries SET nfo_mode='read-only' WHERE id=$1::uuid`, library.Library.ID); err != nil {
		t.Fatal(err)
	}
	beforeCalls := allCalls.Load()
	if response := apply(newItem("Movie"), "movie", 4500, 1); response.status != 503 || allCalls.Load() != beforeCalls {
		t.Fatal("NFO-enabled library bypassed local-data guard", response.status)
	}
	t.Log("actual loopback HTTP/TLS/PostgreSQL: locked/manual fields, fallback origin, invalid response, 429, cancellation, concurrent edits and NFO availability guard PASS")
}
