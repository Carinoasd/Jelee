package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
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

// repairCLIStore is what "jelee-cli repair" needs from the database (G50.4).
// docs/repair.md describes the actions and the result document.
type repairCLIStore interface {
	app.RepairRepository
	ResolveLibrary(ctx context.Context, reference string) (string, error)
}

type repairCLIDependencies struct {
	load     func() (config.Config, error)
	lookup   func(string) (string, bool)
	open     func(context.Context, config.Config) (repairCLIStore, func(), error)
	repairer func(config.Config, repairCLIStore) (*app.Repairer, error)
	// client sends API requests of --token-stdin; nil uses a bounded client.
	client *http.Client
}

const repairUsage = `usage: jelee-cli repair items|image-variants|caches|stats|orphans|nfo|counts [--library ID|NAME] [--dry-run | --yes] [--json] [--stat-budget N] [--timeout DURATION] [--token-stdin [--url http://127.0.0.1:8097]]
       jelee-cli repair revert --run RUN_ID --yes [--json] [--token-stdin [--url http://127.0.0.1:8097]]`

// Exit codes: 0 done (or a dry run with nothing to do), 3 a dry run that
// found work, 1 failure, 2 usage, 130 cancelled.
const repairPlannedExit = 3

func runRepairCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runRepairCLIWith(ctx, argv, stdin, stdout, stderr, repairCLIDependencies{
		load:   config.Load,
		lookup: os.LookupEnv,
		open: func(ctx context.Context, cfg config.Config) (repairCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
		repairer: func(cfg config.Config, store repairCLIStore) (*app.Repairer, error) {
			return consistency.NewRepairer(cfg, store)
		},
	})
}

func repairFailure(stderr io.Writer, err error, code string) int {
	switch {
	case errors.Is(err, context.Canceled):
		_, _ = fmt.Fprintln(stderr, "repair_cancelled")
		return 130
	case errors.Is(err, context.DeadlineExceeded):
		_, _ = fmt.Fprintln(stderr, "repair_timeout")
	case errors.Is(err, domain.ErrRepairUnavailable):
		_, _ = fmt.Fprintln(stderr, "repair_requires_server: this action changes state the running server owns; run it with --token-stdin through POST /api/v1/admin/repairs")
	case errors.Is(err, domain.ErrNotFound):
		_, _ = fmt.Fprintln(stderr, "repair_not_found")
	case errors.Is(err, domain.ErrJobBusy):
		_, _ = fmt.Fprintln(stderr, "repair_library_busy: the library has another active job")
	case errors.Is(err, domain.ErrJobQueueFull):
		_, _ = fmt.Fprintln(stderr, "repair_queue_full")
	case errors.Is(err, domain.ErrConflict):
		_, _ = fmt.Fprintln(stderr, "repair_not_revertible: only finished stats and counts runs can be reverted")
	default:
		_, _ = fmt.Fprintln(stderr, code)
	}
	return 1
}

