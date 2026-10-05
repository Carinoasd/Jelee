package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/legacydb"
	"github.com/MoYuanCN/Jelee/internal/adapter/legacydb/legacydbtest"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type legacyStoreStub struct {
	opts   domain.LegacyImportOptions
	info   domain.LegacySourceInfo
	report domain.LegacyImportReport
	err    error
}

func (s *legacyStoreStub) LegacyImport(_ context.Context, src domain.LegacySource, opts domain.LegacyImportOptions) (domain.LegacyImportReport, error) {
	s.opts, s.info = opts, src.Info()
	return s.report, s.err
}

type fakeLegacySource struct{}

func (fakeLegacySource) Info() domain.LegacySourceInfo { return domain.LegacySourceInfo{File: "x.db"} }
func (fakeLegacySource) Users(context.Context, string, int) ([]domain.LegacyUser, string, error) {
	return nil, "", nil
}
func (fakeLegacySource) Libraries(context.Context, string, int) ([]domain.LegacyLibrary, string, error) {
	return nil, "", nil
}
func (fakeLegacySource) Items(context.Context, string, int) ([]domain.LegacyItem, string, error) {
	return nil, "", nil
}
func (fakeLegacySource) UserData(context.Context, string, int) ([]domain.LegacyUserData, string, error) {
	return nil, "", nil
}

func legacyDeps(store legacyCLIStore, sourceErr error) legacyCLIDependencies {
	return legacyCLIDependencies{
		load: func() (config.Config, error) {
			return config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 2}, nil
		},
		open: func(context.Context, config.Config) (legacyCLIStore, func(), error) {
			if store == nil {
				return nil, nil, errors.New("dial postgres://private:secret@localhost/database")
			}
			return store, func() {}, nil
		},
		openSource: func(context.Context, string, string) (domain.LegacySource, func(), error) {
			if sourceErr != nil {
				return nil, nil, sourceErr
			}
			return fakeLegacySource{}, func() {}, nil
		},
	}
}

func completedLegacyReport() domain.LegacyImportReport {
	r := domain.LegacyImportReport{Schema: domain.LegacyImportReportSchema, State: domain.LegacyStateCompleted, RunID: "run", Resumed: true,
		Source:                domain.LegacySourceReport{File: "upstream.db", SHA256: "ab", LatestMigration: "m"},
		PasswordResetRequired: 2, FavoritesDropped: 1, Verification: &domain.LegacyVerification{RowsBalanced: true, TargetMatch: true}}
	users := r.Category(domain.LegacyCatUsers)
	users.Source, users.Inserted = 3, 2
	users.Conflict("name_taken", 1)
	items := r.Category(domain.LegacyCatItems)
	items.Source = 2
	items.Pend("not_scanned", 1)
	items.Skip("structural", 1)
	return r
}

// targetPath returns a slash path as an absolute path of the host: path-map
// targets name the Jelee host's library roots, so a Windows host needs a
// drive-letter target and refuses a POSIX one.
func targetPath(slash string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(slash)
	}
	return slash
}

func TestLegacyCLIRunsAndReports(t *testing.T) {
	store := &legacyStoreStub{report: completedLegacyReport()}
	report := filepath.Join(t.TempDir(), "report.json")
	var out, errs bytes.Buffer
	code := runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db", "--path-map", `D:\Media=` + targetPath("/srv/media"), "--path-map", "/a=" + targetPath("/b"), "--merge-users",
		"--skip-conflicts", "--restart", "--batch-size", "50", "--max-batches", "4", "--report", report}, &out, &errs, legacyDeps(store, nil))
	if code != 0 || errs.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs.String())
	}
	o := store.opts
	if len(o.PathMap) != 2 || o.PathMap[0].From != `D:\Media` || !o.MergeUsers || !o.SkipConflicts || !o.Restart || o.BatchSize != 50 || o.MaxBatches != 4 || o.PreflightOnly ||
		o.FileExists == nil || o.FileExists(report+"-absent") || !o.FileExists(t.TempDir()) {
		t.Fatalf("options %+v", o)
	}
	text := out.String()
	for _, want := range []string{"legacy import: completed (run run, resumed)", "users", "users conflicts: name_taken=1", "items pending: not_scanned=1",
		"accounts without a password: 2", "favorites not imported: 1", "pending items: scan the libraries", "rows balanced=true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary lacks %q:\n%s", want, text)
		}
	}
	data, err := os.ReadFile(report)
	info, statErr := os.Stat(report)
	var decoded domain.LegacyImportReport
	if err != nil || json.Unmarshal(data, &decoded) != nil || decoded.RunID != "run" || statErr != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("report file %v %v", err, statErr)
	}
	// An existing report file is never replaced.
	errs.Reset()
	if code = runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db", "--report", report}, &out, &errs, legacyDeps(store, nil)); code != 1 ||
		strings.TrimSpace(errs.String()) != "legacy_report_exists" {
		t.Fatalf("existing report: %d %q", code, errs.String())
	}
	// --json prints the report; a paused run exits 3.
	store.report.State = domain.LegacyStatePaused
	out.Reset()
	if code = runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db", "--json"}, &out, &errs, legacyDeps(store, nil)); code != legacyExitPaused ||
		json.Unmarshal(out.Bytes(), &decoded) != nil || decoded.State != domain.LegacyStatePaused {
		t.Fatalf("paused: %d %q", code, out.String())
	}
	out.Reset()
	writeLegacySummary(&out, store.report)
	if !strings.Contains(out.String(), "paused: run the same command again") {
		t.Fatal(out.String())
	}
}

