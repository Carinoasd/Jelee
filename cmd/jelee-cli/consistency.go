package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/diag"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/consistency"
)

// consistencyCLIStore is what the consistency commands (G50.3) need from
// the database. docs/consistency.md describes the commands and the report.
type consistencyCLIStore interface {
	consistency.Repository
	ResolveLibrary(ctx context.Context, reference string) (string, error)
	LatestConsistencyReport(ctx context.Context, library string) ([]byte, error)
	RevertConsistencyRun(ctx context.Context, run string) (domain.ConsistencyRevertResult, error)
	EnqueueConsistencyCheck(ctx context.Context, library, priority string, policy domain.JobPolicy) (domain.Job, error)
}

type consistencyCLIDependencies struct {
	load    func() (config.Config, error)
	lookup  func(string) (string, bool)
	open    func(context.Context, config.Config) (consistencyCLIStore, func(), error)
	checker func(config.Config, consistencyCLIStore) (*app.ConsistencyChecker, error)
}

const consistencyUsage = `usage: jelee-cli consistency check [--library ID|NAME] [--json] [--fix] [--stat-budget N] [--watch-sample N] [--timeout DURATION]
       jelee-cli consistency report [--library ID|NAME] [--json]
       jelee-cli consistency revert --run RUN_ID [--json]
       jelee-cli consistency enqueue --library ID|NAME [--priority manual|background]`

// Exit codes of "consistency check": 0 no open findings, 3 open findings
// (the report is printed), 1 failure, 2 usage, 130 cancelled.
const consistencyFindingsExit = 3

func runConsistencyCLI(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runConsistencyCLIWith(ctx, argv, stdout, stderr, consistencyCLIDependencies{
		load:   config.Load,
		lookup: os.LookupEnv,
		open: func(ctx context.Context, cfg config.Config) (consistencyCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
		checker: func(cfg config.Config, store consistencyCLIStore) (*app.ConsistencyChecker, error) {
			return consistency.NewChecker(cfg, store)
		},
	})
}

func consistencyFailure(stderr io.Writer, err error, code string) int {
	switch {
	case errors.Is(err, context.Canceled):
		_, _ = fmt.Fprintln(stderr, "consistency_cancelled")
		return 130
	case errors.Is(err, context.DeadlineExceeded):
		_, _ = fmt.Fprintln(stderr, "consistency_timeout")
		return 1
	case errors.Is(err, domain.ErrNotFound):
		_, _ = fmt.Fprintln(stderr, "consistency_not_found")
		return 1
	case errors.Is(err, domain.ErrJobBusy):
		_, _ = fmt.Fprintln(stderr, "consistency_library_busy: the library has an active job")
		return 1
	case errors.Is(err, domain.ErrJobQueueFull):
		_, _ = fmt.Fprintln(stderr, "consistency_queue_full")
		return 1
	}
	_, _ = fmt.Fprintln(stderr, code)
	return 1
}

// runConsistencyCLIWith implements "jelee-cli consistency". Errors are
// stable codes on stderr; the connection string, root paths and driver
// detail never appear. Output passes the doctor's sensitive scan.
func runConsistencyCLIWith(ctx context.Context, argv []string, stdout, stderr io.Writer, deps consistencyCLIDependencies) int {
	usage := func() int {
		_, _ = fmt.Fprintln(stderr, consistencyUsage)
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	command := argv[0]
	flags := flag.NewFlagSet("consistency "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	library := flags.String("library", "", "library ID or name")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	var fix bool
	var statBudget, watchSample int
	var timeout time.Duration
	var run, priority string
	switch command {
	case "check":
		flags.BoolVar(&fix, "fix", false, "apply the safe, journaled repairs")
		flags.IntVar(&statBudget, "stat-budget", 0, "file probes for the whole run (default from configuration)")
		flags.IntVar(&watchSample, "watch-sample", 0, "statistics rows recounted per library (default from configuration)")
		flags.DurationVar(&timeout, "timeout", 30*time.Minute, "overall time limit")
	case "report":
	case "revert":
		flags.StringVar(&run, "run", "", "run ID")
	case "enqueue":
		flags.StringVar(&priority, "priority", domain.JobPriorityManual, "queue priority")
	default:
		return usage()
	}
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 {
		return usage()
	}
	switch {
	case command == "check" && (timeout <= 0 || statBudget < 0 || statBudget > domain.ConsistencyMaxStatBudget || watchSample < 0 || watchSample > domain.ConsistencyMaxWatchSample),
		command == "revert" && !domain.ValidID(run),
		command == "enqueue" && (*library == "" || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground),
		len(*library) > 128:
		return usage()
	}
	cfg, err := deps.load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "consistency_configuration_invalid")
		return 1
	}
	if command != "check" {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "consistency_database_unavailable: run jelee-cli doctor")
		return 1
	}
	defer closeStore()
	libraryID := ""
	if *library != "" {
		if libraryID, err = store.ResolveLibrary(ctx, *library); err != nil {
			return consistencyFailure(stderr, err, "consistency_library_unavailable")
		}
	}
	var out bytes.Buffer
	status := 0
	switch command {
	case "check":
		checker, err := deps.checker(cfg, store)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "consistency_unavailable")
			return 1
		}
		options := consistency.Template(cfg)
		options.Origin, options.Library, options.Fix = domain.ConsistencyOriginCLI, libraryID, fix
		if statBudget > 0 {
			options.StatBudget = statBudget
		}
		if watchSample > 0 {
			options.WatchSample = watchSample
		}
		report, runErr := checker.Run(ctx, options)
		if runErr != nil && report.RunID == "" {
			return consistencyFailure(stderr, runErr, "consistency_check_failed")
		}
		if err = writeConsistencyReport(&out, report, *asJSON); err != nil {
			_, _ = fmt.Fprintln(stderr, "consistency_output_failed")
			return 1
		}
		if runErr != nil {
			status = consistencyFailure(io.Discard, runErr, "")
		} else if report.Totals.Findings > report.Totals.Fixed {
			status = consistencyFindingsExit
		}
	case "report":
		document, err := store.LatestConsistencyReport(ctx, libraryID)
		if err != nil {
			return consistencyFailure(stderr, err, "consistency_report_unavailable")
		}
		if *asJSON {
			out.Write(document)
			out.WriteByte('\n')
		} else {
			var report domain.ConsistencyReport
			if json.Unmarshal(document, &report) != nil || writeConsistencyReport(&out, report, false) != nil {
				_, _ = fmt.Fprintln(stderr, "consistency_output_failed")
				return 1
			}
		}
	case "revert":
		result, err := store.RevertConsistencyRun(ctx, run)
		if err != nil {
			return consistencyFailure(stderr, err, "consistency_revert_failed")
		}
		if *asJSON {
			if err = json.NewEncoder(&out).Encode(result); err != nil {
				return 1
			}
		} else {
			_, _ = fmt.Fprintf(&out, "Run %s: %d repairs reverted, %d skipped (row changed since the repair or already reverted).\n", result.RunID, result.Reverted, result.Skipped)
		}
	case "enqueue":
		job, err := store.EnqueueConsistencyCheck(ctx, libraryID, priority, cfg.Jobs.Policy())
		if err != nil {
			return consistencyFailure(stderr, err, "consistency_enqueue_failed")
		}
		if *asJSON {
			if err = json.NewEncoder(&out).Encode(job); err != nil {
				return 1
			}
		} else {
			_, _ = fmt.Fprintf(&out, "Queued consistency check job %s; follow it with jelee-cli jobs get --id %s.\n", job.ID, job.ID)
		}
	}
	// The same final gate as doctor output: anything that looks like a
	// secret or a configured absolute path is withheld.
	scanner := diag.NewScanner(cfg, deps.lookup)
	if libraries, err := store.ConsistencyLibraries(ctx, libraryID); err == nil {
		for _, l := range libraries {
			for _, root := range l.Roots {
				scanner.AddPath(root.Path)
			}
		}
	}
	if !scanner.Clean(out.Bytes()) {
		_, _ = fmt.Fprintln(stderr, diag.CodeOutputUnsafe)
		return 1
	}
	if _, err := stdout.Write(out.Bytes()); err != nil {
		return 1
	}
	return status
}

