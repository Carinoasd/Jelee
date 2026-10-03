package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/diag"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// diagEnvironment builds the production doctor inputs. Tests replace it to
// inject faults without touching the process environment.
var diagEnvironment = func(external bool, maxRoots int) diag.Environment {
	cfg, cfgErr := config.LoadWith(os.LookupEnv)
	project, _ := os.Getwd()
	env := diag.Environment{
		Lookup: os.LookupEnv, Config: cfg, ConfigErr: cfgErr, SchemaVersion: postgres.SchemaVersion,
		OpenDB: openDiagnosticsDB, Project: project, External: external, MaxRoots: maxRoots,
	}
	if external && cfg.TMDBAPIKey != "" {
		key := cfg.TMDBAPIKey
		env.TMDB = func(ctx context.Context) diag.ExternalOutcome { return probeTMDB(ctx, key) }
	}
	return env
}

func runDoctor(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	external := flags.Bool("external", false, "also check TMDB connectivity through the controlled outbound client")
	maxRoots := flags.Int("max-roots", diag.DefaultMaxRoots, "maximum library roots to check")
	only := flags.String("checks", "", "comma-separated subset of checks, for example config,database,migrations")
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli doctor [--json] [--external] [--max-roots N] [--checks a,b] | doctor probe | doctor tools")
		return 2
	}
	if err := flags.Parse(argv); err != nil || flags.NArg() != 0 || *maxRoots < 1 || *maxRoots > diag.MaxRootsLimit {
		return usage()
	}
	env := diagEnvironment(*external, *maxRoots)
	session := diag.NewSession(env)
	defer session.Close()
	var report diag.Report
	if *only == "" {
		report = session.Doctor(ctx)
	} else {
		var unknown []string
		if report, unknown = session.DoctorSelected(ctx, strings.Split(*only, ",")); len(unknown) > 0 {
			return usage()
		}
	}
	var out bytes.Buffer
	if *asJSON {
		data, err := diag.MarshalReport(report)
		if err != nil {
			fmt.Fprintln(stderr, diag.CodeOutputUnsafe)
			return 1
		}
		out.Write(data)
	} else if err := diag.RenderTable(&out, report); err != nil {
		fmt.Fprintln(stderr, diag.CodeOutputUnsafe)
		return 1
	}
	// The same final gate as diagnostic bundles: output that looks sensitive
	// is withheld instead of printed.
	if !diag.NewScanner(env.Config, env.Lookup).Clean(out.Bytes()) {
		fmt.Fprintln(stderr, diag.CodeOutputUnsafe)
		return 1
	}
	if _, err := stdout.Write(out.Bytes()); err != nil {
		return 1
	}
	if report.Status == diag.StatusFail {
		return 1
	}
	return 0
}

func runDiag(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli diag export --out FILE.zip [--since 24h] [--max-log-bytes N] [--external] [--max-roots N]")
		return 2
	}
	if len(argv) == 0 || argv[0] != "export" {
		return usage()
	}
	flags := flag.NewFlagSet("diag export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "bundle file to create (.zip, never overwritten)")
	since := flags.Duration("since", diag.DefaultSince, "log and job window")
	maxLog := flags.Int64("max-log-bytes", diag.DefaultMaxLogBytes, "maximum bytes of log records")
	external := flags.Bool("external", false, "include the TMDB connectivity check")
	maxRoots := flags.Int("max-roots", diag.DefaultMaxRoots, "maximum library roots to check")
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 || *out == "" || *since <= 0 || *since > diag.MaxSince ||
		*maxLog < 1 || *maxLog > diag.MaxLogBytesLimit || *maxRoots < 1 || *maxRoots > diag.MaxRootsLimit {
		return usage()
	}
	session := diag.NewSession(diagEnvironment(*external, *maxRoots))
	defer session.Close()
	if err := session.Export(ctx, diag.ExportOptions{Out: *out, Since: *since, MaxLogBytes: *maxLog}); err != nil {
		var exportErr *diag.ExportError
		if errors.As(err, &exportErr) {
			fmt.Fprintln(stderr, exportErr.Code)
		} else {
			fmt.Fprintln(stderr, diag.CodeExportFailed)
		}
		return 1
	}
	// The path was chosen by the operator, so echoing it back is not a leak.
	fmt.Fprintln(stdout, "Diagnostic bundle written (mode 0600). Review it before sharing.")
	return 0
}

// diagnosticsDB adapts the PostgreSQL diagnostics pool to diag.Database.
type diagnosticsDB struct{ d *postgres.Diagnostics }

func openDiagnosticsDB(ctx context.Context, dsn string) (diag.Database, error) {
	d, err := postgres.OpenDiagnostics(ctx, dsn)
	switch {
	case errors.Is(err, postgres.ErrDiagnosticsConfig):
		return nil, diag.ErrDBConfig
	case errors.Is(err, postgres.ErrDiagnosticsAuth):
		return nil, diag.ErrDBAuth
	case err != nil:
		return nil, err
	}
	return diagnosticsDB{d}, nil
}

func (db diagnosticsDB) Ping(ctx context.Context) error { return db.d.Ping(ctx) }
func (db diagnosticsDB) Close()                         { db.d.Close() }
func (db diagnosticsDB) Migration(ctx context.Context) (int64, bool, bool, error) {
	return db.d.Migration(ctx)
}

func (db diagnosticsDB) LibraryRoots(ctx context.Context, limit int) ([]diag.Root, error) {
	rows, err := db.d.LibraryRoots(ctx, limit)
	out := make([]diag.Root, len(rows))
	for i, r := range rows {
		out[i] = diag.Root{ID: r.ID, LibraryID: r.LibraryID, Path: r.Path}
	}
	return out, err
}

func (db diagnosticsDB) TableStats(ctx context.Context, limit int) ([]diag.TableStat, error) {
	rows, err := db.d.TableStats(ctx, limit)
	out := make([]diag.TableStat, len(rows))
	for i, r := range rows {
		out[i] = diag.TableStat{Name: r.Name, EstimatedRows: r.Rows, TotalBytes: r.TotalBytes}
	}
	return out, err
}

func (db diagnosticsDB) JobSummary(ctx context.Context, since time.Time, limit int) ([]diag.JobGroup, error) {
	rows, err := db.d.JobSummary(ctx, since, limit)
	out := make([]diag.JobGroup, len(rows))
	for i, r := range rows {
		out[i] = diag.JobGroup{Kind: r.Kind, State: r.State, ErrorCode: r.ErrorCode, Count: r.Count, Latest: r.Latest.UTC()}
	}
	return out, err
}

// probeTMDB uses the production TMDB adapter, so the request goes through the
// controlled outbound client (DNS pinning, public-address policy, no
// redirects).
func probeTMDB(ctx context.Context, key string) diag.ExternalOutcome {
	client, err := metadata.NewTMDB(key)
	if err != nil {
		return diag.ExternalCredentials
	}
	defer client.Close()
	switch err := client.ValidateCredentials(ctx); {
	case err == nil:
		return diag.ExternalOK
	case errors.Is(err, metadata.ErrCredentials):
		return diag.ExternalCredentials
	case errors.Is(err, metadata.ErrRateLimited):
		return diag.ExternalRateLimited
	}
	return diag.ExternalUnreachable
}