func TestLegacyCLIErrorsAreCodes(t *testing.T) {
	for want, err := range map[string]error{
		"legacy_import_conflict":     domain.ErrLegacyImportConflict,
		"legacy_import_busy":         domain.ErrLegacyImportBusy,
		"legacy_import_run_mismatch": domain.ErrLegacyImportRunMismatch,
		"legacy_import_schema":       domain.ErrLegacyImportSchema,
		"legacy_import_unbalanced":   fmt.Errorf("%w: rows=false target=true", domain.ErrLegacyImportUnbalanced),
		"legacy_import_failed":       errors.New("postgres://private:secret@localhost failed"),
	} {
		store := &legacyStoreStub{report: completedLegacyReport(), err: err}
		var out, errs bytes.Buffer
		if code := runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db", "--preflight"}, &out, &errs, legacyDeps(store, nil)); code != 1 ||
			strings.TrimSpace(errs.String()) != want || !store.opts.PreflightOnly || strings.Contains(out.String()+errs.String(), "secret") {
			t.Fatalf("%s: code=%d stderr=%q", want, code, errs.String())
		}
		// The report is printed for refusals too.
		if !strings.Contains(out.String(), "legacy import:") {
			t.Fatalf("%s: no summary", want)
		}
	}
	for want, err := range map[string]error{
		"legacy_source_unsupported:library_db_before_10_11": fmt.Errorf("%w: library_db_before_10_11", domain.ErrLegacySourceUnsupported),
		"legacy_source_changed":                             domain.ErrLegacySourceChanged,
	} {
		var errs bytes.Buffer
		if code := runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db"}, &bytes.Buffer{}, &errs, legacyDeps(&legacyStoreStub{}, err)); code != 1 || strings.TrimSpace(errs.String()) != want {
			t.Fatalf("%s: code=%d stderr=%q", want, code, errs.String())
		}
	}
	var errs bytes.Buffer
	if code := runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db"}, &bytes.Buffer{}, &errs, legacyDeps(nil, nil)); code != 1 ||
		strings.TrimSpace(errs.String()) != "legacy_database_unavailable" {
		t.Fatalf("unavailable: %d %q", code, errs.String())
	}
	deps := legacyDeps(&legacyStoreStub{}, nil)
	deps.load = func() (config.Config, error) { return config.Config{}, errors.New("bad") }
	errs.Reset()
	if code := runLegacyImportCLIWith(context.Background(), []string{"--source", "x.db"}, &bytes.Buffer{}, &errs, deps); code != 1 || strings.TrimSpace(errs.String()) != "legacy_configuration_invalid" {
		t.Fatalf("config: %d %q", code, errs.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := legacyErrorCode(ctx, errors.New("x")); got != "legacy_cancelled" {
		t.Fatal(got)
	}
}

func TestLegacyCLIRejectsUsage(t *testing.T) {
	for _, argv := range [][]string{nil, {"--source"}, {"--path-map", "/a=/b"}, {"--source", "x", "extra"}, {"--source", "x", "--path-map", "/a=relative"},
		{"--source", "x", "--path-map", "noequals"}, {"--source", "x", "--batch-size", "0"}, {"--source", "x", "--batch-size", "10001"}, {"--source", "x", "--max-batches", "-1"},
		{"--source", "x", "--preflight", "--restart"}, {"--source", "x", "--preflight", "--max-batches", "1"}, {"--source", "x", "--timeout", "0s"}, {"--source", "x", "--unknown"}} {
		var errs bytes.Buffer
		if code := runLegacyImportCLIWith(context.Background(), argv, &bytes.Buffer{}, &errs, legacyDeps(&legacyStoreStub{}, nil)); code != 2 || !strings.Contains(errs.String(), "usage: jelee-cli legacy-import") {
			t.Fatalf("%v: code=%d", argv, code)
		}
	}
	var rules pathRules
	if rules.Set("/a="+targetPath("/b")) != nil || rules.String() != "1" {
		t.Fatal("path rule flag")
	}
	// A target that is not absolute on this host is refused.
	foreign := "/a=" + `C:\b`
	if runtime.GOOS == "windows" {
		foreign = "/a=/b"
	}
	if rules.Set(foreign) == nil || rules.String() != "1" {
		t.Fatal("foreign target accepted")
	}
}

// TestLegacyCLIReadsARealSource drives the production source opener on a
// generated database; the store is a stub.
func TestLegacyCLIReadsARealSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upstream.db")
	if err := legacydbtest.Build(context.Background(), path, legacydbtest.Spec{Users: []legacydbtest.User{{ID: "a1000000-0000-4000-8000-000000000001", Name: "a"}}}); err != nil {
		t.Fatal(err)
	}
	store := &legacyStoreStub{report: completedLegacyReport()}
	deps := legacyDeps(store, nil)
	var opened domain.LegacySource
	deps.openSource = func(ctx context.Context, path, workDir string) (domain.LegacySource, func(), error) {
		src, err := legacydb.Open(ctx, path, workDir)
		if err != nil {
			return nil, nil, err
		}
		opened = src
		return src, func() { _ = src.Close() }, nil
	}
	var errs bytes.Buffer
	if code := runLegacyImportCLIWith(context.Background(), []string{"--source", path, "--work-dir", t.TempDir()}, &bytes.Buffer{}, &errs, deps); code != 0 || opened == nil ||
		store.info.Counts[legacydb.CountUsers] != 1 || len(store.info.SHA256) != 64 {
		t.Fatalf("code=%d stderr=%q info=%+v", code, errs.String(), store.info)
	}
	errs.Reset()
	if code := runLegacyImportCLIWith(context.Background(), []string{"--source", path + ".absent"}, &bytes.Buffer{}, &errs, deps); code != 1 ||
		strings.TrimSpace(errs.String()) != "legacy_source_unsupported:source_unreadable" {
		t.Fatalf("absent: %d %q", code, errs.String())
	}
}
