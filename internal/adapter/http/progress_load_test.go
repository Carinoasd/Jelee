package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/jackc/pgx/v5"
)

// progressTableWrites reads the row write counters PostgreSQL keeps for the
// tables of this schema.
func progressTableWrites(t *testing.T, ctx context.Context, dsn string) map[string][3]int64 {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect for statistics")
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `SELECT relname,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables
 WHERE schemaname=current_schema() AND relname IN ('playback_sessions','playback_samples','user_item_data','sessions')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][3]int64{}
	for rows.Next() {
		var name string
		var v [3]int64
		if err := rows.Scan(&name, &v[0], &v[1], &v[2]); err != nil {
			t.Fatal(err)
		}
		out[name] = v
	}
	return out
}

// TestProgressWriteAmplificationPostgres is the G23.2 evidence: 100 native
// sessions report progress concurrently every 100 ms through the real router
// for JELEE_PROGRESS_LOAD_SECONDS (default 10) while the buffer flushes every
// second. It records the requests served, the achieved request rate, the
// write statements the buffer issued and the row writes PostgreSQL counted,
// and fails when writes grow with the report rate instead of the flush
// interval. docs/playback-progress.md records a run.
func TestProgressWriteAmplificationPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	seconds := 10
	if raw := os.Getenv("JELEE_PROGRESS_LOAD_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 2 || n > 600 {
			t.Fatal("JELEE_PROGRESS_LOAD_SECONDS must be 2..600")
		}
		seconds = n
	}
	const sessions, interval, flush = 100, 100 * time.Millisecond, time.Second
	root := t.TempDir()
	library := compatInsert(t, ctx, store, `INSERT INTO libraries(name) VALUES('Load') RETURNING id::text`)
	rootID := compatInsert(t, ctx, store, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, library, root)
	items := make([]string, 10)
	for i := range items {
		items[i] = compatInsert(t, ctx, store, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,'Movie') RETURNING id::text`, library, fmt.Sprintf("Load %d", i))
		compatInsert(t, ctx, store, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`,
			items[i], library, rootID, fmt.Sprintf("load-%d.mkv", i))
	}
	tokens := make([]string, sessions)
	for i := range tokens {
		var err error
		if tokens[i], err = store.Provision(ctx, fmt.Sprintf("load-%03d", i), access.ClientNative, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT id,$1::uuid FROM users`, library); err != nil {
		t.Fatal(err)
	}
	progress, err := app.NewProgress(store, store, app.ProgressOptions{FlushInterval: flush, SessionTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cfg := leakConfig(t, dsn, 0)
	handler := leakHandlerWithProgress(t, store, cfg, &httpAccountPasswords{}, progress)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); progress.Run(runCtx) }()

	// Backends report table statistics when they go idle or exit: replace
	// the pool so the fixture's writes are counted before the baseline.
	store.Pool.Close()
	fresh, err := postgres.Open(ctx, dsn, 16)
	if err != nil {
		t.Fatal(err)
	}
	store.Pool = fresh.Pool
	time.Sleep(500 * time.Millisecond)
	before := progressTableWrites(t, ctx, dsn)
	var served, failed atomic.Int64
	latencies := make([][]time.Duration, sessions)
	post := func(i int, path, body string) {
		r := accountRequest(http.MethodPost, path, body, "")
		r.Header.Set("Authorization", "Bearer "+tokens[i])
		w := httptest.NewRecorder()
		began := time.Now()
		handler.ServeHTTP(w, r)
		latencies[i] = append(latencies[i], time.Since(began))
		served.Add(1)
		if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
			failed.Add(1)
		}
	}
	started := time.Now()
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item := items[i%len(items)]
			key := fmt.Sprintf("load-%03d", i)
			post(i, "/api/v1/playback/start", `{"itemId":"`+item+`","playSessionId":"`+key+`"}`)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			position := int64(0)
			for deadline := started.Add(time.Duration(seconds) * time.Second); time.Now().Before(deadline); <-ticker.C {
				position += int64(interval / 100)
				post(i, "/api/v1/playback/progress", fmt.Sprintf(`{"playSessionId":%q,"positionTicks":%d}`, key, position))
			}
			post(i, "/api/v1/playback/stop", fmt.Sprintf(`{"playSessionId":%q,"itemId":%q,"positionTicks":%d}`, key, item, position))
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	stop()
	<-done
	if _, err := progress.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// Backends report table statistics when they go idle or exit; closing
	// the pool makes every counter final before it is read.
	store.Pool.Close()
	time.Sleep(500 * time.Millisecond)
	after := progressTableWrites(t, ctx, dsn)
	var all []time.Duration
	for _, l := range latencies {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	stats := progress.Stats()
	rowWrites := func(table string) int64 {
		var n int64
		for k := range 3 {
			n += after[table][k] - before[table][k]
		}
		return n
	}
	progressRows := rowWrites("playback_sessions") + rowWrites("playback_samples") + rowWrites("user_item_data")
	events := served.Load()
	t.Logf("load: %d sessions, report every %s, flush every %s, %s wall", sessions, interval, flush, elapsed.Round(time.Millisecond))
	t.Logf("requests served %d (failed %d), %.0f req/s, latency p50 %s p99 %s max %s", events, failed.Load(), float64(events)/elapsed.Seconds(),
		all[len(all)/2].Round(time.Microsecond), all[len(all)*99/100].Round(time.Microsecond), all[len(all)-1].Round(time.Microsecond))
	t.Logf("buffer: reports %d, coalesced %d, flushes %d, flush statements %d, start writes %d, stop writes %d",
		stats.Reports, stats.Coalesced, stats.Flushes, stats.FlushStatements, stats.StartWrites, stats.StopWrites)
	t.Logf("write statements %d (periodic flushes %d, stops %d, starts %d) for %d reports (%.4f per report)", stats.FlushStatements+stats.StartWrites,
		stats.FlushStatements-stats.StopWrites, stats.StopWrites, stats.StartWrites, events, float64(stats.FlushStatements+stats.StartWrites)/float64(events))
	for _, table := range []string{"playback_sessions", "playback_samples", "user_item_data", "sessions"} {
		t.Logf("rows %s: +%d inserted, +%d updated, +%d deleted", table, after[table][0]-before[table][0], after[table][1]-before[table][1], after[table][2]-before[table][2])
	}
	t.Logf("progress table row writes %d for %d reports (%.3f per report)", progressRows, events, float64(progressRows)/float64(events))
	if failed.Load() != 0 {
		t.Fatalf("%d requests failed", failed.Load())
	}
	// Statements: one per flush (100 sessions fit one batch) plus one per
	// start and stop; never one per progress report.
	maxFlushes := int64(elapsed/flush) + 3
	if stats.FlushStatements-stats.StopWrites > maxFlushes || stats.StartWrites != sessions || stats.StopWrites != sessions {
		t.Fatalf("write statements grew with reports: %+v (max flushes %d)", stats, maxFlushes)
	}
	// Rows: each session row is updated at most once per flush, so updates
	// stay near sessions × flushes while reports are ten times that.
	if updates := after["playback_sessions"][1] - before["playback_sessions"][1]; updates > sessions*(maxFlushes+1) || progressRows*3 > events {
		t.Fatalf("row writes %d (session updates %d) for %d reports", progressRows, updates, events)
	}
}
