package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// metadataCLIStore is the metadata backup of G36.4; docs/backup-restore.md
// describes the format and the restore procedure.
type metadataCLIStore interface {
	ExportMetadata(context.Context, io.Writer, domain.MetadataExportOptions) (domain.MetadataExportSummary, error)
	ImportMetadata(context.Context, io.Reader, domain.MetadataImportOptions) (domain.MetadataImportReport, error)
}

type metadataCLIDependencies struct {
	load  func() (config.Config, error)
	open  func(context.Context, config.Config) (metadataCLIStore, func(), error)
	stdin io.Reader
}

const metadataUsage = `usage: jelee-cli metadata export --out FILE|- [--include-password-hashes] [--timeout DURATION]
       jelee-cli metadata import --in FILE|- [--dry-run] [--skip-conflicts] [--timeout DURATION]`

func runMetadataCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runMetadataCLIWith(ctx, argv, stdout, stderr, metadataCLIDependencies{
		load:  config.Load,
		stdin: stdin,
		open: func(ctx context.Context, cfg config.Config) (metadataCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
	})
}

// runMetadataCLIWith implements "jelee-cli metadata export|import". Errors
// are stable codes on stderr; the connection string and driver detail never
// appear.
func runMetadataCLIWith(ctx context.Context, argv []string, stdout, stderr io.Writer, deps metadataCLIDependencies) int {
	usage := func() int {
		fmt.Fprintln(stderr, metadataUsage)
		return 2
	}
	if len(argv) == 0 || argv[0] != "export" && argv[0] != "import" {
		return usage()
	}
	flags := flag.NewFlagSet("metadata "+argv[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := ""
	if argv[0] == "export" {
		flags.StringVar(&path, "out", "", "output file, or - for standard output")
	} else {
		flags.StringVar(&path, "in", "", "input file, or - for standard input")
	}
	hashes := flags.Bool("include-password-hashes", false, "export: include account password hashes")
	dryRun := flags.Bool("dry-run", false, "import: run every check and roll back")
	skip := flags.Bool("skip-conflicts", false, "import: skip conflicting records instead of aborting")
	timeout := flags.Duration("timeout", 2*time.Hour, "overall time limit")
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 || path == "" || *timeout <= 0 {
		return usage()
	}
	if argv[0] == "export" && (*dryRun || *skip) || argv[0] == "import" && *hashes {
		return usage()
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	cfg, err := deps.load()
	if err != nil {
		fmt.Fprintln(stderr, "metadata_configuration_invalid")
		return 1
	}
	if argv[0] == "export" {
		return runMetadataExport(ctx, path, *hashes, stdout, stderr, cfg, deps)
	}
	return runMetadataImport(ctx, path, domain.MetadataImportOptions{DryRun: *dryRun, SkipConflicts: *skip}, stdout, stderr, cfg, deps)
}

func runMetadataExport(ctx context.Context, path string, hashes bool, stdout, stderr io.Writer, cfg config.Config, deps metadataCLIDependencies) int {
	var out *os.File
	var partial string
	if path != "-" {
		// Never replace an existing backup; write beside it and publish the
		// complete file under its name only after the export succeeded.
		if _, err := os.Lstat(path); err == nil {
			fmt.Fprintln(stderr, "metadata_output_exists")
			return 1
		} else if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "metadata_output_failed")
			return 1
		}
		var err error
		out, err = os.CreateTemp(filepath.Dir(path), ".jelee-metadata-*.partial")
		if err != nil {
			fmt.Fprintln(stderr, "metadata_output_failed")
			return 1
		}
		partial = out.Name()
		defer func() {
			if out != nil {
				_ = out.Close()
				_ = os.Remove(partial)
			}
		}()
		if err = out.Chmod(0o600); err != nil {
			fmt.Fprintln(stderr, "metadata_output_failed")
			return 1
		}
	}
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "metadata_database_unavailable")
		return 1
	}
	defer closeStore()
	var w io.Writer = stdout
	report := stdout
	if out != nil {
		w = out
	} else {
		// The document owns standard output; the summary goes to stderr.
		report = stderr
	}
	summary, err := store.ExportMetadata(ctx, w, domain.MetadataExportOptions{IncludePasswordHashes: hashes})
	if err != nil {
		fmt.Fprintln(stderr, metadataErrorCode(ctx, err, "metadata_export_failed"))
		return 1
	}
	if out != nil {
		if err = out.Sync(); err == nil {
			err = out.Close()
		}
		if err == nil {
			err = publishMetadataFile(partial, path)
		}
		if err != nil {
			fmt.Fprintln(stderr, "metadata_output_failed")
			return 1
		}
		out = nil
	}
	if err = json.NewEncoder(report).Encode(summary); err != nil {
		fmt.Fprintln(stderr, "metadata_output_failed")
		return 1
	}
	return 0
}

// publishMetadataFile gives the finished file its name without replacing
// a file that appeared meanwhile; a hard link refuses an existing name.
func publishMetadataFile(partial, path string) error {
	err := os.Link(partial, path)
	if err == nil {
		return os.Remove(partial)
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	// File systems without hard links: rename after a last check.
	if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
		return fs.ErrExist
	}
	return os.Rename(partial, path)
}

func runMetadataImport(ctx context.Context, path string, opts domain.MetadataImportOptions, stdout, stderr io.Writer, cfg config.Config, deps metadataCLIDependencies) int {
	in := deps.stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, "metadata_input_unavailable")
			return 1
		}
		defer file.Close()
		in = file
	}
	if in == nil {
		fmt.Fprintln(stderr, "metadata_input_unavailable")
		return 1
	}
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "metadata_database_unavailable")
		return 1
	}
	defer closeStore()
	report, err := store.ImportMetadata(ctx, in, opts)
	// The report is printed for refusals too: it names the conflicts.
	if encodeErr := json.NewEncoder(stdout).Encode(report); encodeErr != nil && err == nil {
		fmt.Fprintln(stderr, "metadata_output_failed")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, metadataErrorCode(ctx, err, "metadata_import_failed"))
		return 1
	}
	return 0
}

func metadataErrorCode(ctx context.Context, err error, fallback string) string {
	switch {
	case ctx.Err() != nil:
		return "metadata_cancelled"
	case errors.Is(err, domain.ErrMetadataBackupCorrupt):
		return "metadata_backup_corrupt"
	case errors.Is(err, domain.ErrMetadataBackupTruncated):
		return "metadata_backup_truncated"
	case errors.Is(err, domain.ErrMetadataBackupUnsupported):
		return "metadata_backup_unsupported"
	case errors.Is(err, domain.ErrMetadataBackupConflict):
		return "metadata_backup_conflict"
	}
	return fallback
}
