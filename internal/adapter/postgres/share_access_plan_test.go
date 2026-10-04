package postgres

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// networkPlanLibraries other libraries carry network rules, so the rule
// table is not trivially small.
const networkPlanLibraries = 300

// lanScope and wanScope are request parameters as requestScopeArg binds
// them for a LAN and an Internet client.
const (
	lanScope = `{"ip":"192.168.10.20","lan":true,"kind":"native"}`
	wanScope = `{"ip":"203.0.113.9","lan":false,"kind":"native"}`
)

// TestNetworkAccessPlanPostgres is the G48.5/G48.8 evidence: the request
// parameter is pushed into the statement, the network rules are read once
// per statement (InitPlan) whatever the catalog size, and a library hidden
// from the request costs no item reads.
func TestNetworkAccessPlanPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	seedContentPlanLibrary(t, f)
	lib := f.registration.Library.ID
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO libraries(name) SELECT 'net plan '||n FROM generate_series(1,$1::int) n`, networkPlanLibraries)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_network_rules(library_id,network,cidrs,client_kinds)
 SELECT id,'any',ARRAY['198.51.100.0/24'::cidr],'{native}' FROM libraries WHERE name LIKE 'net plan %'`)
	if _, err := f.s.CreateNetworkRule(f.ctx, f.a, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", CIDRs: []string{}, ClientKinds: []string{}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `ANALYZE`)
	viewer := f.viewer.principal.UserID
	rulesOnce := func(stage string, plan map[string]any) {
		t.Helper()
		planNodes(plan, func(node map[string]any) {
			relation, _ := node["Relation Name"].(string)
			loops, _ := node["Actual Loops"].(float64)
			if relation == "library_network_rules" && loops > 1 {
				t.Errorf("%s: network rules read %.0f times; want once per statement", stage, loops)
			}
			if relation == "items" && node["Node Type"] == "Seq Scan" && stage != "library count" {
				t.Errorf("%s: sequential scan on items", stage)
			}
			if repeatedGrantScan(node) {
				t.Errorf("%s: library_acl scanned per row", stage)
			}
		})
	}

	// LAN: the bounded page reads a bounded number of items.
	page, raw := explainContentPlan(t, f, listItemsSQL, viewer, "", 50, lanScope)
	rulesOnce("lan list page", page)
	if visited := countPlanItemRows(page); visited < 50 || visited > 200 {
		t.Fatalf("LAN list page visited %.0f item rows of %d", visited, contentPlanItems)
	}
	t.Logf("LAN list page: %s; plan: %s", planSummary(page), raw)
	// WAN: the hidden library is dropped before its page is read.
	page, raw = explainContentPlan(t, f, listItemsSQL, viewer, "", 50, wanScope)
	rulesOnce("wan list page", page)
	if visited := countPlanItemRows(page); visited > 0 {
		t.Fatalf("WAN list page read %.0f item rows of a hidden library", visited)
	}
	t.Logf("WAN list page: %s; plan: %s", planSummary(page), raw)

	// A whole-library count reads the library's items (the total needs
	// them) but evaluates the network rules once.
	count := `SELECT count(*) FROM users u JOIN items i ON i.library_id=$2::uuid WHERE u.id=$1::uuid AND ` + itemVisibleSQL("$3", "i.library_id", "i.id")
	var visible int
	if err := f.s.Pool.QueryRow(f.ctx, count, viewer, lib, lanScope).Scan(&visible); err != nil || visible != contentPlanItems+len(f.all(false)) {
		t.Fatalf("LAN visible %d of %d: %v", visible, contentPlanItems+len(f.all(false)), err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, count, viewer, lib, wanScope).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("WAN visible %d: %v", visible, err)
	}
	whole, raw := explainContentPlan(t, f, count, viewer, lib, lanScope)
	rulesOnce("library count", whole)
	t.Logf("LAN library count: %s; plan: %s", planSummary(whole), raw)

	// By ID: index lookups only.
	var episode string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM items WHERE title='plan episode 4 2'`).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	detail, raw := explainContentPlan(t, f, `SELECT i.id FROM users u JOIN items i ON i.id=$2::uuid WHERE u.id=$1::uuid AND `+itemVisibleSQL("$3", "i.library_id", "i.id"), viewer, episode, lanScope)
	rulesOnce("by id", detail)
	t.Logf("LAN by-ID: %s; plan: %s", planSummary(detail), raw)

	// A guest of an item share: the share probes are index lookups.
	share := f.share(t, domain.ShareInput{ItemID: f.items["series-pg"]})
	guest := f.guestOf(t, share.Token, false)
	page, raw = explainContentPlan(t, f, listItemsSQL, guest.principal.UserID, "", 50, lanScope)
	rulesOnce("guest list page", page)
	// The share's subtree is walked, not its library: a handful of item
	// rows, and share_links (a few rows, so the planner may scan it) is
	// read per walked item at most.
	if visited := countPlanItemRows(page); visited > 20 {
		t.Errorf("item share guest list page read %.0f item rows", visited)
	}
	planNodes(page, func(node map[string]any) {
		loops, _ := node["Actual Loops"].(float64)
		if node["Relation Name"] == "share_links" && loops > 20 {
			t.Errorf("guest list page: share_links read %.0f times", loops)
		}
	})
	if items, err := f.s.ListItems(scoped(f.ctx, guest, lanAddress, access.ClientWeb, nil), guest.principal.UserID, "", 50); err != nil || len(items) != 4 {
		t.Fatalf("item share guest lists %d items: %v", len(items), err)
	}
	t.Logf("guest list page: %s; plan: %s", planSummary(page), raw)
}

// planSummary condenses a plan for the test log: execution time, item rows
// read, network rule reads and the relations read by sequential scans.
func planSummary(plan map[string]any) string {
	reads, seq := 0.0, map[string]bool{}
	planNodes(plan, func(node map[string]any) {
		relation, _ := node["Relation Name"].(string)
		loops, _ := node["Actual Loops"].(float64)
		if relation == "library_network_rules" {
			reads += loops
		}
		if node["Node Type"] == "Seq Scan" {
			seq[relation] = true
		}
	})
	names := make([]string, 0, len(seq))
	for name := range seq {
		names = append(names, name)
	}
	slices.Sort(names)
	ms, _ := plan["Execution Time"].(float64)
	return fmt.Sprintf("%.1f ms, %.0f item rows, network rule scans %.0f, sequential scans %v", ms, countPlanItemRows(plan), reads, names)
}

// TestNetworkAccessStatementCountPostgres asserts the statements of one
// list and one detail request (G48.8): the request parameter rides on the
// same statements, so a network-restricted request costs exactly what an
// unrestricted one does, from inside or outside the LAN. A guest request
// adds one audit statement for its first use of a route in a minute.
func TestNetworkAccessStatementCountPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	if _, err := f.s.CreateNetworkRule(f.ctx, f.a, domain.NetworkRuleInput{LibraryID: f.other.Library.ID, Network: "lan", CIDRs: []string{}, ClientKinds: []string{}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	counter := &statementCounter{}
	cfg, err := pgxpool.ParseConfig(f.s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := &Store{Pool: pool}
	catalog, err := app.NewCatalog(store).WithPlayback(store)
	if err == nil {
		catalog, err = catalog.WithBrowse(store)
	}
	if err == nil {
		catalog, err = catalog.WithDetails(store)
	}
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test",
		MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableCatalog: true, EnableDirect: true}, store, catalog, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.s.Provision(f.ctx, "count-network", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid),($1::uuid,$3::uuid)`, p.UserID, f.registration.Library.ID, f.other.Library.ID)
	g, err := f.s.CreateShare(f.ctx, f.a, domain.ShareInput{LibraryID: f.registration.Library.ID, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1, AllowPlayback: true})
	if err != nil {
		t.Fatal(err)
	}
	guest, err := f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: g.Token, Native: true, Client: domain.NativeClient{Name: "count", DeviceID: "count"}, MaxSessions: 4, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, token, address string) (int, int64, string) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://localhost"+path, nil)
		r.RemoteAddr = address + ":40000"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		before := counter.n.Load()
		handler.ServeHTTP(w, r)
		return w.Code, counter.n.Load() - before, w.Body.String()
	}
	// Warm the sessions' last-use records and the guest's access records.
	for _, probe := range []string{"/api/v1/items?limit=1", "/api/v1/items/" + f.items["movie-g"], "/api/v1/items/" + f.items["movie-g"] + "/details"} {
		request(probe, token, lanAddress)
		request(probe, guest.Token, lanAddress)
	}
	for _, probe := range []string{"/api/v1/items?limit=50", "/api/v1/items?limit=50&q=movie&type=Movie", "/api/v1/items/" + f.items["movie-g"] + "/details", "/api/v1/items/" + f.items["movie-g"]} {
		var counts []int64
		for _, c := range []struct{ token, address string }{{token, lanAddress}, {token, wanAddress}, {guest.Token, lanAddress}} {
			status, n, body := request(probe, c.token, c.address)
			if status != 200 {
				t.Fatalf("%s from %s: status %d %s", probe, c.address, status, body)
			}
			if c.address == wanAddress && strings.Contains(body, f.items["movie-other"]) {
				t.Fatalf("%s: WAN response shows the LAN-only library", probe)
			}
			counts = append(counts, n)
		}
		if counts[0] != 2 || counts[1] != 2 || counts[2] != 2 {
			t.Errorf("%s: statements LAN %d, WAN %d, guest %d; want 2 each", probe, counts[0], counts[1], counts[2])
		}
		t.Logf("%s: %v SQL statements per request (LAN, WAN, guest)", probe, counts)
	}
}
