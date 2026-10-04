package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type metadataStoreStub struct {
	exportErr, importErr error
	exported             string
	imported             string
	opts                 domain.MetadataImportOptions
	hashes               bool
}

func (s *metadataStoreStub) ExportMetadata(_ context.Context, w io.Writer, opts domain.MetadataExportOptions) (domain.MetadataExportSummary, error) {
	s.hashes = opts.IncludePasswordHashes
	if s.exportErr != nil {
		_, _ = io.WriteString(w, "partial")
		return domain.MetadataExportSummary{}, s.exportErr
	}
	_, err := io.WriteString(w, s.exported)
	return domain.MetadataExportSummary{Records: 1, SHA256: "ab"}, err
}

func (s *metadataStoreStub) ImportMetadata(_ context.Context, r io.Reader, opts domain.MetadataImportOptions) (domain.MetadataImportReport, error) {
	data, _ := io.ReadAll(r)
	s.imported, s.opts = string(data), opts
	return domain.MetadataImportReport{SHA256: "cd", DryRun: opts.DryRun, ConflictTotals: map[string]int64{"user:user_name_taken": 1}}, s.importErr
}

func metadataDeps(store metadataCLIStore, stdin io.Reader) metadataCLIDependencies {
	return metadataCLIDependencies{
		load: func() (config.Config, error) {
			return config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 2}, nil
		},
		open: func(context.Context, config.Config) (metadataCLIStore, func(), error) {
			if store == nil {
				return nil, nil, errors.New("dial postgres://private:secret@localhost/database")
			}
			return store, func() {}, nil
		},
		stdin: stdin,
	}
}

func TestMetadataCLIExportWritesPrivateFileOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.jsonl")
	store := &metadataStoreStub{exported: "document\n"}
	var out, errs bytes.Buffer
	if code := runMetadataCLIWith(context.Background(), []string{"export", "--out", path, "--include-password-hashes"}, &out, &errs, metadataDeps(store, nil)); code != 0 || !store.hashes {
		t.Fatalf("code=%d hashes=%t stderr=%q", code, store.hashes, errs.String())
	}
	data, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || string(data) != "document\n" || statErr != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("file %q mode %v err %v", data, info.Mode(), err)
	}
	var summary domain.MetadataExportSummary
	if err = json.Unmarshal(out.Bytes(), &summary); err != nil || summary.SHA256 != "ab" {
		t.Fatalf("summary %q", out.String())
	}
	// An existing backup is never replaced, and a failed export leaves no file.
	errs.Reset()
	if code := runMetadataCLIWith(context.Background(), []string{"export", "--out", path}, &out, &errs, metadataDeps(store, nil)); code != 1 || strings.TrimSpace(errs.String()) != "metadata_output_exists" {
		t.Fatalf("overwrite: code=%d stderr=%q", code, errs.String())
	}
	failed := filepath.Join(dir, "failed.jsonl")
	errs.Reset()
	store.exportErr = domain.ErrDatabase
	if code := runMetadataCLIWith(context.Background(), []string{"export", "--out", failed}, &out, &errs, metadataDeps(store, nil)); code != 1 || strings.TrimSpace(errs.String()) != "metadata_export_failed" {
		t.Fatalf("failure: code=%d stderr=%q", code, errs.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("failed export left files: %v", entries)
	}
}

func TestMetadataCLIImportReportsAndHidesSecrets(t *testing.T) {
	store := &metadataStoreStub{importErr: domain.ErrMetadataBackupConflict}
	var out, errs bytes.Buffer
	code := runMetadataCLIWith(context.Background(), []string{"import", "--in", "-", "--dry-run", "--skip-conflicts"}, &out, &errs, metadataDeps(store, strings.NewReader("doc")))
	if code != 1 || strings.TrimSpace(errs.String()) != "metadata_backup_conflict" || store.imported != "doc" || !store.opts.DryRun || !store.opts.SkipConflicts {
		t.Fatalf("code=%d stderr=%q store=%+v", code, errs.String(), store)
	}
	var report domain.MetadataImportReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.ConflictTotals["user:user_name_taken"] != 1 {
		t.Fatalf("report %q", out.String())
	}
	for name, err := range map[string]error{"metadata_backup_corrupt": domain.ErrMetadataBackupCorrupt, "metadata_backup_truncated": domain.ErrMetadataBackupTruncated,
		"metadata_backup_unsupported": domain.ErrMetadataBackupUnsupported, "metadata_import_failed": domain.ErrDatabase} {
		store.importErr = err
		errs.Reset()
		if code := runMetadataCLIWith(context.Background(), []string{"import", "--in", "-"}, io.Discard, &errs, metadataDeps(store, strings.NewReader("x"))); code != 1 || strings.TrimSpace(errs.String()) != name {
			t.Fatalf("%v: code=%d stderr=%q", err, code, errs.String())
		}
	}
	errs.Reset()
	if code := runMetadataCLIWith(context.Background(), []string{"import", "--in", "-"}, io.Discard, &errs, metadataDeps(nil, strings.NewReader("x"))); code != 1 || strings.Contains(errs.String(), "secret") {
		t.Fatalf("unavailable database: code=%d stderr=%q", code, errs.String())
	}
}

