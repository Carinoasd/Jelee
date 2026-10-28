package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const nfoExportUsage = "usage: jelee-cli nfo export --library UUID [--priority manual|background] --key KEY --token-stdin [--url ORIGIN] [--json] | export-status --job UUID --token-stdin [--url ORIGIN] [--json]"

// nfoExportCLIRequest is a parsed batch export command (G39.14).
type nfoExportCLIRequest struct {
	command, library, job, key, priority string
	asJSON                               bool
}

func parseNFOExportCLI(argv []string) (c nfoExportCLIRequest, base string, ok bool) {
	if len(argv) == 0 || argv[0] != "export" && argv[0] != "export-status" {
		return c, "", false
	}
	c.command = argv[0]
	flags := flag.NewFlagSet("nfo "+c.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	baseFlag := flags.String("url", "http://127.0.0.1:8097", "service origin")
	fromStdin := flags.Bool("token-stdin", false, "read bearer token from stdin")
	flags.BoolVar(&c.asJSON, "json", false, "print the JSON response")
	if c.command == "export" {
		flags.StringVar(&c.library, "library", "", "library UUID")
		flags.StringVar(&c.priority, "priority", domain.JobPriorityManual, "queue priority")
		flags.StringVar(&c.key, "key", "", "idempotency key")
	} else {
		flags.StringVar(&c.job, "job", "", "job UUID")
	}
	if flags.Parse(argv[1:]) != nil || flags.NArg() != 0 || !*fromStdin {
		return c, "", false
	}
	if c.command == "export" && (!domain.ValidID(c.library) || !validNFOCLIKey(c.key) ||
		c.priority != domain.JobPriorityManual && c.priority != domain.JobPriorityBackground) ||
		c.command == "export-status" && !domain.ValidID(c.job) {
		return c, "", false
	}
	return c, *baseFlag, true
}

func runNFOExportCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, base, ok := parseNFOExportCLI(argv)
	if !ok {
		_, _ = fmt.Fprintln(stderr, nfoExportUsage)
		return 2
	}
	u, err := jobsBaseURL(base)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, nfoExportUsage)
		return 2
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_token_read_failed")
	}
	method, body := http.MethodGet, ""
	if c.command == "export" {
		method = http.MethodPost
		u.Path = "/api/v1/libraries/" + c.library + "/nfo/export"
		raw, _ := json.Marshal(map[string]string{"priority": c.priority})
		body = string(raw)
	} else {
		u.Path = "/api/v1/jobs/" + c.job + "/nfo-export"
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body)) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", c.key)
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	response, err := client.Do(request) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_service_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	if response.StatusCode != http.StatusOK && !(c.command == "export" && response.StatusCode == http.StatusAccepted) {
		if ctx.Err() != nil {
			return nfoCLIError(ctx, stderr, "nfo_request_failed")
		}
		if code := nfoWriteServerCode(payload); code != "" {
			_, _ = fmt.Fprintf(stderr, "nfo_request_rejected (HTTP %d %s)\n", response.StatusCode, code)
		} else {
			_, _ = fmt.Fprintf(stderr, "nfo_request_rejected (HTTP %d)\n", response.StatusCode)
		}
		return 1
	}
	replayed := response.Header.Get("Idempotency-Replayed") == "true"
	value, ok := decodeCLINFOExportResponse(payload, c)
	if !ok {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	stop := accountCloseOnCancellation(ctx, stdout)
	defer stop()
	if c.asJSON {
		out := map[string]any{"data": value}
		if c.command == "export" {
			out["replayed"] = replayed
		}
		if json.NewEncoder(stdout).Encode(out) != nil {
			return nfoCLIError(ctx, stderr, "nfo_output_failed")
		}
		return 0
	}
	if _, err = io.WriteString(stdout, nfoExportSummary(value, replayed)); err != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	return 0
}

func decodeCLINFOExportResponse(raw []byte, c nfoExportCLIRequest) (any, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	envelope, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(envelope, "data") {
		return nil, false
	}
	data := envelope["data"]
	if c.command == "export-status" {
		fields, ok := jobsCLIObject(data)
		names := []string{"jobId", "libraryId", "cursorItemId", "childJobId", "chunks", "written", "unchanged", "skippedMissing", "skippedInvalid", "skippedOther", "failed", "updatedAt"}
		if !ok || !jobsCLIHas(fields, "jobId", "libraryId", "chunks", "written", "unchanged", "skippedMissing", "skippedInvalid", "skippedOther", "failed", "updatedAt") {
			return nil, false
		}
		// Both ids are null while they do not apply, but never absent.
		for _, name := range []string{"cursorItemId", "childJobId"} {
			if _, exists := fields[name]; !exists {
				return nil, false
			}
		}
		public := map[string]json.RawMessage{}
		for _, name := range names {
			public[name] = fields[name]
		}
		encoded, _ := json.Marshal(public)
		var state domain.NFOExportJobState
		if json.Unmarshal(encoded, &state) != nil || state.JobID != c.job || !domain.ValidID(state.LibraryID) ||
			state.CursorItemID != nil && !domain.ValidID(*state.CursorItemID) || state.ChildJobID != nil && !domain.ValidID(*state.ChildJobID) ||
			state.Chunks < 0 || state.Written < 0 || state.Unchanged < 0 || state.SkippedMissing < 0 || state.SkippedInvalid < 0 || state.SkippedOther < 0 || state.Failed < 0 || state.UpdatedAt.IsZero() {
			return nil, false
		}
		return state, true
	}
	var job domain.Job
	if !jobsCLIPublicObject(data, &job, []string{"id", "libraryId", "kind", "state", "priority", "attempts", "cancelRequested", "files", "directories", "skipped", "bytes", "missing", "reviewRequired", "createdAt"}, "errorCode", "startedAt", "finishedAt") ||
		!domain.ValidID(job.ID) || job.LibraryID != c.library || job.Kind != domain.JobNFOExport ||
		job.Priority != domain.JobPriorityManual && job.Priority != domain.JobPriorityBackground || job.CreatedAt.IsZero() {
		return nil, false
	}
	switch job.State {
	case domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobCancelled, domain.JobFailed:
	default:
		return nil, false
	}
	return job, true
}

func nfoExportSummary(value any, replayed bool) string {
	switch v := value.(type) {
	case domain.NFOExportJobState:
		cursor, child := "none", "none"
		if v.CursorItemID != nil {
			cursor = *v.CursorItemID
		}
		if v.ChildJobID != nil {
			child = *v.ChildJobID
		}
		return fmt.Sprintf("job %s library %s\nwritten %d unchanged %d skipped-missing %d skipped-invalid %d skipped-other %d failed %d\nchunks %d write-job %s\ncursor %s\nupdated %s\n",
			v.JobID, v.LibraryID, v.Written, v.Unchanged, v.SkippedMissing, v.SkippedInvalid, v.SkippedOther, v.Failed, v.Chunks, child, cursor, v.UpdatedAt.UTC().Format(time.RFC3339))
	case domain.Job:
		suffix := ""
		if replayed {
			suffix = " (replayed)"
		}
		return fmt.Sprintf("job %s %s%s\nlibrary %s\npriority %s\n", v.ID, v.State, suffix, v.LibraryID, v.Priority)
	}
	return ""
}
