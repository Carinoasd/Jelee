package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func runLibraryCLI(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli library add --name NAME --root ABSOLUTE_DIRECTORY")
		return 2
	}
	if len(argv) == 0 || argv[0] != "add" {
		return usage()
	}
	flags := flag.NewFlagSet("library add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "library name")
	root := flags.String("root", "", "absolute media root")
	if flags.Parse(argv[1:]) != nil || flags.NArg() != 0 || *name == "" || !filepath.IsAbs(*root) {
		return usage()
	}
	if err := scan.ValidateRoot(ctx, *root); err != nil {
		fmt.Fprintln(stderr, "library_root_unavailable")
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "library_configuration_invalid")
		return 1
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
	if err != nil {
		fmt.Fprintln(stderr, "library_database_unavailable")
		return 1
	}
	defer store.Pool.Close()
	result, err := store.RegisterLibrary(ctx, *name, filepath.Clean(*root))
	if err != nil {
		fmt.Fprintln(stderr, "library_registration_failed")
		return 1
	}
	stop := accountCloseOnCancellation(ctx, stdout)
	defer stop()
	if json.NewEncoder(stdout).Encode(result) != nil {
		fmt.Fprintln(stderr, "library_output_failed")
		return 1
	}
	return 0
}

func jobsBaseURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Opaque != "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, domain.ErrInvalid
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, domain.ErrInvalid
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, domain.ErrInvalid
		}
	}
	u.Path = ""
	return u, nil
}

func readJobsToken(ctx context.Context, input io.Reader) (string, error) {
	if input == nil {
		return "", domain.ErrInvalid
	}
	stop := accountCloseOnCancellation(ctx, input)
	defer stop()
	data, err := io.ReadAll(io.LimitReader(accountContextReader{ctx: ctx, reader: input}, 46))
	if err != nil {
		return "", err
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(token) != 43 {
		return "", domain.ErrInvalid
	}
	for _, r := range token {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", domain.ErrInvalid
		}
	}
	return token, nil
}

func runJobsCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli jobs scan|ignore|probe|probe-rebuild-library|probe-rebuild-item|list|libraries|get|entries|cancel|retry --token-stdin [--url http://127.0.0.1:8097] [--id UUID] [--key ASCII] [--priority manual|background] [--probe] [--nfo] [--ignore jeleeignore|jeleeignore-legacy-v1 --ignore-case sensitive|ascii-insensitive] [--cursor CURSOR] [--limit 50] [--state STATE]")
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	command := argv[0]
	flags := flag.NewFlagSet("jobs "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("url", "http://127.0.0.1:8097", "service origin")
	fromStdin := flags.Bool("token-stdin", false, "read bearer token from stdin")
	var id, key, priority, cursor, state string
	var enableProbe, enableNFO bool
	var ignoreMode, ignoreCase string
	limit := 50
	switch command {
	case "scan":
		flags.StringVar(&ignoreMode, "ignore", "", "ignore rule mode")
		flags.StringVar(&ignoreCase, "ignore-case", "", "ignore case behavior")
		flags.BoolVar(&enableProbe, "probe", false, "probe metadata after inventory")
		flags.BoolVar(&enableNFO, "nfo", false, "validate NFO after inventory")
		fallthrough
	case "probe-rebuild-library", "probe-rebuild-item":
		flags.StringVar(&priority, "priority", "manual", "queue priority")
		fallthrough
	case "retry":
		flags.StringVar(&key, "key", "", "idempotency key")
		fallthrough
	case "get", "cancel", "probe":
		flags.StringVar(&id, "id", "", "library or job UUID")
	case "ignore":
		flags.StringVar(&id, "id", "", "job UUID")
		flags.StringVar(&cursor, "cursor", "", "opaque report cursor")
		flags.IntVar(&limit, "limit", 50, "page size")
	case "entries":
		flags.StringVar(&id, "id", "", "job UUID")
		fallthrough
	case "list", "libraries":
		flags.StringVar(&cursor, "cursor", "", "UUID cursor")
		flags.IntVar(&limit, "limit", 50, "page size")
		if command == "list" {
			flags.StringVar(&state, "state", "", "job state")
		}
	default:
		return usage()
	}
	if flags.Parse(argv[1:]) != nil || flags.NArg() != 0 || !*fromStdin {
		return usage()
	}
	ignoreIntent := domain.IgnoreIntent{Mode: ignoreMode, CaseMode: ignoreCase}
	if domain.ValidateIgnoreIntent(ignoreIntent) != nil && domain.ValidateFamilyIgnoreIntent(ignoreIntent) != nil {
		return usage()
	}
	u, err := jobsBaseURL(*base)
	if err != nil {
		return usage()
	}
	enqueue := command == "scan" || command == "probe-rebuild-library" || command == "probe-rebuild-item"
	if id != "" && !domain.ValidID(id) || (enqueue || command == "retry" || command == "get" || command == "entries" || command == "cancel" || command == "probe" || command == "ignore") && !domain.ValidID(id) || command != "ignore" && cursor != "" && !domain.ValidID(cursor) || command == "ignore" && !validCLIIgnoreCursor(cursor) || limit < 1 || limit > 100 {
		return usage()
	}
	if enqueue || command == "retry" {
		if len(key) < 1 || len(key) > 128 {
			return usage()
		}
		for _, r := range key {
			if r < '!' || r > '~' {
				return usage()
			}
		}
	}
	if enqueue && priority != "manual" && priority != "background" {
		return usage()
	}
	if state != "" && state != "queued" && state != "running" && state != "succeeded" && state != "failed" && state != "cancelled" {
		return usage()
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "jobs_token_read_failed")
		return 1
	}
	method, body := http.MethodGet, ""
	switch command {
	case "scan":
		u.Path = "/api/v1/libraries/" + id + "/scan"
		method = http.MethodPost
		input := map[string]any{"priority": priority}
		if ignoreMode != "" {
			input["ignore"] = map[string]string{"mode": ignoreMode, "caseMode": ignoreCase}
		}
		if enableProbe {
			input["probe"] = true
		}
		if enableNFO {
			input["nfo"] = true
		}
		data, _ := json.Marshal(input)
		body = string(data)
	case "probe-rebuild-library", "probe-rebuild-item":
		target := "libraries"
		if command == "probe-rebuild-item" {
			target = "items"
		}
		u.Path = "/api/v1/" + target + "/" + id + "/probe/rebuild"
		method = http.MethodPost
		data, _ := json.Marshal(map[string]string{"priority": priority})
		body = string(data)
	case "probe":
		u.Path = "/api/v1/jobs/" + id + "/probe"
	case "ignore":
		u.Path = "/api/v1/jobs/" + id + "/ignore"
	case "list":
		u.Path = "/api/v1/jobs"
	case "libraries":
		u.Path = "/api/v1/libraries"
	case "get":
		u.Path = "/api/v1/jobs/" + id
	case "entries", "cancel", "retry":
		u.Path = "/api/v1/jobs/" + id + "/" + command
		if command != "entries" {
			method = http.MethodPost
			body = "{}"
		}
	}
	if command == "list" || command == "entries" || command == "libraries" || command == "ignore" {
		q := url.Values{"limit": []string{strconv.Itoa(limit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if state != "" {
			q.Set("state", state)
		}
		u.RawQuery = q.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		fmt.Fprintln(stderr, "jobs_request_failed")
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintln(stderr, "jobs_service_unavailable")
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 202 {
		fmt.Fprintf(stderr, "jobs_request_rejected (HTTP %d)\n", response.StatusCode)
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(stderr, "jobs_response_invalid")
		return 1
	}
	// Validate required fields before re-encoding only the public contract.
	decoded, valid := decodeJobsCLIResponse(data, command, limit)
	if !valid {
		fmt.Fprintln(stderr, "jobs_response_invalid")
		return 1
	}
	stop := accountCloseOnCancellation(ctx, stdout)
	defer stop()
	if json.NewEncoder(stdout).Encode(decoded) != nil {
		fmt.Fprintln(stderr, "jobs_output_failed")
		return 1
	}
	return 0
}

type jobsCLIPagination struct {
	NextCursor string `json:"nextCursor"`
	Limit      int    `json:"limit"`
}

// Object fields are selected by their exact public spelling. Unknown fields,
// including case aliases understood by encoding/json, cannot override a field
// or become output. Duplicate object keys are rejected rather than merged.
func jobsCLIObject(raw []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return nil, false
	}
	_, err = decoder.Token()
	return fields, err == io.EOF
}

func jobsCLIHas(fields map[string]json.RawMessage, required ...string) bool {
	for _, name := range required {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func jobsCLIPublicObject(raw []byte, target any, required []string, optional ...string) bool {
	fields, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(fields, required...) {
		return false
	}
	public := make(map[string]json.RawMessage, len(required)+len(optional))
	for _, name := range required {
		public[name] = fields[name]
	}
	for _, name := range optional {
		if value, exists := fields[name]; exists {
			if !jobsCLIHas(fields, name) {
				return false
			}
			public[name] = value
		}
	}
	data, err := json.Marshal(public)
	return err == nil && json.Unmarshal(data, target) == nil
}

func decodeJobsCLIResponse(raw []byte, command string, limit int) (any, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	envelope, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(envelope, "data") {
		return nil, false
	}
	var data any
	switch command {
	case "ignore":
		data, ok = decodeCLIIgnoreReport(envelope["data"], limit)
	case "probe":
		data, ok = decodeCLIProbeSummary(envelope["data"])
	case "list":
		data, ok = decodeJobsCLIPage(envelope["data"], "jobs", limit, decodeCLIJob)
	case "entries":
		data, ok = decodeJobsCLIPage(envelope["data"], "entries", limit, decodeCLIInventory)
	case "libraries":
		data, ok = decodeJobsCLIPage(envelope["data"], "libraries", limit, decodeCLILibrary)
	default:
		data, ok = decodeCLIJob(envelope["data"])
	}
	if !ok {
		return nil, false
	}
	return map[string]any{"data": data}, true
}

func decodeJobsCLIPage[T any](raw []byte, field string, limit int, decode func([]byte) (T, bool)) (any, bool) {
	fields, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(fields, field, "pagination") || limit < 1 || limit > 100 {
		return nil, false
	}
	var page jobsCLIPagination
	if !jobsCLIPublicObject(fields["pagination"], &page, []string{"nextCursor", "limit"}) || page.Limit != limit || page.NextCursor != "" && !domain.ValidID(page.NextCursor) {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(fields[field], &items) != nil || items == nil || len(items) > limit {
		return nil, false
	}
	result := make([]T, 0, len(items))
	for _, item := range items {
		value, valid := decode(item)
		if !valid {
			return nil, false
		}
		result = append(result, value)
	}
	return map[string]any{field: result, "pagination": page}, true
}

func decodeCLIJob(raw []byte) (domain.Job, bool) {
	var j domain.Job
	if !jobsCLIPublicObject(raw, &j, []string{"id", "libraryId", "kind", "state", "priority", "attempts", "cancelRequested", "files", "directories", "skipped", "bytes", "missing", "reviewRequired", "createdAt"}, "errorCode", "startedAt", "finishedAt") ||
		!domain.ValidID(j.ID) || !domain.ValidID(j.LibraryID) || j.Kind != "inventory_scan" ||
		j.Priority != domain.JobPriorityManual && j.Priority != domain.JobPriorityBackground ||
		j.Attempts < 0 || j.Files < 0 || j.Directories < 0 || j.Skipped < 0 || j.Bytes < 0 || j.Missing < 0 ||
		j.CreatedAt.IsZero() || j.StartedAt != nil && j.StartedAt.IsZero() || j.FinishedAt != nil && j.FinishedAt.IsZero() {
		return domain.Job{}, false
	}
	switch j.State {
	case domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobCancelled:
		if j.ErrorCode != "" {
			return domain.Job{}, false
		}
	case domain.JobFailed:
		switch j.ErrorCode {
		case "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted":
		default:
			return domain.Job{}, false
		}
	default:
		return domain.Job{}, false
	}
	return j, true
}

func jobsCLIText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) < 1 || len(value) > maxBytes {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func decodeCLIInventory(raw []byte) (domain.InventoryEntry, bool) {
	var e domain.InventoryEntry
	if !jobsCLIPublicObject(raw, &e, []string{"id", "rootId", "path", "kind", "size", "modifiedUnixNano"}) ||
		!domain.ValidID(e.ID) || !domain.ValidID(e.RootID) || e.Size < 0 || !jobsCLIText(e.Path, domain.ScanPathMaxBytes) ||
		e.Path == "." || !fs.ValidPath(e.Path) || strings.ContainsAny(e.Path, `\:`) {
		return domain.InventoryEntry{}, false
	}
	switch e.Kind {
	case "video", "nfo", "image", "other":
		return e, true
	default:
		return domain.InventoryEntry{}, false
	}
}

func decodeCLILibrary(raw []byte) (domain.LibrarySummary, bool) {
	var l domain.LibrarySummary
	if !jobsCLIPublicObject(raw, &l, []string{"id", "name", "roots"}) || !domain.ValidID(l.ID) || l.Roots < 0 ||
		!jobsCLIText(l.Name, 128) || strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.Name) != l.Name {
		return domain.LibrarySummary{}, false
	}
	return l, true
}
