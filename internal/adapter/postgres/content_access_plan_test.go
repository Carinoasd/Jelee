package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// contentPlanItems is the size of the synthetic library of the plan test:
// contentPlanMovies movies and contentPlanSeries series of one season with
// three episodes each.
const (
	contentPlanMovies = 12000
	contentPlanSeries = 1600
	contentPlanItems  = contentPlanMovies + 5*contentPlanSeries
	// contentPlanUsers other users hold grants, rules and blocked tags, so
	// the rule tables are not trivially small.
	contentPlanUsers = 400
)

// seedContentPlanLibrary adds the synthetic catalog to the granted library:
// every movie and series rated (G, PG-13, R, TV-MA in turn), every tenth
// tagged horror, episodes inheriting their series' rating, plus grants, 25
// item rules and 5 blocked tags for each of contentPlanUsers other users.
func seedContentPlanLibrary(t *testing.T, f contentAccessFixture) {
	t.Helper()
	lib := f.registration.Library.ID
	exec := func(query string, args ...any) {
		t.Helper()
		imageRepositoryExec(t, f.jobFixture, query, args...)
	}
	exec(`INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'plan '||n,'Movie' FROM generate_series(1,$2::int) n`, lib, contentPlanMovies)
	exec(`INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'plan series '||n,'Series' FROM generate_series(1,$2::int) n`, lib, contentPlanSeries)
	exec(`INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'plan season '||n,'Season' FROM generate_series(1,$2::int) n`, lib, contentPlanSeries)
	exec(`INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'plan episode '||n||' '||e,'Episode' FROM generate_series(1,$2::int) n CROSS JOIN generate_series(1,3) e`, lib, contentPlanSeries)
	exec(`INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind)
 SELECT c.id,c.library_id,'Season',p.id,'Series' FROM items c JOIN items p ON p.title='plan series '||split_part(c.title,' ',3) WHERE c.title LIKE 'plan season %'`)
	exec(`INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind)
 SELECT c.id,c.library_id,'Episode',p.id,'Season' FROM items c JOIN items p ON p.title='plan season '||split_part(c.title,' ',3) WHERE c.title LIKE 'plan episode %'`)
	// The number in the title decides the rating and the tag.
	number := `split_part(title,' ',CASE WHEN kind='Movie' THEN 2 ELSE 3 END)::int`
	exec(`INSERT INTO item_metadata_state(item_id,revision) SELECT id,2 FROM items WHERE title LIKE 'plan %' AND kind IN ('Movie','Series')`)
	exec(`INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at)
 SELECT id,'mpaa',(ARRAY['G','PG-13','R','TV-MA'])[1+(` + number + `%4)],'manual',now() FROM items WHERE title LIKE 'plan %' AND kind IN ('Movie','Series')`)
	exec(`INSERT INTO item_metadata_facts(item_id,field,value,source,updated_at)
 SELECT id,'tags','["Horror"]'::jsonb,'manual',now() FROM items WHERE title LIKE 'plan %' AND kind IN ('Movie','Series') AND ` + number + `%10=0`)
	exec(`INSERT INTO users(name) SELECT 'plan user '||n FROM generate_series(1,$1::int) n`, contentPlanUsers)
	exec(`INSERT INTO library_acl(user_id,library_id) SELECT id,$1::uuid FROM users WHERE name LIKE 'plan user %'`, lib)
	exec(`INSERT INTO user_item_access_rules(user_id,item_id,effect)
 SELECT u.id,i.id,'hide' FROM users u CROSS JOIN LATERAL (SELECT id FROM items WHERE library_id=$1::uuid ORDER BY md5(u.id::text||id::text) LIMIT 25) i WHERE u.name LIKE 'plan user %'`, lib)
	exec(`INSERT INTO user_blocked_tags(user_id,tag) SELECT u.id,'tag '||n FROM users u CROSS JOIN generate_series(1,5) n WHERE u.name LIKE 'plan user %'`)
	exec(`ANALYZE`)
}

