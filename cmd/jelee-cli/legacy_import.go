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
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/legacydb"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// legacyCLIStore is the legacy database import of G04.6;
// docs/legacy-import.md describes the procedure.
type legacyCLIStore interface {
	LegacyImport(context.Context, domain.LegacySource, domain.LegacyImportOptions) (domain.LegacyImportReport, error)
}

type legacyCLIDependencies struct {
	load       func() (config.Config, error)
	open       func(context.Context, config.Config) (legacyCLIStore, func(), error)
	openSource func(ctx context.Context, path, workDir string) (domain.LegacySource, func(), error)
}

const legacyUsage = `usage: jelee-cli legacy-import --source FILE [--path-map FROM=TO]... [--preflight] [--merge-users] [--skip-conflicts]
       [--restart] [--batch-size N] [--max-batches N] [--work-dir DIR] [--report FILE] [--json] [--timeout DURATION]`

// legacyExitPaused is the exit status of a run stopped by --max-batches.
const legacyExitPaused = 3

type pathRules []domain.LegacyPathRule

func (p *pathRules) String() string { return fmt.Sprint(len(*p)) }
func (p *pathRules) Set(v string) error {
	rule, err := domain.ParseLegacyPathRule(v)
	if err != nil || !filepath.IsAbs(rule.To) {
		return domain.ErrInvalid
	}
	*p = append(*p, rule)
	return nil
}

func runLegacyImportCLI(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runLegacyImportCLIWith(ctx, argv, stdout, stderr, legacyCLIDependencies{
		load: config.Load,
		open: func(ctx context.Context, cfg config.Config) (legacyCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
		openSource: func(ctx context.Context, path, workDir string) (domain.LegacySource, func(), error) {
			src, err := legacydb.Open(ctx, path, workDir)
			if err != nil {
				return nil, nil, err
			}
			return src, func() { _ = src.Close() }, nil
		},
	})
}

// runLegacyImportCLIWith implements "jelee-cli legacy-import". Errors are
// stable codes on stderr; the connection string never appears.
func runLegacyImportCLIWith(ctx context.Context, argv []string, stdout, stderr io.Writer, deps legacyCLIDependencies) int {
	usage := func() int {
		_, _ = fmt.Fprintln(stderr, legacyUsage)
		return 2
	}
	flags := flag.NewFlagSet("legacy-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var rules pathRules
	source := flags.String("source", "", "upstream SQLite database (read through a private copy)")
	flags.Var(&rules, "path-map", "FROM=TO: rewrite source path prefix FROM to the absolute target prefix TO (repeatable)")
	preflight := flags.Bool("preflight", false, "check source, target and conflicts; write nothing")
	merge := flags.Bool("merge-users", false, "map accounts whose name exists in Jelee to that account instead of conflicting")
	skip := flags.Bool("skip-conflicts", false, "skip conflicting accounts, libraries and roots instead of refusing")
	restart := flags.Bool("restart", false, "abandon an unfinished run of another source or options and start over")
	batch := flags.Int("batch-size", domain.LegacyImportDefaultBatch, "rows per transaction")
	maxBatches := flags.Int("max-batches", 0, "stop after this many batches (0: no limit); run again to continue")
	workDir := flags.String("work-dir", "", "directory for the private copy of the source (default: system temporary directory)")
	reportPath := flags.String("report", "", "also write the JSON report to this new file")
	asJSON := flags.Bool("json", false, "print the JSON report instead of the summary")
	timeout := flags.Duration("timeout", 12*time.Hour, "overall time limit")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 0 || *source == "" || *timeout <= 0 ||
		*batch < 1 || *batch > domain.LegacyImportMaxBatch || *maxBatches < 0 || *preflight && (*restart || *maxBatches > 0) {
		return usage()
	}
	if *reportPath != "" {
		if _, err := os.Lstat(*reportPath); err == nil {
			_, _ = fmt.Fprintln(stderr, "legacy_report_exists")
			return 1
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	cfg, err := deps.load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "legacy_configuration_invalid")
		return 1
	}
	src, closeSource, err := deps.openSource(ctx, *source, *workDir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, legacyErrorCode(ctx, err))
		return 1
	}
	defer closeSource()
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "legacy_database_unavailable")
		return 1
	}
	defer closeStore()
	opts := domain.LegacyImportOptions{PathMap: rules, MergeUsers: *merge, SkipConflicts: *skip, PreflightOnly: *preflight, Restart: *restart,
		BatchSize: *batch, MaxBatches: *maxBatches, FileExists: func(p string) bool {
			_, statErr := os.Stat(p)
			return !errors.Is(statErr, fs.ErrNotExist)
		}}
	report, err := store.LegacyImport(ctx, src, opts)
	// The report is printed for refusals and failures too.
	if *asJSON {
		if encodeErr := json.NewEncoder(stdout).Encode(report); encodeErr != nil {
			_, _ = fmt.Fprintln(stderr, "legacy_output_failed")
			return 1
		}
	} else {
		writeLegacySummary(stdout, report)
	}
	if *reportPath != "" {
		if writeErr := writeLegacyReport(*reportPath, report); writeErr != nil {
			_, _ = fmt.Fprintln(stderr, "legacy_output_failed")
			return 1
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, legacyErrorCode(ctx, err))
		return 1
	}
	if report.State == domain.LegacyStatePaused {
		return legacyExitPaused
	}
	return 0
}

