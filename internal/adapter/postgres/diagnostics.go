package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Diagnostics is a small read-only pool for jelee-cli doctor and diag export.
// Unlike Open it does not require a clean current schema, so it can report a
// missing, dirty, older or newer migration state. Errors carry fixed text only.
type Diagnostics struct{ pool *pgxpool.Pool }

var (
	ErrDiagnosticsConfig      = errors.New("invalid PostgreSQL configuration")
	ErrDiagnosticsUnavailable = errors.New("PostgreSQL is unavailable")
	ErrDiagnosticsQuery       = errors.New("diagnostic query failed")
	ErrDiagnosticsAuth        = errors.New("PostgreSQL rejected the credentials")
)

// OpenDiagnostics connects with at most two connections and a read-only,
// time-bounded session.
func OpenDiagnostics(ctx context.Context, dsn string) (*Diagnostics, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrDiagnosticsConfig
	}
	cfg.MaxConns, cfg.MinConns = 2, 0
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["application_name"] = "jelee-doctor"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, ErrDiagnosticsConfig
	}
	d := &Diagnostics{pool: pool}
	if err := d.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return d, nil
}

func (d *Diagnostics) Close() { d.pool.Close() }

func (d *Diagnostics) Ping(ctx context.Context) error {
	if err := d.pool.Ping(ctx); err != nil {
		if diagnosticsAuthFailure(err) {
			return ErrDiagnosticsAuth
		}
		return ErrDiagnosticsUnavailable
	}
	return nil
}

// Migration reports the golang-migrate state. present is false when the
// schema_migrations table does not exist or holds no row.
func (d *Diagnostics) Migration(ctx context.Context) (version int64, dirty, present bool, err error) {
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, false, false, ErrDiagnosticsQuery
	}
	if !exists {
		return 0, false, false, nil
	}
	err = d.pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, ErrDiagnosticsQuery
	}
	return version, dirty, true, nil
}

// DiagnosticRoot is one configured media root. Path is the absolute local
// path; callers must never write it to diagnostic output.
type DiagnosticRoot struct {
	ID, LibraryID, Path string
}

// LibraryRoots returns at most limit roots in a stable order. A missing
// library_roots table (unmigrated database) yields an empty list.
func (d *Diagnostics) LibraryRoots(ctx context.Context, limit int) ([]DiagnosticRoot, error) {
	if limit < 1 {
		return nil, nil
	}
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('library_roots') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, ErrDiagnosticsQuery
	}
	if !exists {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT id::text,library_id::text,path FROM library_roots ORDER BY library_id,id LIMIT $1`, limit)
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	roots, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DiagnosticRoot, error) {
		var r DiagnosticRoot
		return r, row.Scan(&r.ID, &r.LibraryID, &r.Path)
	})
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	return roots, nil
}

// DiagnosticTable carries catalog statistics only, never row content. Rows is
// the planner's live-tuple estimate, which avoids full scans of large tables.
type DiagnosticTable struct {
	Name       string
	Rows       int64
	TotalBytes int64
}

// TableStats lists the tables of the connection's current schema, largest
// first, bounded by limit.
func (d *Diagnostics) TableStats(ctx context.Context, limit int) ([]DiagnosticTable, error) {
	if limit < 1 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT s.relname::text,COALESCE(s.n_live_tup,0)::bigint,pg_total_relation_size(s.relid)::bigint
FROM pg_stat_user_tables s WHERE s.schemaname=current_schema()
ORDER BY 3 DESC,1 LIMIT $1`, limit)
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	tables, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DiagnosticTable, error) {
		var t DiagnosticTable
		return t, row.Scan(&t.Name, &t.Rows, &t.TotalBytes)
	})
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	return tables, nil
}

// DiagnosticJobGroup aggregates jobs created since a cutoff. Kind, state and
// error code are constrained enumerations; no identifiers or paths are read.
type DiagnosticJobGroup struct {
	Kind, State, ErrorCode string
	Count                  int64
	Latest                 time.Time
}

func (d *Diagnostics) JobSummary(ctx context.Context, since time.Time, limit int) ([]DiagnosticJobGroup, error) {
	if limit < 1 {
		return nil, nil
	}
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('jobs') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, ErrDiagnosticsQuery
	}
	if !exists {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT kind,state,error_code,count(*),max(created_at) FROM jobs WHERE created_at>=$1
GROUP BY kind,state,error_code ORDER BY kind,state,error_code LIMIT $2`, since, limit)
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DiagnosticJobGroup, error) {
		var g DiagnosticJobGroup
		return g, row.Scan(&g.Kind, &g.State, &g.ErrorCode, &g.Count, &g.Latest)
	})
	if err != nil {
		return nil, ErrDiagnosticsQuery
	}
	return groups, nil
}

// diagnosticsAuthFailure reports whether a connection error is an
// authentication rejection, which needs a different fix than a network fault.
func diagnosticsAuthFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "28P01" || pgErr.Code == "28000")
}