func TestMetadataCLIRejectsUsage(t *testing.T) {
	for _, argv := range [][]string{nil, {"backup"}, {"export"}, {"import"}, {"export", "--in", "x"}, {"import", "--out", "x"},
		{"export", "--out", "x", "--dry-run"}, {"import", "--in", "x", "--include-password-hashes"}, {"import", "--in", "x", "extra"}, {"export", "--out", "x", "--timeout", "0s"}} {
		var errs bytes.Buffer
		if code := runMetadataCLIWith(context.Background(), argv, io.Discard, &errs, metadataDeps(&metadataStoreStub{}, nil)); code != 2 || !strings.Contains(errs.String(), "usage:") {
			t.Fatalf("%v: code=%d", argv, code)
		}
	}
}

// End to end through the CLI against two migrated schemas: export a file,
// import it elsewhere, import it again without changes.
func TestMetadataCLIRoundTripPostgres(t *testing.T) {
	ctx, sourceDSN := setupCLIDatabase(t)
	_, targetDSN := setupCLIDatabase(t)
	source, err := postgres.Open(ctx, sourceDSN, 2)
	if err != nil {
		t.Fatal("open source")
	}
	defer source.Pool.Close()
	for _, sql := range []string{
		`INSERT INTO users(id,name,is_admin) VALUES ('e0000000-0000-4000-8000-000000000001','admin',true)`,
		`INSERT INTO libraries(id,name) VALUES ('e0000000-0000-4000-8000-000000000002','Movies')`,
		`INSERT INTO library_roots(id,library_id,path) VALUES ('e0000000-0000-4000-8000-000000000003','e0000000-0000-4000-8000-000000000002','/media/movies')`,
		`INSERT INTO items(id,library_id,title,kind) VALUES ('e0000000-0000-4000-8000-000000000004','e0000000-0000-4000-8000-000000000002','Film','Movie')`,
		`INSERT INTO user_item_data(user_id,item_id,resume_ticks,updated_at) VALUES ('e0000000-0000-4000-8000-000000000001','e0000000-0000-4000-8000-000000000004',42,now())`,
	} {
		if _, err = source.Pool.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	deps := func(dsn string) metadataCLIDependencies {
		d := metadataDeps(nil, nil)
		d.load = setupCLIConfig(dsn)
		d.open = func(ctx context.Context, cfg config.Config) (metadataCLIStore, func(), error) {
			s, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return s, s.Pool.Close, nil
		}
		return d
	}
	path := filepath.Join(t.TempDir(), "metadata.jsonl")
	var out, errs bytes.Buffer
	if code := runMetadataCLIWith(ctx, []string{"export", "--out", path}, &out, &errs, deps(sourceDSN)); code != 0 {
		t.Fatalf("export: code=%d stderr=%q", code, errs.String())
	}
	for round, wantInserted := range []bool{true, false} {
		out.Reset()
		if code := runMetadataCLIWith(ctx, []string{"import", "--in", path}, &out, &errs, deps(targetDSN)); code != 0 {
			t.Fatalf("import %d: code=%d stderr=%q", round, code, errs.String())
		}
		var report domain.MetadataImportReport
		if err = json.Unmarshal(out.Bytes(), &report); err != nil || !report.Committed {
			t.Fatalf("report %q", out.String())
		}
		if got := report.Kinds["user_item_data"].Inserted == 1; got != wantInserted {
			t.Fatalf("import %d: progress inserted=%t", round, got)
		}
	}
	target, err := postgres.Open(ctx, targetDSN, 2)
	if err != nil {
		t.Fatal("open target")
	}
	defer target.Pool.Close()
	var ticks int64
	if err = target.Pool.QueryRow(ctx, `SELECT resume_ticks FROM user_item_data`).Scan(&ticks); err != nil || ticks != 42 {
		t.Fatalf("restored progress %d err %v", ticks, err)
	}
}