func legacyErrorCode(ctx context.Context, err error) string {
	switch {
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return "legacy_cancelled"
	case errors.Is(err, domain.ErrLegacySourceUnsupported):
		// The wrapped detail is a fixed reason code, never a path.
		return strings.ReplaceAll(err.Error(), ": ", ":")
	}
	for _, known := range []error{domain.ErrLegacySourceChanged, domain.ErrLegacyImportConflict, domain.ErrLegacyImportBusy,
		domain.ErrLegacyImportRunMismatch, domain.ErrLegacyImportSchema, domain.ErrLegacyImportUnbalanced} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "legacy_import_failed"
}

// writeLegacyReport writes the JSON report to a new private file.
func writeLegacyReport(path string, report domain.LegacyImportReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".jelee-legacy-*.partial")
	if err != nil {
		return err
	}
	partial := out.Name()
	defer func() { _ = os.Remove(partial) }()
	if err = out.Chmod(0o600); err == nil {
		_, err = out.Write(append(data, '\n'))
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return publishMetadataFile(partial, path)
}

func reasonList(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func writeLegacySummary(w io.Writer, r domain.LegacyImportReport) {
	tested := "tested"
	if !r.Source.Tested {
		tested = "newer than tested"
	}
	_, _ = fmt.Fprintf(w, "legacy import: %s", r.State)
	if r.RunID != "" {
		_, _ = fmt.Fprintf(w, " (run %s", r.RunID)
		if r.Resumed {
			_, _ = fmt.Fprint(w, ", resumed")
		}
		_, _ = fmt.Fprint(w, ")")
	}
	_, _ = fmt.Fprintf(w, "\nsource: %s sha256=%s size=%d migration=%s (%s)\n", r.Source.File, r.Source.SHA256, r.Source.Size, r.Source.LatestMigration, tested)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "category\tsource\tinserted\tupdated\tmatched\tunchanged\tskipped\tconflicts\tpending\t")
	for _, name := range domain.LegacyImportCategories() {
		c := r.Categories[name]
		if c == nil {
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n", name, c.Source, c.Inserted, c.Updated, c.Matched, c.Unchanged, sumReasons(c.Skipped), sumReasons(c.Conflicts), sumReasons(c.Pending))
	}
	_ = tw.Flush()
	for _, name := range domain.LegacyImportCategories() {
		c := r.Categories[name]
		if c == nil {
			continue
		}
		for _, part := range []struct {
			label   string
			reasons map[string]int64
		}{{"skipped", c.Skipped}, {"conflicts", c.Conflicts}, {"pending", c.Pending}} {
			if len(part.reasons) > 0 {
				_, _ = fmt.Fprintf(w, "  %s %s: %s\n", name, part.label, reasonList(part.reasons))
			}
		}
	}
	if r.PasswordResetRequired > 0 {
		_, _ = fmt.Fprintf(w, "accounts without a password: %d (set one with: jelee-cli account set-password --name NAME --password-stdin)\n", r.PasswordResetRequired)
	}
	if r.FavoritesDropped > 0 {
		_, _ = fmt.Fprintf(w, "favorites not imported: %d (Jelee has no favorites)\n", r.FavoritesDropped)
	}
	if c := r.Categories[domain.LegacyCatItems]; c != nil && sumReasons(c.Pending) > 0 {
		_, _ = fmt.Fprintln(w, "pending items: scan the libraries, then run the same import again")
	}
	if v := r.Verification; v != nil {
		_, _ = fmt.Fprintf(w, "verification: rows balanced=%t, target rows match=%t\n", v.RowsBalanced, v.TargetMatch)
	}
	if r.State == domain.LegacyStatePaused {
		_, _ = fmt.Fprintln(w, "paused: run the same command again to continue")
	}
}

func sumReasons(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}
