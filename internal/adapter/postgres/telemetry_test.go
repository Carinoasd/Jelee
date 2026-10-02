package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPoolStatsDoesNotAcquireOrConnect(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://localhost/jelee_test")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 8, 0, 0
	var connects atomic.Int64
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		connects.Add(1)
		return errors.New("unexpected connection")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := &Store{Pool: pool}
	for range 3 {
		if got := store.PoolStats(); got != (app.PoolStatsSnapshot{MaxConns: 8}) {
			t.Fatalf("empty pool snapshot = %+v", got)
		}
	}
	if connects.Load() != 0 {
		t.Fatal("reading pool statistics attempted a connection")
	}
}

type telemetryQueryCounter struct{ queries atomic.Int64 }

func (q *telemetryQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.queries.Add(1)
	return ctx
}

func (*telemetryQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPoolStatsRealAcquisitionAndCancellation(t *testing.T) {
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required telemetry integration database is unavailable")
		}
		t.Skip("pool telemetry PostgreSQL integration NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("pool telemetry integration requires dedicated jelee_test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test pool configuration")
	}
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 1, 0, 0
	trace := &telemetryQueryCounter{}
	cfg.ConnConfig.Tracer = trace
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("create telemetry test pool")
	}
	defer pool.Close()
	store := &Store{Pool: pool}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("acquire telemetry test connection")
	}
	defer conn.Release()
	held := store.PoolStats()
	if held.AcquiredConns != 1 || held.IdleConns != 0 || held.TotalConns != 1 || held.MaxConns != 1 || held.ConstructingConns != 0 || held.AcquireCount != 1 || held.AcquireDuration <= 0 || held.EmptyAcquireCount != 1 || held.EmptyAcquireWaitTime <= 0 {
		t.Fatalf("unexpected held pool snapshot: %+v", held)
	}
	waitCtx, stopWait := context.WithTimeout(ctx, 20*time.Millisecond)
	_, err = pool.Acquire(waitCtx)
	stopWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked acquisition should be canceled")
	}
	canceled := store.PoolStats()
	if canceled.CanceledAcquireCount != 1 || canceled.AcquireCount != held.AcquireCount || canceled.AcquireDuration != held.AcquireDuration || canceled.EmptyAcquireCount != held.EmptyAcquireCount || canceled.EmptyAcquireWaitTime != held.EmptyAcquireWaitTime {
		t.Fatalf("canceled acquisition changed successful acquisition counters: %+v", canceled)
	}
	conn.Release()
	released := store.PoolStats()
	if released.AcquiredConns != 0 || released.IdleConns != 1 || released.TotalConns != 1 {
		t.Fatalf("release was not reflected: %+v", released)
	}
	queries := trace.queries.Load()
	for range 3 {
		if got := store.PoolStats(); got != released {
			t.Fatalf("statistics read changed pool state: %+v", got)
		}
	}
	if trace.queries.Load() != queries {
		t.Fatal("statistics collection issued SQL")
	}
}
