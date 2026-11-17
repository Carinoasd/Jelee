package postgres

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

var namingLibraryFiles = []string{
	"新闻联播/新闻联播 2024-03-15.mp4",
	"新闻联播/新闻联播 2024-03-16.mp4",
	"Frieren/[SubsPlease] Frieren - 05 (1080p).mkv",
	"Frieren/[SubsPlease] Frieren - 06 (1080p).mkv",
	"Show/Show.S01E10.mkv",
	"Show/Show.S01E10-S02E01.mkv",
	"Title/Title S01E01.mkv",
	"Title/Title OVA 2.mkv",
	"Title/[Group] Title S01 OVA 03 [1080p].mkv",
	"Title/劇場版/Title (2016).mkv",
}

// itemOfPath returns the item a synchronised file belongs to, or "".
func itemOfPath(t *testing.T, f jobFixture, relative string) string {
	t.Helper()
	var item string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT item_id::text FROM media_sources WHERE library_id=$1::uuid AND relative_path=$2),'')`, f.registration.Library.ID, relative).Scan(&item)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// parentChain names an item's season and series titles.
func parentChain(t *testing.T, f jobFixture, item string) (season, series string) {
	t.Helper()
	err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(s.title,''),COALESCE(r.title,'') FROM item_parent_links p
 LEFT JOIN items s ON s.id=p.parent_id LEFT JOIN item_parent_links q ON q.item_id=s.id LEFT JOIN items r ON r.id=q.parent_id WHERE p.item_id=$1::uuid`, item).Scan(&season, &series)
	if err != nil {
		t.Fatal(err)
	}
	return season, series
}

func putNaming(t *testing.T, f jobFixture, o domain.CatalogNamingOptions, key string) {
	t.Helper()
	v, err := f.s.PutCatalogNaming(f.ctx, f.a, f.registration.Library.ID, o)
	if err != nil || v.CatalogNamingOptions != o || v.ParserVersion != domain.CatalogSyncParserVersionFor(o) {
		t.Fatalf("put naming %v %+v", err, v)
	}
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
}

