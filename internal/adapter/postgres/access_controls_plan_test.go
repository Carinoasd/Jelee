package postgres

import (
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// aliasRows counts the rows a plan reads from the relation alias, over all
// loops: the item rows a listing page visits are those of alias "it".
func aliasRows(value any, alias string) float64 {
	total := float64(0)
	planNodes(value, func(node map[string]any) {
		if node["Alias"] == alias && node["Relation Name"] == "items" {
			loops, _ := node["Actual Loops"].(float64)
			for _, name := range []string{"Actual Rows", "Rows Removed by Filter", "Rows Removed by Index Recheck"} {
				rows, _ := node[name].(float64)
				total += rows * loops
			}
		}
	})
	return total
}

// TestAccessControlsPlanPostgres is the G48.8 evidence for blocked keywords
// and restricted time windows (G48.4): with 20,000 items and 400 other
// users holding keywords and windows, both are index probes inside the one
// statement, a bounded page stays bounded, and the window is decided by the
// request time parameter.
func TestAccessControlsPlanPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	seedContentPlanLibrary(t, f)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_blocked_keywords(user_id,keyword) SELECT u.id,'word '||n FROM users u CROSS JOIN generate_series(1,3) n WHERE u.name LIKE 'plan user %'`)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_access_windows(user_id,position,weekdays,start_minute,end_minute,time_zone,rating_max)
 SELECT u.id,n,ARRAY[n]::smallint[],1200,420,'Asia/Taipei',7 FROM users u CROSS JOIN generate_series(0,1) n WHERE u.name LIKE 'plan user %'`)
	viewer := f.viewer.principal.UserID
	// Every series numbered 7, 17, 27… and movies titled "plan 7…" carry
	// the keyword "plan 7"; the window caps ratings at 13 on Fridays from
	// 21:00 in Taipei.
	if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: []string{"ｐｌａｎ ７"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetAccessWindows(f.ctx, f.a, viewer, []domain.AccessWindow{{Weekdays: []int{5}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei", RatingMax: ptr(13)}}); err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `ANALYZE`)
	inside := requestScopeArg(f.viewer.at(f.ctx, time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)))
	outside := requestScopeArg(f.viewer.at(f.ctx, time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC)))
	guarded := append([]string{"user_blocked_keywords", "user_access_windows"}, contentRuleRelations...)
	noSeqScan := func(stage string, plan map[string]any, items bool) {
		t.Helper()
		planNodes(plan, func(node map[string]any) {
			relation, _ := node["Relation Name"].(string)
			for _, g := range guarded {
				if relation == g && node["Node Type"] == "Seq Scan" && (items || g != "items" || node["Alias"] != "i") {
					t.Errorf("%s: sequential scan on %s (%v)", stage, relation, node["Alias"])
				}
			}
			if repeatedGrantScan(node) {
				t.Errorf("%s: library_acl scanned per row", stage)
			}
		})
	}
	for _, c := range []struct {
		name string
		rq   any
	}{{"inside the window", inside}, {"outside the window", outside}} {
		page, raw := explainContentPlan(t, f, listItemsSQL, viewer, "", 50, c.rq)
		noSeqScan("list page "+c.name, page, true)
		visited := aliasRows(page, "it")
		if visited < 50 || visited > 400 {
			t.Fatalf("list page %s visited %.0f item rows", c.name, visited)
		}
		t.Logf("list page %s visited %.0f item rows, %.2f ms; plan: %s", c.name, visited, page["Execution Time"], raw)
	}
	count := `SELECT count(*) FROM users u JOIN items i ON i.library_id=$2::uuid WHERE u.id=$1::uuid AND ` + itemVisibleSQL("$3", "i.library_id", "i.id")
	counts := map[string]int{}
	for name, rq := range map[string]any{"inside": inside, "outside": outside} {
		var n int
		if err := f.s.Pool.QueryRow(f.ctx, count, viewer, f.registration.Library.ID, rq).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[name] = n
		whole, _ := explainContentPlan(t, f, count, viewer, f.registration.Library.ID, rq)
		noSeqScan("library count "+name, whole, false)
		t.Logf("library count %s: %d visible, %.2f ms", name, n, whole["Execution Time"])
	}
	// Outside the window only the keyword hides: movies "plan 7", "plan 7x",
	// "plan 7xx", "plan 7xxxx"… and the series trees whose title holds
	// "plan 7" (series 7…, but also "plan series 7" does not contain it).
	want := func(window bool) int {
		visibleMovie := func(n int) bool {
			if window && !(n%4 == 0 || n%4 == 1) {
				return false
			}
			return !hasPrefixDigits(n, 7)
		}
		total := 0
		for n := 1; n <= contentPlanMovies; n++ {
			if visibleMovie(n) {
				total++
			}
		}
		for n := 1; n <= contentPlanSeries; n++ {
			if !window || n%4 == 0 || n%4 == 1 {
				total += 5
			}
		}
		return total
	}
	fixture := len(f.all(false))
	fixtureInside := len(without(f.all(false), "movie-r", "movie-16", "series-ma", "season-ma", "episode-ma"))
	if counts["outside"] != want(false)+fixture || counts["inside"] != want(true)+fixtureInside {
		t.Fatalf("visible inside %d (want %d), outside %d (want %d)", counts["inside"], want(true)+fixtureInside, counts["outside"], want(false)+fixture)
	}
	// A preview evaluates every item for each user: one user over the whole
	// catalog, timed for the documentation.
	start := time.Now()
	if _, err := f.s.ApplyAccessGrants(f.ctx, f.a, []domain.AccessGrantOperation{{Action: domain.AccessGrantRemove, UserIDs: []string{viewer}, LibraryIDs: []string{f.registration.Library.ID}}}, true); err != nil {
		t.Fatal(err)
	}
	t.Logf("bulk preview of one user over %d items: %s", contentPlanItems, time.Since(start).Round(time.Millisecond))
}

// hasPrefixDigits reports whether the decimal digits of n start with d.
func hasPrefixDigits(n, d int) bool {
	for n >= 10 {
		n /= 10
	}
	return n == d
}