// runRepairCLIWith implements "jelee-cli repair". Errors are stable codes on
// stderr; the connection string, root paths and driver detail never appear.
// Output passes the doctor's sensitive scan.
func runRepairCLIWith(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer, deps repairCLIDependencies) int {
	usage := func() int {
		_, _ = fmt.Fprintln(stderr, repairUsage)
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	command := argv[0]
	if command != "revert" && !domain.ValidRepairAction(command) {
		return usage()
	}
	flags := flag.NewFlagSet("repair "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	yes := flags.Bool("yes", false, "apply the plan")
	fromStdin := flags.Bool("token-stdin", false, "run through the server API with an administrator token read from stdin")
	base := flags.String("url", "http://127.0.0.1:8097", "service origin for --token-stdin")
	var library, run string
	var dryRun bool
	var statBudget int
	var timeout time.Duration
	if command == "revert" {
		flags.StringVar(&run, "run", "", "run ID")
	} else {
		flags.StringVar(&library, "library", "", "library ID or name")
		flags.BoolVar(&dryRun, "dry-run", false, "list the affected objects without changing anything")
		flags.IntVar(&statBudget, "stat-budget", 0, "file probes of the orphans action (default from configuration)")
		flags.DurationVar(&timeout, "timeout", 30*time.Minute, "overall time limit")
	}
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 {
		return usage()
	}
	switch {
	case command == "revert" && !domain.ValidID(run),
		dryRun && *yes,
		len(library) > 128,
		statBudget < 0 || statBudget > domain.ConsistencyMaxStatBudget,
		command != "revert" && timeout <= 0:
		return usage()
	}
	if !dryRun && !*yes {
		_, _ = fmt.Fprintln(stderr, "repair_confirmation_required: preview with --dry-run, then apply with --yes")
		return 2
	}
	if command == "revert" {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if *fromStdin {
		return repairThroughAPI(ctx, command, library, run, dryRun, statBudget, *asJSON, *base, stdin, stdout, stderr, deps.client)
	}
	cfg, err := deps.load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_configuration_invalid")
		return 1
	}
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_database_unavailable: run jelee-cli doctor")
		return 1
	}
	defer closeStore()
	repairer, err := deps.repairer(cfg, store)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_unavailable")
		return 1
	}
	libraryID := ""
	if library != "" {
		if libraryID, err = store.ResolveLibrary(ctx, library); err != nil {
			return repairFailure(stderr, err, "repair_library_unavailable")
		}
	}
	var out bytes.Buffer
	status := 0
	if command == "revert" {
		result, err := repairer.Revert(ctx, domain.Actor{}, run)
		if err != nil {
			return repairFailure(stderr, err, "repair_revert_failed")
		}
		if err = writeRepairRevert(&out, result, *asJSON); err != nil {
			return 1
		}
	} else {
		options := consistency.RepairTemplate(cfg)
		options.Action, options.Library, options.DryRun, options.Origin = command, libraryID, dryRun, domain.RepairOriginCLI
		if statBudget > 0 {
			options.StatBudget = statBudget
		}
		result, runErr := repairer.Run(ctx, options)
		if runErr != nil && result.Schema == "" {
			return repairFailure(stderr, runErr, "repair_failed")
		}
		if err = writeRepairResult(&out, result, *asJSON); err != nil {
			_, _ = fmt.Fprintln(stderr, "repair_output_failed")
			return 1
		}
		switch {
		case runErr != nil:
			status = repairFailure(stderr, runErr, "repair_failed")
		case dryRun && result.Planned > 0:
			status = repairPlannedExit
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

// repairThroughAPI sends the same request to the running server, which owns
// the image variant store and the scan pipeline. The library must be an ID.
func repairThroughAPI(ctx context.Context, command, library, run string, dryRun bool, statBudget int, asJSON bool, base string, stdin io.Reader, stdout, stderr io.Writer, client *http.Client) int {
	if library != "" && !domain.ValidID(library) {
		_, _ = fmt.Fprintln(stderr, "repair_library_invalid: the API takes a library ID")
		return 2
	}
	u, err := jobsBaseURL(base)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_url_invalid: use https, or http to a loopback address")
		return 2
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_token_invalid")
		return 2
	}
	var body []byte
	if command == "revert" {
		u.Path = "/api/v1/admin/repairs/" + run + "/revert"
		body, _ = json.Marshal(map[string]bool{"iUnderstand": true})
	} else {
		u.Path = "/api/v1/admin/repairs"
		body, _ = json.Marshal(map[string]any{"action": command, "libraryId": library, "dryRun": dryRun, "iUnderstand": !dryRun, "statBudget": statBudget})
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body)) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "repair_request_failed")
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if client == nil {
		transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	}
	response, err := client.Do(request) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		if ctx.Err() != nil {
			return repairFailure(stderr, ctx.Err(), "")
		}
		_, _ = fmt.Fprintln(stderr, "repair_service_unavailable")
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		_, _ = fmt.Fprintln(stderr, "repair_response_invalid")
		return 1
	}
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		code := "unknown"
		if json.Unmarshal(data, &problem) == nil && repairProblemCode(problem.Error.Code) {
			code = problem.Error.Code
		}
		_, _ = fmt.Fprintf(stderr, "repair_request_rejected (HTTP %d %s)\n", response.StatusCode, code)
		return 1
	}
	var out bytes.Buffer
	status := 0
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if command == "revert" {
		var envelope struct{ Data domain.RepairRevertResult }
		if decoder.Decode(&envelope) != nil || envelope.Data.RunID != run || writeRepairRevert(&out, envelope.Data, asJSON) != nil {
			_, _ = fmt.Fprintln(stderr, "repair_response_invalid")
			return 1
		}
	} else {
		var envelope struct{ Data domain.RepairResult }
		if decoder.Decode(&envelope) != nil || envelope.Data.Schema != domain.RepairResultSchema || envelope.Data.Action != command || writeRepairResult(&out, envelope.Data, asJSON) != nil {
			_, _ = fmt.Fprintln(stderr, "repair_response_invalid")
			return 1
		}
		if dryRun && envelope.Data.Planned > 0 {
			status = repairPlannedExit
		}
	}
	if _, err = stdout.Write(out.Bytes()); err != nil {
		return 1
	}
	return status
}