// G21.3: cross-season files, OVA and theatrical classification with default
// options; switching the library's naming options plans the files again on
// the next synchronisation, and a locked identification survives it (G21.5).
func TestCatalogNamingOptionsReplanAndRespectLocks(t *testing.T) {
	f := newJobFixtureWithin(t, 240*time.Second)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, namingLibraryFiles...)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 2)
	defer stop()
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)

	// Defaults: dates and absolute numbers wait for confirmation.
	pending := pendingByPath(t, f)
	for _, p := range []string{"新闻联播/新闻联播 2024-03-15.mp4", "Frieren/[SubsPlease] Frieren - 05 (1080p).mkv", "Title/劇場版/Title (2016).mkv"} {
		if _, ok := pending[p]; !ok || itemOfPath(t, f, p) != "" {
			t.Fatalf("%s not pending: %+v", p, pending)
		}
	}
	// A cross-season file belongs to its first season and records the end
	// season; it never merges with the same-season episode.
	cross, single := itemOfPath(t, f, "Show/Show.S01E10-S02E01.mkv"), itemOfPath(t, f, "Show/Show.S01E10.mkv")
	if cross == "" || single == "" || cross == single {
		t.Fatal("cross-season file merged or missing", cross, single)
	}
	v := identityOf(t, f, cross)
	if intValue(v.Season) != 1 || intValue(v.Episode) != 10 || intValue(v.EpisodeEnd) != 1 || v.Scan == nil || intValue(v.Scan.SeasonEnd) != 2 {
		t.Fatalf("cross-season identity %+v scan %+v", v, v.Scan)
	}
	if season, series := parentChain(t, f, cross); season != "Season 1" || series != "Show" {
		t.Fatal("cross-season parent", season, series)
	}
	if s := identityOf(t, f, single); s.Scan == nil || s.Scan.SeasonEnd != nil || s.Scan.Special != "" {
		t.Fatalf("single episode carries naming facts %+v", s.Scan)
	}
	// OVA: Season 0 of the series, also when the name carries a bare season.
	for path, episode := range map[string]int{"Title/Title OVA 2.mkv": 2, "Title/[Group] Title S01 OVA 03 [1080p].mkv": 3} {
		item := itemOfPath(t, f, path)
		o := identityOf(t, f, item)
		if intValue(o.Season) != 0 || intValue(o.Episode) != episode || o.Scan == nil || o.Scan.Special != "ova" {
			t.Fatalf("%s: %+v scan %+v", path, o, o.Scan)
		}
		if season, series := parentChain(t, f, item); season != "Specials" || series != "Title" {
			t.Fatal("OVA parent", path, season, series)
		}
	}
	// Theatrical: confirmed as a standalone movie, classified theatrical.
	accepted, err := f.s.AcceptCatalogPending(f.ctx, f.a, f.registration.Library.ID, pending["Title/劇場版/Title (2016).mkv"].ID, domain.CatalogPendingDecision{})
	if err != nil || accepted.Kind != "Movie" {
		t.Fatal("accept theatrical", err, accepted)
	}
	if m := identityOf(t, f, accepted.ItemID); m.Kind != "Movie" || m.ParentID != "" || m.Scan == nil || m.Scan.Special != "theatrical" {
		t.Fatalf("theatrical movie %+v scan %+v", m, m.Scan)
	}
	// Lock Frieren 05 with a deliberately different number before the
	// library switches to absolute numbering.
	n5 := pending["Frieren/[SubsPlease] Frieren - 05 (1080p).mkv"].ID
	one, fifty := 1, 50
	if accepted, err = f.s.AcceptCatalogPending(f.ctx, f.a, f.registration.Library.ID, n5, domain.CatalogPendingDecision{Kind: "episode", Season: &one, Episode: &fifty}); err != nil {
		t.Fatal("accept Frieren", err)
	}
	frieren5 := accepted.ItemID

	// Enable both options: the next synchronisation plans every file again.
	all := domain.CatalogNamingOptions{DateEpisodes: true, AbsoluteEpisodes: true}
	putNaming(t, f, all, "naming-on")
	r := latestSyncReport(t, f)
	if r.Protected < 1 || r.Created < 3 {
		t.Fatalf("report after enabling options %+v", r)
	}
	if n := syncCount(t, f, `SELECT count(*) FROM catalog_scan_sources WHERE library_id=$1::uuid AND parser_version<>$2`, f.registration.Library.ID, domain.CatalogSyncParserVersionFor(all)); n != 0 {
		t.Fatal("sources not planned with the new options", n)
	}
	for path, want := range map[string]struct {
		episode int
		date    string
	}{"新闻联播/新闻联播 2024-03-15.mp4": {315, "2024-03-15"}, "新闻联播/新闻联播 2024-03-16.mp4": {316, "2024-03-16"}} {
		item := itemOfPath(t, f, path)
		d := identityOf(t, f, item)
		if item == "" || intValue(d.Season) != 2024 || intValue(d.Episode) != want.episode || d.Scan == nil || d.Scan.AirDate != want.date {
			t.Fatalf("%s: %+v scan %+v", path, d, d.Scan)
		}
		if season, series := parentChain(t, f, item); season != "Season 2024" || series != "新闻联播" {
			t.Fatal("daily parent", season, series)
		}
	}
	frieren6 := itemOfPath(t, f, "Frieren/[SubsPlease] Frieren - 06 (1080p).mkv")
	if a := identityOf(t, f, frieren6); intValue(a.Season) != 1 || intValue(a.Episode) != 6 || a.Scan == nil || intValue(a.Scan.Absolute) != 6 {
		t.Fatalf("absolute episode %+v scan %+v", a, a.Scan)
	}
	// The locked episode keeps its corrected number; the scan value under it
	// follows the new rule, and both episodes share one series.
	l := identityOf(t, f, frieren5)
	if !l.EpisodeLocked || intValue(l.Episode) != 50 || l.Scan == nil || intValue(l.Scan.Episode) != 5 || intValue(l.Scan.Absolute) != 5 {
		t.Fatalf("lock overwritten %+v scan %+v", l, l.Scan)
	}
	_, s5 := parentChain(t, f, frieren5)
	_, s6 := parentChain(t, f, frieren6)
	if s5 != "Frieren" || s6 != s5 {
		t.Fatal("absolute episodes split across series", s5, s6)
	}
	if len(pendingByPath(t, f)) != 0 {
		t.Fatalf("files still pending %+v", pendingByPath(t, f))
	}

	// Unchanged options: nothing is planned again.
	if _, _, err = f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "naming-same", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	waitJobsIdle(t, f)
	if r = latestSyncReport(t, f); r.Updated != 0 || r.Created != 0 || r.Unchanged != int64(len(namingLibraryFiles)) {
		t.Fatalf("unchanged options planned again %+v", r)
	}

	// Disabling the options again plans the files once more but never takes
	// registered items apart.
	putNaming(t, f, domain.CatalogNamingOptions{}, "naming-off")
	if r = latestSyncReport(t, f); r.Updated != int64(len(namingLibraryFiles)) || r.Created != 0 {
		t.Fatalf("report after disabling options %+v", r)
	}
	if itemOfPath(t, f, "新闻联播/新闻联播 2024-03-15.mp4") == "" || itemOfPath(t, f, "Frieren/[SubsPlease] Frieren - 06 (1080p).mkv") != frieren6 {
		t.Fatal("disabling options removed registered items")
	}
	if l = identityOf(t, f, frieren5); intValue(l.Episode) != 50 {
		t.Fatalf("lock lost after disabling options %+v", l)
	}
}