// writeConsistencyReport renders the report document or a table of it.
func writeConsistencyReport(w io.Writer, report domain.ConsistencyReport, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	_, _ = fmt.Fprintf(w, "Consistency run %s (%s, %s): %s\n", report.RunID, report.Origin, report.Mode, report.State)
	table := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	section := func(title string, results []domain.ConsistencyCheckResult) {
		_, _ = fmt.Fprintln(table, title)
		_, _ = fmt.Fprintln(table, "  CHECK\tSTATUS\tEXAMINED\tFINDINGS\tFIXED\tNOTE")
		for _, r := range results {
			note := r.Reason
			for _, key := range []string{domain.ConsistencyInfoBaselineStale, domain.ConsistencyInfoUnconfirmed, domain.ConsistencyInfoPending, domain.ConsistencyInfoUnverified, domain.ConsistencyInfoSampled} {
				if n := r.Info[key]; n > 0 {
					note = strings.TrimSpace(fmt.Sprintf("%s %s=%d", note, key, n))
				}
			}
			_, _ = fmt.Fprintf(table, "  %s\t%s\t%d\t%d\t%d\t%s\n", r.Check, r.Status, r.Examined, r.Findings, r.Fixed, note)
			for i, f := range r.Samples {
				if i == 3 {
					_, _ = fmt.Fprintf(table, "    … %d more in --json\t\t\t\t\t\n", r.Findings-3)
					break
				}
				_, _ = fmt.Fprintf(table, "    - %s\t\t\t\t\t%s\n", f.Code, consistencyFindingLabel(f))
			}
		}
	}
	for _, l := range report.Libraries {
		title := fmt.Sprintf("Library %s (no baseline: run a scan)", l.LibraryID)
		if l.Baseline.Available {
			title = fmt.Sprintf("Library %s (baseline revision %d, %d entries)", l.LibraryID, l.Baseline.Revision, l.Baseline.Entries)
		}
		section(title, l.Checks)
	}
	section("Database", report.Global)
	if err := table.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "Findings: %d (fixable %d, fixed %d).\n", report.Totals.Findings, report.Totals.Fixable, report.Totals.Fixed)
	if err == nil && report.Mode == domain.ConsistencyModeFix && report.Totals.Fixed > 0 {
		_, err = fmt.Fprintf(w, "Undo the repairs with: jelee-cli consistency revert --run %s\n", report.RunID)
	}
	return err
}

func consistencyFindingLabel(f domain.ConsistencyFinding) string {
	var parts []string
	for _, kv := range [][2]string{{"item", f.ItemID}, {"source", f.SourceID}, {"user", f.UserID}, {"day", f.Day}, {"object", f.Object}, {"root", f.RootID}, {"path", f.Path}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(parts, " ")
}
