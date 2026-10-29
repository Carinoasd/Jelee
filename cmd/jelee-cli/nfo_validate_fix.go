package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// nfoFixKeySuffix turns the validate key into the key of the fix export, so one
// --key names both submissions and a repeated command replays both.
const nfoFixKeySuffix = "-fix"

// nfoFixPollInterval is how often `nfo validate --fix` looks at the validate
// job; tests shorten it.
var nfoFixPollInterval = time.Second

// nfoFixWaitLimit bounds how long the command waits for the validate job.
const nfoFixWaitLimit = 6 * time.Hour

// runNFOValidateFixCLI is `jelee-cli nfo validate --library X --fix` (G39.11).
// It submits the library validation job as `nfo validate` does, waits for it to
// succeed (a library runs one job at a time, so the repair cannot be queued
// beside it), then submits an nfo_export job in fix mode with the key
// KEY-fix and prints both jobs. The repair is the writer's: safe fixes only,
// each with its .jelee.bak backup; see docs/nfo-export-jobs.md.
func runNFOValidateFixCLI(ctx context.Context, c nfoCLIRequest, base *url.URL, token string, stdout, stderr io.Writer) int {
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	call := func(method, path, key string, body any) ([]byte, bool) {
		u := *base
		u.Path = path
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = strings.NewReader(string(raw))
		}
		request, err := http.NewRequestWithContext(ctx, method, u.String(), reader) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
		if err != nil {
			nfoCLIError(ctx, stderr, "nfo_request_failed")
			return nil, false
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(request) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
		if err != nil {
			nfoCLIError(ctx, stderr, "nfo_service_unavailable")
			return nil, false
		}
		defer func() { _ = response.Body.Close() }()
		payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(payload) > 1<<20 {
			nfoCLIError(ctx, stderr, "nfo_response_invalid")
			return nil, false
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
			if ctx.Err() != nil {
				nfoCLIError(ctx, stderr, "nfo_request_failed")
			} else if code := nfoWriteServerCode(payload); code != "" {
				_, _ = fmt.Fprintf(stderr, "nfo_request_rejected (HTTP %d %s)\n", response.StatusCode, code)
			} else {
				_, _ = fmt.Fprintf(stderr, "nfo_request_rejected (HTTP %d)\n", response.StatusCode)
			}
			return nil, false
		}
		return payload, true
	}
	scanJob := func(payload []byte) (domain.Job, bool) {
		envelope, ok := jobsCLIObject(payload)
		if !ok || !jobsCLIHas(envelope, "data") {
			return domain.Job{}, false
		}
		job, ok := decodeCLIJob(envelope["data"])
		return job, ok && job.LibraryID == c.library
	}

	payload, ok := call(http.MethodPost, "/api/v1/libraries/"+c.library+"/nfo/validate", c.key, map[string]string{"priority": c.priority})
	if !ok {
		return 1
	}
	validate, ok := scanJob(payload)
	if !ok {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	deadline := time.Now().Add(nfoFixWaitLimit)
	for validate.State == domain.JobQueued || validate.State == domain.JobRunning {
		if time.Now().After(deadline) {
			_, _ = fmt.Fprintf(stderr, "nfo_validate_wait_timeout (job %s)\n", validate.ID)
			return 1
		}
		select {
		case <-ctx.Done():
			return nfoCLIError(ctx, stderr, "nfo_request_failed")
		case <-time.After(nfoFixPollInterval):
		}
		if payload, ok = call(http.MethodGet, "/api/v1/jobs/"+validate.ID, "", nil); !ok {
			return 1
		}
		polled, valid := scanJob(payload)
		if !valid || polled.ID != validate.ID {
			return nfoCLIError(ctx, stderr, "nfo_response_invalid")
		}
		validate = polled
	}
	if validate.State != domain.JobSucceeded {
		_, _ = fmt.Fprintf(stderr, "nfo_validate_not_succeeded (job %s %s)\n", validate.ID, validate.State)
		return 1
	}
	payload, ok = call(http.MethodPost, "/api/v1/libraries/"+c.library+"/nfo/export", c.key+nfoFixKeySuffix, map[string]string{"priority": c.priority, "mode": domain.NFOExportFix})
	if !ok {
		_, _ = fmt.Fprintf(stderr, "nfo_fix_not_submitted (validate job %s)\n", validate.ID)
		return 1
	}
	fixed, ok := decodeCLINFOExportResponse(payload, nfoExportCLIRequest{command: "export", library: c.library})
	fix, isJob := fixed.(domain.Job)
	if !ok || !isJob {
		return nfoCLIError(ctx, stderr, "nfo_response_invalid")
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	stop := accountCloseOnCancellation(ctx, stdout)
	defer stop()
	if json.NewEncoder(stdout).Encode(map[string]any{"data": map[string]any{"validate": validate, "fix": fix}}) != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	return 0
}
