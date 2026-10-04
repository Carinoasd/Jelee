package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/diag"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/tools"
)

type cliFakeDB struct {
	version int64
	dirty   bool
}

func (f *cliFakeDB) Ping(context.Context) error { return nil }
func (f *cliFakeDB) Migration(context.Context) (int64, bool, bool, error) {
	return f.version, f.dirty, true, nil
}
func (f *cliFakeDB) LibraryRoots(context.Context, int) ([]diag.Root, error) { return nil, nil }
func (f *cliFakeDB) TableStats(context.Context, int) ([]diag.TableStat, error) {
	return []diag.TableStat{{Name: "items", EstimatedRows: 1, TotalBytes: 8192}}, nil
}
func (f *cliFakeDB) JobSummary(context.Context, time.Time, int) ([]diag.JobGroup, error) {
	return nil, nil
}
func (f *cliFakeDB) Close() {}

const cliSecretDSN = "postgres://jelee:CliS3cretPass@db.cli-secret.example:5432/jelee"

// useDiagEnvironment replaces the production environment with a fake whose
// only possible failure is the migration state.
func useDiagEnvironment(t *testing.T, db *cliFakeDB) {
	t.Helper()
	old := diagEnvironment
	t.Cleanup(func() { diagEnvironment = old })
	temp := t.TempDir()
	diagEnvironment = func(external bool, maxRoots int) diag.Environment {
		return diag.Environment{
			Lookup:        func(string) (string, bool) { return "", false },
			Config:        config.Config{Listen: "127.0.0.1:8097", DatabaseURL: cliSecretDSN, Logging: config.DefaultLoggingConfig()},
			SchemaVersion: 60, TempDir: temp, Tools: []diag.ToolCandidate{}, External: external, MaxRoots: maxRoots,
			ToolSpec: func() (tools.FFprobeSpecification, error) {
				return tools.FFprobeSpecification{}, errors.New("tool_platform_unsupported")
			},
			MatroskaSpec: func(string) (tools.MatroskaToolSpecification, error) {
				return tools.MatroskaToolSpecification{}, errors.New("tool_platform_unsupported")
			},
			OpenDB: func(context.Context, string) (diag.Database, error) { return db, nil },
			Statfs: func(string) (diag.DiskUsage, error) {
				return diag.DiskUsage{TotalBytes: 1 << 40, FreeBytes: 1 << 39}, nil
			},
		}
	}
}

func TestDoctorJSONAndExitCode(t *testing.T) {
	db := &cliFakeDB{version: 60}
	useDiagEnvironment(t, db)
	var out, errOut bytes.Buffer
	if code := runDoctor(context.Background(), []string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("healthy doctor exit %d: %s %s", code, out.String(), errOut.String())
	}
	var report diag.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("doctor --json is not JSON: %v", err)
	}
	// The unsupported tool platform is a warning, which keeps exit 0.
	if report.Status != diag.StatusWarn || report.Summary.Fail != 0 || len(report.Results) != 11 {
		t.Fatalf("report = %+v", report)
	}
	db.dirty = true
	out.Reset()
	if code := runDoctor(context.Background(), []string{"--json"}, &out, &errOut); code != 1 {
		t.Fatalf("failing doctor exit %d", code)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != diag.StatusFail || report.Results[2].Code != diag.CodeMigrationDirty {
		t.Fatalf("report = %+v err=%v", report.Results[2], err)
	}
	out.Reset()
	if code := runDoctor(context.Background(), nil, &out, &errOut); code != 1 || !strings.Contains(out.String(), "db_migration_dirty") || !strings.Contains(out.String(), "Result: fail") {
		t.Fatalf("table exit %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), "CliS3cretPass") || strings.Contains(out.String(), "db.cli-secret.example") {
		t.Fatal("doctor printed the DSN")
	}
}

func TestDoctorExternalFlagAndUsage(t *testing.T) {
	useDiagEnvironment(t, &cliFakeDB{version: 60})
	var out, errOut bytes.Buffer
	if code := runDoctor(context.Background(), []string{"--json", "--external"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var report diag.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Results[len(report.Results)-1].Check != "external" {
		t.Fatalf("external check missing: %v", err)
	}
	out.Reset()
	if code := runDoctor(context.Background(), []string{"--json", "--checks", "migrations,config"}, &out, &errOut); code != 0 {
		t.Fatalf("subset exit %d", code)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Results) != 2 || report.Results[0].Check != "config" || report.Results[1].Check != "migrations" {
		t.Fatalf("subset report = %+v err=%v", report.Results, err)
	}
	for _, argv := range [][]string{{"--checks", "config,nope"}, {"extra"}, {"--max-roots", "0"}, {"--max-roots", "5000"}, {"--nope"}} {
		errOut.Reset()
		if code := runDoctor(context.Background(), argv, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
			t.Fatalf("%v: exit %d", argv, code)
		}
	}
}

func TestDiagExportCLI(t *testing.T) {
	useDiagEnvironment(t, &cliFakeDB{version: 60})
	out := filepath.Join(t.TempDir(), "diag.zip")
	var stdout, stderr bytes.Buffer
	if code := runDiag(context.Background(), []string{"export", "--out", out, "--since", "2h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("export exit %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), out) {
		t.Fatal("stdout repeats the absolute output path")
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	zr.Close()
	slices.Sort(names)
	if !slices.Equal(names, []string{"config.json", "db-stats.json", "doctor.json", "jobs.json", "manifest.json"}) {
		t.Fatalf("names = %v", names)
	}
	stderr.Reset()
	if code := runDiag(context.Background(), []string{"export", "--out", out}, &stdout, &stderr); code != 1 || strings.TrimSpace(stderr.String()) != diag.CodeExportExists {
		t.Fatalf("overwrite exit %d: %s", code, stderr.String())
	}
	for _, argv := range [][]string{nil, {"import"}, {"export"}, {"export", "--out", out, "--since", "0s"}, {"export", "--out", out, "--max-log-bytes", "99999999999"}} {
		if code := runDiag(context.Background(), argv, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: exit %d", argv, code)
		}
	}
}
