package httpapi

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
)

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"": "", "Film": "Film", "=1+1": "'=1+1", "+x": "'+x", "-x": "'-x", "@x": "'@x", "\tx": "'\tx", "a=b": "a=b"} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestWatchStatsHTTPPostgres drives the statistics routes through the real
// router and storage: web sessions read their own statistics without any
// delivery URL, hidden libraries are left out (G48.3), administrators read
// every user, and exports are bounded, audited and safe to open in a
// spreadsheet.
func TestWatchStatsHTTPPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	adminToken, err := store.Provision(ctx, "stats-admin", access.ClientNative, true)
	if err != nil {
		t.Fatal(err)
	}
	webToken, err := store.Provision(ctx, "stats-web", access.ClientWeb, false)
	if err != nil {
		t.Fatal(err)
	}
	viewer := compatInsert(t, ctx, store, `SELECT id::text FROM users WHERE name='stats-web'`)
	visible := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Stats Visible') RETURNING id::text`)
	hidden := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Stats Hidden') RETURNING id::text`)
	film := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'=HYPERLINK("x")','Movie') RETURNING id::text`, visible)
	secret := compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Stats Secret','Movie') RETURNING id::text`, hidden)
	compatInsert(t, ctx, store, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid) RETURNING user_id::text`, viewer, visible)
	root := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, visible, t.TempDir())
	compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mkv','video/x-matroska') RETURNING id::text`,
		film, visible, root)
	if _, err = store.Pool.Exec(ctx, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,completions,completion_milli)
 VALUES($1::uuid,'2026-03-10',$2::uuid,$3::uuid,600000,1,1,1,1,950),($1::uuid,'2026-03-11',$4::uuid,$5::uuid,300000,1,1,1,0,100)`, viewer, film, visible, secret, hidden); err != nil {
		t.Fatal(err)
	}
	handler := leakHandler(t, store, leakConfig(t, dsn, 0))
	get := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(http.MethodGet, path, "", "")
		r.Header.Del("Content-Type")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	type report struct {
		Data struct {
			UserID    string `json:"userId"`
			TimeZone  string `json:"timeZone"`
			WeekStart string `json:"weekStart"`
			Totals    struct {
				EffectiveSeconds int64   `json:"effectiveSeconds"`
				Sessions         int64   `json:"sessions"`
				CompletionRate   float64 `json:"completionRate"`
			} `json:"totals"`
			Periods []struct {
				Start string `json:"start"`
			} `json:"periods"`
			TopItems []struct {
				ItemID   string          `json:"itemId"`
				UserData json.RawMessage `json:"userData"`
			} `json:"topItems"`
			TopUsers []struct {
				UserID string `json:"userId"`
			} `json:"topUsers"`
		} `json:"data"`
	}
	decode := func(w *httptest.ResponseRecorder) report {
		t.Helper()
		var r report
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	noDelivery := func(w *httptest.ResponseRecorder) {
		t.Helper()
		for _, marker := range []string{"/stream", "/api/v1/sources", "/compat/", "film.mkv", "http"} {
			if strings.Contains(w.Body.String(), marker) {
				t.Fatalf("statistics carry %q: %s", marker, w.Body.String())
			}
		}
	}

	const span = "?from=2026-03-01&to=2026-03-31"
	w := get("/api/v1/users/me/watch-stats"+span, webToken)
	own := decode(w)
	noDelivery(w)
	if own.Data.UserID != viewer || own.Data.TimeZone != "UTC" || own.Data.WeekStart != "monday" || own.Data.Totals.EffectiveSeconds != 600 ||
		own.Data.Totals.CompletionRate != 0.95 || len(own.Data.TopItems) != 1 || own.Data.TopItems[0].ItemID != film || len(own.Data.TopItems[0].UserData) == 0 || own.Data.TopUsers != nil {
		t.Fatalf("own statistics %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), hidden) {
		t.Fatal("hidden library in own statistics")
	}
	if r := decode(get("/api/v1/users/me/watch-stats"+span+"&period=month", webToken)); len(r.Data.Periods) != 1 || r.Data.Periods[0].Start != "2026-03-01" {
		t.Fatalf("monthly periods %+v", r.Data.Periods)
	}
	for _, bad := range []string{"?period=hour", "?top=0", "?top=x", "?from=2026-3-1", "?from=2026-03-31&to=2026-03-01", "?unknown=1", "?from=2020-01-01&to=2026-03-01"} {
		if w := get("/api/v1/users/me/watch-stats"+bad, webToken); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/watch-stats", "/api/v1/users/" + viewer + "/watch-stats", "/api/v1/watch-stats/export"} {
		if w := get(path, webToken); w.Code != http.StatusForbidden {
			t.Errorf("viewer %s: %d", path, w.Code)
		}
	}

	// Administrators: one user as administrators see them (every library),
	// and every user.
	w = get("/api/v1/users/"+viewer+"/watch-stats"+span, adminToken)
	if r := decode(w); r.Data.UserID != viewer || r.Data.Totals.EffectiveSeconds != 900 || len(r.Data.TopItems) != 2 {
		t.Fatalf("administrator view of the user %s", w.Body.String())
	}
	if w := get("/api/v1/users/"+leakUUID(t)+"/watch-stats", adminToken); w.Code != http.StatusNotFound {
		t.Fatalf("unknown user %d", w.Code)
	}
	w = get("/api/v1/watch-stats"+span+"&period=week&top=5", adminToken)
	if r := decode(w); r.Data.UserID != "" || r.Data.Totals.Sessions != 2 || len(r.Data.TopUsers) != 1 || r.Data.TopUsers[0].UserID != viewer || len(r.Data.Periods) != 1 || r.Data.Periods[0].Start != "2026-03-09" {
		t.Fatalf("every user %s", w.Body.String())
	}
	noDelivery(w)

	// Export: CSV with a header, formula-safe text, the row count and a
	// completion trailer; audited.
	w = get("/api/v1/watch-stats/export"+span, adminToken)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") || w.Header().Get("X-Jelee-Export-Rows") != "2" ||
		w.Result().Trailer.Get("X-Jelee-Export-Complete") != "true" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv export %d %v %v", w.Code, w.Header(), w.Result().Trailer)
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(records) != 3 || records[0][0] != "day" || records[1][0] != "2026-03-10" || records[1][6] != `'=HYPERLINK("x")` || records[1][7] != "600" || records[1][13] != "0.950" {
		t.Fatalf("csv rows %q %v", records, err)
	}
	w = get("/api/v1/watch-stats/export"+span+"&format=ndjson&userId="+viewer, adminToken)
	lines := 0
	for scanner := bufio.NewScanner(strings.NewReader(w.Body.String())); scanner.Scan(); lines++ {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil || row["userId"] != viewer {
			t.Fatalf("ndjson line %q %v", scanner.Text(), err)
		}
	}
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" || lines != 2 {
		t.Fatalf("ndjson export %d %d", w.Code, lines)
	}
	if w := get("/api/v1/watch-stats/export"+span+"&limit=1", adminToken); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "stats_export_limit") {
		t.Fatalf("export over the limit %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"?format=xml", "?limit=0", "?limit=100001", "?userId=x", "?from=2000-01-01"} {
		if w := get("/api/v1/watch-stats/export"+bad, adminToken); w.Code != http.StatusBadRequest {
			t.Errorf("export %s: %d %s", bad, w.Code, w.Body.String())
		}
	}
	var audited int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE event='watch_stats.exported'`).Scan(&audited); err != nil || audited != 2 {
		t.Fatal("exports audited", audited, err)
	}
}