// planNodes collects every plan node of an EXPLAIN (FORMAT JSON) document.
func planNodes(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		if _, ok := v["Node Type"]; ok {
			visit(v)
		}
		for _, child := range v {
			planNodes(child, visit)
		}
	case []any:
		for _, child := range v {
			planNodes(child, visit)
		}
	}
}

// contentRuleRelations are the relations the content rules read per item.
// None of them may be read by a sequential scan (G48.8). library_acl is
// checked separately: the planner may hash the caller's grants once per
// statement, which is a single pass independent of the catalog size.
var contentRuleRelations = []string{"items", "user_item_access_rules", "user_blocked_tags", "item_metadata_fields", "item_metadata_facts", "item_parent_links"}

// repeatedGrantScan reports a sequential scan of library_acl that runs more
// than once, i.e. per item instead of once per statement.
func repeatedGrantScan(node map[string]any) bool {
	loops, _ := node["Actual Loops"].(float64)
	return node["Relation Name"] == "library_acl" && node["Node Type"] == "Seq Scan" && loops > 1
}

func explainContentPlan(t *testing.T, f contentAccessFixture, query string, args ...any) (map[string]any, []byte) {
	t.Helper()
	var raw []byte
	if err := f.s.Pool.QueryRow(f.ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		t.Fatal("explain", err)
	}
	var plan []map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
		t.Fatal("EXPLAIN did not return one JSON plan")
	}
	return plan[0], raw
}

