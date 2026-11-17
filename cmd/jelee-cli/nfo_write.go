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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const nfoWriteUsage = "usage: jelee-cli nfo write-policy --library UUID --enable|--disable --token-stdin [--url ORIGIN] [--json] | write prepare --item UUID --revision N --set FIELD=VALUE [--set ...] [--create] [--backups N] [--bom preserve|include|omit] --key KEY --token-stdin [--url ORIGIN] [--json] | write submit --library UUID --preparation UUID [--preparation UUID ...] [--priority manual|background] --key KEY --token-stdin [--url ORIGIN] [--json]"

// repeatedFlag collects every occurrence of a flag in order.
type repeatedFlag []string

func (f *repeatedFlag) String() string     { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(v string) error { *f = append(*f, v); return nil }

// nfoWriteCLIRequest is a parsed write-back command. It holds only what the
// fixed routes accept; the server decides everything else.
type nfoWriteCLIRequest struct {
	command, library, item, key, bom, priority string
	revision                                   int64
	backups                                    int
	enable, asJSON, create                     bool
	edits                                      []domain.NFOWriteTextEdit
	preparations                               []string
}

// nfoWritePreparation is the public preparation summary the server returns.
type nfoWritePreparation struct {
	ID                string    `json:"id"`
	ItemID            string    `json:"itemId"`
	LibraryID         string    `json:"libraryId"`
	Revision          int64     `json:"revision"`
	Fields            []string  `json:"fields"`
	OriginalBytes     int64     `json:"originalBytes"`
	ReplacementBytes  int64     `json:"replacementBytes"`
	OriginalSHA256    string    `json:"originalSha256"`
	ReplacementSHA256 string    `json:"replacementSha256"`
	CreatedAt         time.Time `json:"createdAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

func parseNFOWriteCLI(argv []string) (c nfoWriteCLIRequest, base string, ok bool) {
	if len(argv) == 0 {
		return c, "", false
	}
	c.command = argv[0]
	rest := argv[1:]
	if c.command == "write" {
		if len(rest) == 0 || rest[0] != "prepare" && rest[0] != "submit" {
			return c, "", false
		}
		c.command, rest = "write-"+rest[0], rest[1:]
	}
	flags := flag.NewFlagSet("nfo "+c.command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	baseFlag := flags.String("url", "http://127.0.0.1:8097", "service origin")
	fromStdin := flags.Bool("token-stdin", false, "read bearer token from stdin")
	flags.BoolVar(&c.asJSON, "json", false, "print the JSON response")
	var enable, disable bool
	var sets, preparations repeatedFlag
	switch c.command {
	case "write-policy":
		flags.StringVar(&c.library, "library", "", "library UUID")
		flags.BoolVar(&enable, "enable", false, "allow NFO writes")
		flags.BoolVar(&disable, "disable", false, "forbid NFO writes")
	case "write-prepare":
		flags.StringVar(&c.item, "item", "", "item UUID")
		flags.Int64Var(&c.revision, "revision", 0, "expected item metadata revision")
		flags.Var(&sets, "set", "FIELD=VALUE edit, repeatable")
		flags.BoolVar(&c.create, "create", false, "create a missing NFO instead of rewriting one")
		flags.IntVar(&c.backups, "backups", nfoCLIDefaultBackups, "backups to keep (0 with --create)")
		flags.StringVar(&c.bom, "bom", "", "preserve, include or omit")
		flags.StringVar(&c.key, "key", "", "idempotency key")
	case "write-submit":
		flags.StringVar(&c.library, "library", "", "library UUID")
		flags.Var(&preparations, "preparation", "preparation UUID, repeatable")
		flags.StringVar(&c.priority, "priority", domain.JobPriorityManual, "queue priority")
		flags.StringVar(&c.key, "key", "", "idempotency key")
	default:
		return c, "", false
	}
	if flags.Parse(rest) != nil || flags.NArg() != 0 || !*fromStdin {
		return c, "", false
	}
	c.preparations = preparations
	switch c.command {
	case "write-policy":
		if !domain.ValidID(c.library) || enable == disable {
			return c, "", false
		}
		c.enable = enable
	case "write-prepare":
		if !validNFOCLIKey(c.key) || len(sets) < 1 {
			return c, "", false
		}
		for _, set := range sets {
			field, value, found := strings.Cut(set, "=")
			if !found || !utf8.ValidString(value) {
				return c, "", false
			}
			c.edits = append(c.edits, domain.NFOWriteTextEdit{Field: field, Value: value})
		}
		// A created file has nothing to back up: its backups default to 0 and
		// an explicit non-zero count is a usage error, as the server rules.
		if c.create {
			explicit := false
			flags.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "backups" })
			if !explicit {
				c.backups = 0
			}
		}
		// The server applies the same checks; failing here keeps a mistake
		// from costing a request.
		request := domain.NFOWritePrepareRequest{ItemID: c.item, Revision: c.revision, Edits: c.edits, BOM: c.bom, MaxBytes: domain.NFODefaultSourceBytes, Backups: c.backups, Create: c.create}
		if domain.ValidateNFOWritePrepareRequest(request) != nil {
			return c, "", false
		}
	case "write-submit":
		if !domain.ValidID(c.library) || !validNFOCLIKey(c.key) || len(c.preparations) < 1 || len(c.preparations) > 100 ||
			c.priority != domain.JobPriorityManual && c.priority != domain.JobPriorityBackground {
			return c, "", false
		}
		seen := make(map[string]bool)
		for _, id := range c.preparations {
			if !domain.ValidID(id) || seen[id] {
				return c, "", false
			}
			seen[id] = true
		}
	}
	return c, *baseFlag, true
}

// nfoCLIDefaultBackups matches the server's default.
const nfoCLIDefaultBackups = 3

func runNFOWriteCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, base, ok := parseNFOWriteCLI(argv)
	if !ok {
		_, _ = fmt.Fprintln(stderr, nfoWriteUsage)
		return 2
	}
	u, err := jobsBaseURL(base)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, nfoWriteUsage)
		return 2
	}
	if ctx.Err() != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_token_read_failed")
	}
	method, body := http.MethodPost, map[string]any{}
	switch c.command {
	case "write-policy":
		method = http.MethodPut
		u.Path = "/api/v1/libraries/" + c.library + "/nfo/write-policy"
		body["enabled"] = c.enable
	case "write-prepare":
		u.Path = "/api/v1/items/" + c.item + "/nfo/write-preparations"
		body["revision"], body["edits"], body["backups"] = c.revision, c.edits, c.backups
		if c.bom != "" {
			body["bom"] = c.bom
		}
		if c.create {
			body["create"] = true
		}
	case "write-submit":
		u.Path = "/api/v1/libraries/" + c.library + "/nfo/writes"
		body["preparations"], body["priority"] = c.preparations, c.priority
	}
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(string(raw))) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		return nfoCLIError(ctx, stderr, "nfo_request_failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if c.key != "" {
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
	if !nfoWriteStatusOK(c.command, response.StatusCode) {
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
	value, ok := decodeCLINFOWriteResponse(payload, c)
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
		if c.command != "write-policy" {
			out["replayed"] = replayed
		}
		if json.NewEncoder(stdout).Encode(out) != nil {
			return nfoCLIError(ctx, stderr, "nfo_output_failed")
		}
		return 0
	}
	if _, err = io.WriteString(stdout, nfoWriteSummary(c, value, replayed)); err != nil {
		return nfoCLIError(ctx, stderr, "nfo_output_failed")
	}
	return 0
}

func nfoWriteStatusOK(command string, status int) bool {
	switch command {
	case "write-policy":
		return status == http.StatusOK
	case "write-prepare":
		return status == http.StatusOK || status == http.StatusCreated
	}
	return status == http.StatusOK || status == http.StatusAccepted
}

// nfoWriteServerCode returns the stable public error code of a refusal, or
// nothing when the body is not shaped like one.
func nfoWriteServerCode(raw []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	code := envelope.Error.Code
	if len(code) < 1 || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if r != '_' && (r < 'a' || r > 'z') {
			return ""
		}
	}
	return code
}

func decodeCLINFOWriteResponse(raw []byte, c nfoWriteCLIRequest) (any, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	envelope, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(envelope, "data") {
		return nil, false
	}
	data := envelope["data"]
	switch c.command {
	case "write-policy":
		var value domain.NFOLibraryPolicy
		if !jobsCLIPublicObject(data, &value, []string{"libraryId", "mode", "generation", "writeEnabled"}) ||
			domain.ValidateNFOLibraryPolicy(value) != nil || value.LibraryID != c.library || value.WriteEnabled != c.enable {
			return nil, false
		}
		return value, true
	case "write-prepare":
		var value nfoWritePreparation
		if !jobsCLIPublicObject(data, &value, []string{"id", "itemId", "libraryId", "revision", "fields", "originalBytes", "replacementBytes", "originalSha256", "replacementSha256", "createdAt", "expiresAt"}) ||
			!validNFOWritePreparation(value, c) {
			return nil, false
		}
		return value, true
	}
	var job domain.Job
	if !jobsCLIPublicObject(data, &job, []string{"id", "libraryId", "kind", "state", "priority", "attempts", "cancelRequested", "files", "directories", "skipped", "bytes", "missing", "reviewRequired", "createdAt"}, "errorCode", "startedAt", "finishedAt", "progress") ||
		!cliJobProgress(job.Progress) ||
		!domain.ValidID(job.ID) || job.LibraryID != c.library || job.Kind != domain.JobNFOWrite ||
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

func validNFOWritePreparation(p nfoWritePreparation, c nfoWriteCLIRequest) bool {
	fields := make([]string, 0, len(c.edits))
	for _, edit := range c.edits {
		fields = append(fields, edit.Field)
	}
	return domain.ValidID(p.ID) && p.ItemID == c.item && domain.ValidID(p.LibraryID) && p.Revision == c.revision && slices.Equal(p.Fields, fields) &&
		(p.OriginalBytes >= 1 || c.create && p.OriginalBytes == 0) && (p.OriginalBytes == 0) == c.create && p.OriginalBytes <= domain.NFOMaxSourceBytes && p.ReplacementBytes >= 1 && p.ReplacementBytes <= domain.NFOMaxSourceBytes &&
		validNFOCLIDigest(p.OriginalSHA256) && validNFOCLIDigest(p.ReplacementSHA256) &&
		!p.CreatedAt.IsZero() && p.ExpiresAt.After(p.CreatedAt)
}

func validNFOCLIDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func nfoWriteSummary(c nfoWriteCLIRequest, value any, replayed bool) string {
	suffix := ""
	if replayed {
		suffix = " (replayed)"
	}
	switch v := value.(type) {
	case domain.NFOLibraryPolicy:
		state := "disabled"
		if v.WriteEnabled {
			state = "enabled"
		}
		return fmt.Sprintf("NFO writes %s for library %s (mode %s, generation %d)\n", state, v.LibraryID, v.Mode, v.Generation)
	case nfoWritePreparation:
		verb := "prepared"
		if v.OriginalBytes == 0 {
			verb = "prepared (create)"
		}
		return fmt.Sprintf("%s %s%s\nitem %s revision %d\nfields %s\noriginal %d bytes sha256 %s\nreplacement %d bytes sha256 %s\nexpires %s\n",
			verb, v.ID, suffix, v.ItemID, v.Revision, strings.Join(v.Fields, ","), v.OriginalBytes, v.OriginalSHA256, v.ReplacementBytes, v.ReplacementSHA256, v.ExpiresAt.UTC().Format(time.RFC3339))
	case domain.Job:
		return fmt.Sprintf("job %s %s%s\nlibrary %s preparations %s\npriority %s\n", v.ID, v.State, suffix, v.LibraryID, strconv.Itoa(len(c.preparations)), v.Priority)
	}
	return ""
}