// repairProblemCode accepts an error code shape only, so a hostile response
// cannot print arbitrary text.
func repairProblemCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

// writeRepairResult renders the result document or a table of it.
func writeRepairResult(w io.Writer, result domain.RepairResult, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	mode := "executed"
	if result.DryRun {
		mode = "dry run"
	}
	_, _ = fmt.Fprintf(w, "Repair %s (%s): %s; planned %d, applied %d, skipped %d.\n", result.Action, mode, result.State, result.Planned, result.Applied, result.Skipped)
	if result.RunID != "" {
		_, _ = fmt.Fprintf(w, "Run %s.\n", result.RunID)
	}
	if result.Reason != "" {
		_, _ = fmt.Fprintf(w, "Reason: %s.\n", result.Reason)
	}
	table := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "  KIND\tPLANNED\tAPPLIED\tSKIPPED")
	for _, t := range result.Targets {
		_, _ = fmt.Fprintf(table, "  %s\t%d\t%d\t%d\n", t.Kind, t.Planned, t.Applied, t.Skipped)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	for _, j := range result.Jobs {
		state := "queued"
		if j.Replayed {
			state = "already active"
		}
		_, _ = fmt.Fprintf(w, "Job %s for library %s: %s; follow it with jelee-cli jobs get --id %s.\n", j.JobID, j.LibraryID, state, j.JobID)
	}
	if len(result.Info) > 0 {
		keys := make([]string, 0, len(result.Info))
		for k := range result.Info {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, result.Info[k]))
		}
		_, _ = fmt.Fprintf(w, "Info: %s.\n", strings.Join(parts, " "))
	}
	for _, s := range result.Samples {
		var parts []string
		for _, kv := range [][2]string{{"library", s.LibraryID}, {"item", s.ItemID}, {"source", s.SourceID}, {"user", s.UserID}, {"day", s.Day}, {"object", s.Object}, {"path", s.Path}} {
			if kv[1] != "" {
				parts = append(parts, kv[0]+"="+kv[1])
			}
		}
		_, _ = fmt.Fprintf(w, "  - %s %s\n", s.Kind, strings.Join(parts, " "))
	}
	if result.SamplesTruncated {
		_, _ = fmt.Fprintf(w, "  … more objects than the %d samples; see --json counts.\n", domain.RepairSamples)
	}
	var err error
	switch {
	case result.DryRun && result.Planned > 0:
		_, err = fmt.Fprintf(w, "Apply with: jelee-cli repair %s --yes\n", result.Action)
	case result.Revertible && result.Applied > 0:
		_, err = fmt.Fprintf(w, "Undo with: jelee-cli repair revert --run %s --yes\n", result.RunID)
	}
	return err
}

func writeRepairRevert(w io.Writer, result domain.RepairRevertResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(result)
	}
	_, err := fmt.Fprintf(w, "Run %s: %d repairs reverted, %d skipped (row changed since the repair or already reverted).\n", result.RunID, result.Reverted, result.Skipped)
	return err
}