// TestContentAccessPlanPostgres is the G48.8 evidence: with 20,000 rated
// and tagged items, the rules of a restricted user are index probes and a
// bounded page visits a bounded number of items.
func TestContentAccessPlanPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	seedContentPlanLibrary(t, f)
	viewer := f.viewer.principal.UserID
	ceiling := 13
	if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{ParentalRatingMax: &ceiling, BlockedTags: []string{"horror"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"], domain.ItemAccessHide); err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `ANALYZE`)
	noSeqScan := func(stage string, plan map[string]any) {
		t.Helper()
		planNodes(plan, func(node map[string]any) {
			relation, _ := node["Relation Name"].(string)
			for _, guarded := range contentRuleRelations {
				if relation == guarded && node["Node Type"] == "Seq Scan" {
					t.Errorf("%s: sequential scan on %s", stage, relation)
				}
			}
			if repeatedGrantScan(node) {
				t.Errorf("%s: library_acl scanned per row", stage)
			}
		})
	}

	// A bounded listing page: the rules filter inside each granted
	// library's ordered page, so hidden items cost only their own probes.
	page, raw := explainContentPlan(t, f, listItemsSQL, viewer, "", 50)
	noSeqScan("list page", page)
	visited := countPlanItemRows(page)
	if visited < 50 || visited > 400 {
		t.Fatalf("restricted list page visited %.0f item rows of %d", visited, contentPlanItems)
	}
	items, err := f.s.ListItems(f.ctx, viewer, "", 50)
	if err != nil || len(items) != 50 {
		t.Fatalf("restricted page: %d %v", len(items), err)
	}
	t.Logf("restricted list page visited %.0f item rows of %d; plan: %s", visited, contentPlanItems, raw)

	// A whole-library count, the shape of a filtered browse total: every
	// item is probed once through the indexes, never by scanning a rule or
	// metadata table.
	var visible int
	count := `SELECT count(*) FROM users u JOIN items i ON i.library_id=$2::uuid WHERE u.id=$1::uuid AND ` + itemVisibleSQL("i.library_id", "i.id")
	if err = f.s.Pool.QueryRow(f.ctx, count, viewer, f.registration.Library.ID).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	// Plan items: G and PG-13 rated movies and series trees without the
	// tag survive.
	var want int
	for n := 1; n <= contentPlanMovies; n++ {
		if (n%4 == 0 || n%4 == 1) && n%10 != 0 {
			want++
		}
	}
	for n := 1; n <= contentPlanSeries; n++ {
		if (n%4 == 0 || n%4 == 1) && n%10 != 0 {
			want += 5
		}
	}
	if fixture := len(without(f.all(false), "movie-r", "movie-16", "series-ma", "season-ma", "episode-ma", "series-pg", "season-pg", "episode-pg", "episode-pg-tagged", "movie-tagged")); visible != want+fixture {
		t.Fatalf("visible %d, want %d plan items and %d fixture items", visible, want, fixture)
	}
	whole, raw := explainContentPlan(t, f, count, viewer, f.registration.Library.ID)
	planNodes(whole, func(node map[string]any) {
		relation, _ := node["Relation Name"].(string)
		// The whole-library count reads every item of the library, so
		// items itself may be scanned; the rules may not.
		for _, guarded := range contentRuleRelations[1:] {
			if relation == guarded && node["Node Type"] == "Seq Scan" {
				t.Errorf("library count: sequential scan on %s", relation)
			}
		}
		if repeatedGrantScan(node) {
			t.Errorf("library count: library_acl scanned per row")
		}
	})
	t.Logf("restricted library count (%d visible of %d): %s", visible, contentPlanItems, raw)

	// By ID: one item, index lookups only.
	var episode string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM items WHERE title='plan episode 4 2'`).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	detail, raw := explainContentPlan(t, f, `SELECT i.id FROM users u JOIN items i ON i.id=$2::uuid WHERE u.id=$1::uuid AND `+itemVisibleSQL("i.library_id", "i.id"), viewer, episode)
	noSeqScan("by id", detail)
	t.Logf("restricted by-ID lookup: %s", raw)
}

// statementCounter counts the statements a pool sends.
type statementCounter struct{ n atomic.Int64 }

func (c *statementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestContentAccessStatementCountPostgres asserts the statements of one
// list and one detail request (G48.8): content rules ride inside the same
// statements, so a restricted user costs exactly what an unrestricted one
// does.
func TestContentAccessStatementCountPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
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
	tokens := map[string]string{}
	for _, name := range []string{"count-restricted", "count-free"} {
		token, err := f.s.Provision(f.ctx, name, access.ClientNative, false)
		if err != nil {
			t.Fatal(err)
		}
		p, err := f.s.Authenticate(f.ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, p.UserID, f.registration.Library.ID)
		tokens[name] = token
		if name == "count-restricted" {
			ceiling := 13
			if _, err = f.s.SetContentAccess(f.ctx, f.a, p.UserID, domain.ContentAccess{ParentalRatingMax: &ceiling, BlockedTags: []string{"horror"}}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.SetItemAccessRule(f.ctx, f.a, p.UserID, f.items["series-pg"], domain.ItemAccessHide); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := func(path, token string) (int, int64, string) {
		t.Helper()
		// The first request of a session records its last use; warm it so
		// every measured request is a steady-state one.
		r := httptest.NewRequest("GET", "http://localhost"+path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		before := counter.n.Load()
		handler.ServeHTTP(w, r)
		return w.Code, counter.n.Load() - before, w.Body.String()
	}
	for _, token := range tokens {
		request("/api/v1/items?limit=1", token)
	}
	for _, probe := range []struct {
		path string
		max  int64
	}{
		{"/api/v1/items?limit=50", 2},
		{"/api/v1/items?limit=50&q=movie&type=Movie", 2},
		{"/api/v1/items/" + f.items["movie-g"] + "/details", 2},
		{"/api/v1/items/" + f.items["movie-g"], 2},
	} {
		restrictedStatus, restricted, body := request(probe.path, tokens["count-restricted"])
		freeStatus, free, _ := request(probe.path, tokens["count-free"])
		if restrictedStatus != 200 || freeStatus != 200 {
			t.Fatalf("%s: status %d/%d %s", probe.path, restrictedStatus, freeStatus, body)
		}
		if strings.Contains(body, f.items["movie-r"]) || strings.Contains(body, f.items["series-pg"]) {
			t.Fatalf("%s: restricted response shows hidden items", probe.path)
		}
		if restricted != free || restricted > probe.max {
			t.Errorf("%s: restricted request sent %d statements, unrestricted %d, limit %d", probe.path, restricted, free, probe.max)
		}
		t.Logf("%s: %d SQL statements per request (restricted and unrestricted)", probe.path, restricted)
	}
}