func TestCatalogNamingSettingsHTTP(t *testing.T) {
	f := newJobFixtureWithin(t, 120*time.Second)
	createAccount(t, f.ctx, f.s, f.a, "viewer")
	admin := accountLogin(t, f.ctx, f.s, "job-admin")
	viewer := accountLogin(t, f.ctx, f.s, "viewer")
	service, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig(), EnableCatalog: true}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	request := func(method, path, body, token string) (int, string) {
		t.Helper()
		r, err := http.NewRequestWithContext(f.ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		value, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, string(value)
	}
	naming := "/api/v1/libraries/" + f.registration.Library.ID + "/naming"
	missing := "/api/v1/libraries/00000000-0000-4000-8000-000000000000/naming"
	for _, c := range []struct{ method, path, body string }{{"GET", naming, ""}, {"PUT", naming, `{"dateEpisodes":true,"absoluteEpisodes":false}`}, {"GET", missing, ""}} {
		if status, _ := request(c.method, c.path, c.body, viewer.Token); status != 403 {
			t.Fatal("viewer", c.method, c.path, status)
		}
	}
	if status, _ := request("GET", naming, "", ""); status != 401 {
		t.Fatal("anonymous read", status)
	}
	if status, body := request("GET", naming, "", admin.Token); status != 200 || !strings.Contains(body, `"dateEpisodes":false`) || !strings.Contains(body, `"parserVersion":"`+domain.CatalogSyncParserVersion+`"`) {
		t.Fatal("default read", status, body)
	}
	for _, bad := range []string{`{}`, `{"dateEpisodes":true}`, `{"dateEpisodes":"yes","absoluteEpisodes":false}`, `{"dateEpisodes":true,"absoluteEpisodes":false,"x":1}`} {
		if status, _ := request("PUT", naming, bad, admin.Token); status != 400 {
			t.Fatal("invalid options accepted", bad, status)
		}
	}
	if status, _ := request("GET", missing, "", admin.Token); status != 404 {
		t.Fatal("missing library", status)
	}
	if status, _ := request("PUT", missing, `{"dateEpisodes":true,"absoluteEpisodes":true}`, admin.Token); status != 404 {
		t.Fatal("missing library write", status)
	}
	want := domain.CatalogSyncParserVersionFor(domain.CatalogNamingOptions{DateEpisodes: true})
	if status, body := request("PUT", naming, `{"dateEpisodes":true,"absoluteEpisodes":false}`, admin.Token); status != 200 || !strings.Contains(body, `"parserVersion":"`+want+`"`) {
		t.Fatal("write", status, body)
	}
	// Repeating the same options writes no second audit row.
	if status, _ := request("PUT", naming, `{"dateEpisodes":true,"absoluteEpisodes":false}`, admin.Token); status != 200 {
		t.Fatal("repeat", status)
	}
	if status, body := request("GET", naming, "", admin.Token); status != 200 || !strings.Contains(body, `"dateEpisodes":true`) || !strings.Contains(body, `"absoluteEpisodes":false`) {
		t.Fatal("read back", status, body)
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event='library.catalog_naming_changed' AND target_id=$1::uuid`, f.registration.Library.ID) != 1 {
		t.Fatal("audit rows")
	}
}

// The previous schema has no naming options: a configured library refuses
// the downgrade; with the options off the migration round-trips and keeps
// synchronised items.
func TestCatalogNamingMigrationRefusesEnabledOptions(t *testing.T) {
	f := newJobFixture(t)
	if _, err := f.s.PutCatalogNaming(f.ctx, f.a, f.registration.Library.ID, domain.CatalogNamingOptions{AbsoluteEpisodes: true}); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "catalog_naming_options", "enabled naming options were downgraded")
}

func TestCatalogNamingMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Show S01E10-S02E01','Episode') RETURNING id::text`, f.registration.Library.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,season,episode,episode_end,season_end,special) VALUES($1::uuid,$2::uuid,'Episode',sha256('x'::bytea),$3,'Show',1,10,1,2,'ova')`, item, f.registration.Library.ID, domain.CatalogSyncParserVersion); err != nil {
		t.Fatal(err)
	}
	// The end season must follow the start season.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE catalog_scan_items SET season_end=1 WHERE item_id=$1::uuid`, item); err == nil {
		t.Fatal("season_end not after season accepted")
	}
	if _, err := f.s.PutCatalogNaming(f.ctx, f.a, f.registration.Library.ID, domain.CatalogNamingOptions{}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "catalog_naming_options")
	dsn := f.s.Pool.Config().ConnString()
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatal("disabled options refused the downgrade", version, dirty, err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade again", version, dirty, err)
	}
	var seasonEnd *int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT season_end FROM catalog_scan_items WHERE item_id=$1::uuid`, item).Scan(&seasonEnd); err != nil || seasonEnd != nil {
		t.Fatal("round trip lost the item or kept the dropped column", err, seasonEnd)
	}
	if _, err := f.s.GetCatalogNaming(f.ctx, f.a, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutCatalogNaming(f.ctx, f.a, "00000000-0000-4000-8000-000000000000", domain.CatalogNamingOptions{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing library", err)
	}
}
