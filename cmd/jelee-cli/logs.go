package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// "jelee-cli logs" (G46.2, G46.7, G46.9).
//
// tail, filter and export read the local log files (the configured file and
// its rotated, possibly gzipped backups) and re-apply the logging whitelist
// to every line, like the diagnostic bundle, so their output never holds
// more than the live sinks wrote. level, levels and retention change the
// running servers through the administration API with an administrator
// token read from stdin.

const logsUsage = `usage: jelee-cli logs tail [--lines N] [--follow] [FILTERS] [--format json|console] [--file PATH]
       jelee-cli logs filter [FILTERS] [--limit N] [--format json|console] [--file PATH]
       jelee-cli logs export --out FILE|- [FILTERS] [--max-bytes N] [--file PATH]
       jelee-cli logs level [--component SCOPE] LEVEL|reset [--ttl DURATION] --token-stdin [--url ORIGIN]
       jelee-cli logs levels --token-stdin [--url ORIGIN] [--json]
       jelee-cli logs retention [--log-days N] [--log-max-total-mb N] [--audit-days N] [--security-days N] --token-stdin [--url ORIGIN] [--json]
FILTERS: --level debug|info|warn|error (minimum) --component SCOPE --since TIME|DURATION --until TIME|DURATION --trace TRACE_ID`

// Bounds of the local readers.
const (
	logsMaxLine          = 1 << 20
	logsDefaultTail      = 100
	logsMaxTail          = 100000
	logsDefaultLimit     = 1000
	logsMaxLimit         = 1000000
	logsDefaultExport    = 64 << 20
	logsMaxExport        = 1 << 30
	logsFollowInterval   = 500 * time.Millisecond
	logsAPIResponseLimit = 1 << 20
)

type logsCLIDependencies struct {
	load func() (config.Config, error)
	now  func() time.Time
	// client sends the API requests; nil uses a bounded client.
	client *http.Client
	// followInterval polls the active file of tail --follow.
	followInterval time.Duration
}

func runLogsCLI(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runLogsCLIWith(ctx, argv, stdin, stdout, stderr, logsCLIDependencies{load: config.Load, now: time.Now})
}

func runLogsCLIWith(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer, deps logsCLIDependencies) int {
	usage := func() int {
		_, _ = fmt.Fprintln(stderr, logsUsage)
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.followInterval <= 0 {
		deps.followInterval = logsFollowInterval
	}
	switch argv[0] {
	case "tail", "filter", "export":
		return runLogsLocal(ctx, argv[0], argv[1:], stdout, stderr, deps, usage)
	case "level", "levels", "retention":
		return runLogsAPI(ctx, argv[0], argv[1:], stdin, stdout, stderr, deps, usage)
	}
	return usage()
}

// logFilter selects records by minimum level, scope, time window and trace.
type logFilter struct {
	level        int
	hasLevel     bool
	scope        string
	since, until time.Time
	trace        string
	redactor     *logging.Redactor
}

func levelRank(name string) (int, bool) {
	switch strings.ToUpper(name) {
	case "DEBUG":
		return 0, true
	case "INFO":
		return 1, true
	case "WARN", "WARNING":
		return 2, true
	case "ERROR":
		return 3, true
	}
	// slog writes offsets such as "INFO+2"; they rank with their base.
	if base, _, ok := strings.Cut(strings.ToUpper(name), "+"); ok {
		return levelRank(base)
	}
	if base, _, ok := strings.Cut(strings.ToUpper(name), "-"); ok {
		return levelRank(base)
	}
	return 0, false
}

// parseLogTime accepts RFC 3339 or a duration before now.
func parseLogTime(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return time.Time{}, domain.ErrInvalid
	}
	return now.Add(-d), nil
}

// match re-applies the whitelist to line and reports whether the record
// passes the filter, with the redacted line.
func (f *logFilter) match(line []byte) ([]byte, map[string]any, bool) {
	clean, ok := f.redactor.Line(line)
	if !ok {
		return nil, nil, false
	}
	var record map[string]any
	dec := json.NewDecoder(bytes.NewReader(clean))
	dec.UseNumber()
	if dec.Decode(&record) != nil {
		return nil, nil, false
	}
	if f.hasLevel {
		name, _ := record["level"].(string)
		rank, ok := levelRank(name)
		if !ok || rank < f.level {
			return nil, nil, false
		}
	}
	if f.scope != "" {
		component, _ := record["component"].(string)
		scope, ok := logging.ScopeFor(component)
		if !ok || scope != f.scope {
			return nil, nil, false
		}
	}
	if !f.since.IsZero() || !f.until.IsZero() {
		stamp, _ := record["time"].(string)
		t, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !f.since.IsZero() && t.Before(f.since) || !f.until.IsZero() && !t.Before(f.until) {
			return nil, nil, false
		}
	}
	if f.trace != "" {
		if trace, _ := record["traceId"].(string); trace != f.trace {
			return nil, nil, false
		}
	}
	return clean, record, true
}

// render writes one record as JSON or as a console line.
func renderLogRecord(w io.Writer, clean []byte, record map[string]any, console bool) error {
	if !console {
		_, err := w.Write(append(clean, '\n'))
		return err
	}
	var b strings.Builder
	for _, key := range []string{"time", "level", "component", "msg"} {
		if v, ok := record[key]; ok {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprint(&b, v)
		}
	}
	keys := make([]string, 0, len(record))
	for key := range record {
		switch key {
		case "time", "level", "component", "msg":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, _ := json.Marshal(record[key])
		fmt.Fprintf(&b, " %s=%s", key, value)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// openLogFile opens a log file or a gzipped backup for reading.
func openLogFile(name string) (io.ReadCloser, error) {
	f, err := os.Open(name) //nolint:gosec // G304: the configured log file and its rotated backups
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, closeBoth{gz, f}}, nil
}

type closeBoth struct{ a, b io.Closer }

func (c closeBoth) Close() error { return errors.Join(c.a.Close(), c.b.Close()) }

// eachLine calls fn with every complete line of r shorter than
// logsMaxLine; longer lines are skipped. fn returns false to stop.
func eachLine(ctx context.Context, r io.Reader, fn func([]byte) bool) error {
	reader := bufio.NewReaderSize(r, 64<<10)
	var long bool
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if long {
			long = isPrefix
			continue
		}
		line = append(line, chunk...)
		if isPrefix {
			if len(line) > logsMaxLine {
				line, long = line[:0], true
			}
			continue
		}
		if !fn(line) {
			return nil
		}
		line = line[:0]
	}
}

func runLogsLocal(ctx context.Context, command string, argv []string, stdout, stderr io.Writer, deps logsCLIDependencies, usage func() int) int {
	flags := flag.NewFlagSet("logs "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	file := flags.String("file", "", "log file (default: logging.file.path of the configuration)")
	levelName := flags.String("level", "", "minimum level")
	component := flags.String("component", "", "scope or component name")
	sinceText := flags.String("since", "", "RFC 3339 time or duration before now")
	untilText := flags.String("until", "", "RFC 3339 time or duration before now")
	trace := flags.String("trace", "", "trace ID (32 hexadecimal digits)")
	format := flags.String("format", "json", "json or console")
	var lines, limit int
	var follow bool
	var out string
	var maxBytes int64
	switch command {
	case "tail":
		flags.IntVar(&lines, "lines", logsDefaultTail, "records to show")
		flags.BoolVar(&follow, "follow", false, "keep printing new records")
	case "filter":
		flags.IntVar(&limit, "limit", logsDefaultLimit, "maximum records")
	case "export":
		flags.StringVar(&out, "out", "", "NDJSON output file, or - for stdout")
		flags.Int64Var(&maxBytes, "max-bytes", logsDefaultExport, "maximum output bytes")
	}
	if flags.Parse(argv) != nil || flags.NArg() != 0 {
		return usage()
	}
	now := deps.now()
	filter := &logFilter{}
	var err error
	if *levelName != "" {
		rank, ok := levelRank(*levelName)
		if !ok || strings.Contains(*levelName, "+") || strings.Contains(*levelName, "-") {
			return usage()
		}
		filter.level, filter.hasLevel = rank, true
	}
	if *component != "" {
		scope, ok := logging.ScopeFor(*component)
		if !ok {
			_, _ = fmt.Fprintln(stderr, "logs_component_unknown")
			return 2
		}
		filter.scope = scope
	}
	if filter.since, err = parseLogTime(*sinceText, now); err != nil {
		return usage()
	}
	if filter.until, err = parseLogTime(*untilText, now); err != nil {
		return usage()
	}
	if *trace != "" && !validTraceID(*trace) {
		return usage()
	}
	filter.trace = *trace
	console := *format == "console"
	switch {
	case *format != "json" && *format != "console",
		command == "tail" && (lines < 1 || lines > logsMaxTail),
		command == "filter" && (limit < 1 || limit > logsMaxLimit),
		command == "export" && (out == "" || maxBytes < 1 || maxBytes > logsMaxExport):
		return usage()
	}
	cfg, err := deps.load()
	if err != nil && *file == "" {
		_, _ = fmt.Fprintln(stderr, "logs_configuration_invalid")
		return 1
	}
	path := *file
	if path == "" {
		path = cfg.Logging.File.Path
	}
	if path == "" {
		_, _ = fmt.Fprintln(stderr, "logs_no_file_configured: set logging.output to file or both with logging.file.path, or pass --file")
		return 1
	}
	filter.redactor = logging.NewRedactor(logging.IPMode(cfg.Logging.IPMode), logging.PathMode(cfg.Logging.PathMode), cfg.Logging.PathRoots)
	files, err := logging.ListLogFiles(path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
		return 1
	}
	switch command {
	case "tail":
		return logsTail(ctx, path, filter, lines, follow, console, stdout, stderr, deps)
	case "filter":
		return logsScan(ctx, path, files, filter, stderr, func(clean []byte, record map[string]any) (bool, error) {
			limit--
			return limit > 0, renderLogRecord(stdout, clean, record, console)
		})
	}
	return logsExport(ctx, path, files, filter, out, maxBytes, stdout, stderr)
}

func validTraceID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// logsScan reads files oldest first and calls emit for every matching
// record until emit returns false. A backup rotated before --since holds
// only older records and is skipped.
func logsScan(ctx context.Context, path string, files []string, filter *logFilter, stderr io.Writer, emit func([]byte, map[string]any) (bool, error)) int {
	for _, name := range files {
		if stamp, ok := logging.BackupTime(path, name); ok && !filter.since.IsZero() && stamp.Before(filter.since) {
			continue
		}
		reader, err := openLogFile(name)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
			return 1
		}
		more := true
		var writeErr error
		err = eachLine(ctx, reader, func(line []byte) bool {
			clean, record, ok := filter.match(line)
			if !ok {
				return true
			}
			more, writeErr = emit(clean, record)
			return more && writeErr == nil
		})
		_ = reader.Close()
		switch {
		case writeErr != nil:
			_, _ = fmt.Fprintln(stderr, "logs_output_failed")
			return 1
		case errors.Is(err, context.Canceled):
			_, _ = fmt.Fprintln(stderr, "logs_cancelled")
			return 130
		case err != nil:
			_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
			return 1
		}
		if !more {
			return 0
		}
	}
	return 0
}

// logsTail prints the last lines matching records of the active file and,
// with follow, every record appended later. A file that shrinks or is
// replaced (rotation) is read again from its start.
func logsTail(ctx context.Context, path string, filter *logFilter, lines int, follow, console bool, stdout, stderr io.Writer, deps logsCLIDependencies) int {
	type kept struct {
		clean  []byte
		record map[string]any
	}
	ring := make([]kept, 0, min(lines, 1024))
	start := 0
	reader, err := os.Open(path) //nolint:gosec // G304: the configured log file
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
		return 1
	}
	defer func() { _ = reader.Close() }()
	err = eachLine(ctx, reader, func(line []byte) bool {
		clean, record, ok := filter.match(line)
		if !ok {
			return true
		}
		entry := kept{append([]byte(nil), clean...), record}
		if len(ring) < lines {
			ring = append(ring, entry)
		} else {
			ring[start] = entry
			start = (start + 1) % lines
		}
		return true
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
		return 1
	}
	for i := range ring {
		entry := ring[(start+i)%len(ring)]
		if renderLogRecord(stdout, entry.clean, entry.record, console) != nil {
			_, _ = fmt.Fprintln(stderr, "logs_output_failed")
			return 1
		}
	}
	if !follow {
		return 0
	}
	offset, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "logs_file_unreadable")
		return 1
	}
	identity, _ := reader.Stat()
	var pending []byte
	ticker := time.NewTicker(deps.followInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
		info, err := os.Stat(path) //nolint:gosec // G703: the configured log file the operator asked to follow
		if err == nil && (identity == nil || !os.SameFile(identity, info) || info.Size() < offset) {
			// Rotated or truncated: continue with the new active file.
			if next, openErr := os.Open(path); openErr == nil { //nolint:gosec // G304: the configured log file
				_ = reader.Close()
				reader, offset, pending = next, 0, nil
				identity, _ = next.Stat()
			}
		}
		chunk := make([]byte, 256<<10)
		for {
			n, readErr := reader.Read(chunk)
			if n > 0 {
				offset += int64(n)
				pending = append(pending, chunk[:n]...)
			}
			if readErr != nil || n == 0 {
				break
			}
		}
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				if len(pending) > logsMaxLine {
					pending = nil
				}
				break
			}
			line := pending[:i]
			pending = pending[i+1:]
			if clean, record, ok := filter.match(line); ok {
				if renderLogRecord(stdout, clean, record, console) != nil {
					_, _ = fmt.Fprintln(stderr, "logs_output_failed")
					return 1
				}
			}
		}
	}
}

// logsExport writes matching records as NDJSON, oldest first, up to
// maxBytes; a record that would pass the limit ends the export.
func logsExport(ctx context.Context, path string, files []string, filter *logFilter, out string, maxBytes int64, stdout, stderr io.Writer) int {
	w := stdout
	var file *os.File
	if out != "-" {
		var err error
		file, err = os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the operator names the export file
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "logs_export_unwritable: the output file must not exist yet")
			return 1
		}
		w = file
	}
	buffered := bufio.NewWriterSize(w, 64<<10)
	var written, records int64
	truncated := false
	status := logsScan(ctx, path, files, filter, stderr, func(clean []byte, _ map[string]any) (bool, error) {
		if written+int64(len(clean))+1 > maxBytes {
			truncated = true
			return false, nil
		}
		written += int64(len(clean)) + 1
		records++
		_, err := buffered.Write(append(clean, '\n'))
		return true, err
	})
	flushErr := buffered.Flush()
	if file != nil {
		flushErr = errors.Join(flushErr, file.Sync(), file.Close())
	}
	if status != 0 {
		return status
	}
	if flushErr != nil {
		_, _ = fmt.Fprintln(stderr, "logs_output_failed")
		return 1
	}
	note := ""
	if truncated {
		note = "; truncated at --max-bytes (logs_export_truncated)"
	}
	_, _ = fmt.Fprintf(stderr, "exported %d records, %d bytes%s\n", records, written, note)
	return 0
}

// API commands.

type logsAPIProblem struct {
	status int
	code   string
}

func (p logsAPIProblem) Error() string { return "logs_request_rejected" }

func logsHTTPClient(client *http.Client) (*http.Client, func()) {
	if client != nil {
		return client, func() {}
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}, transport.CloseIdleConnections
}

// logsCall sends one request and decodes data into target.
func logsCall(ctx context.Context, client *http.Client, base, token, method, path string, body any, target any) error {
	u, err := jobsBaseURL(base)
	if err != nil {
		return err
	}
	u.Path = path
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), payload) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request) //nolint:gosec // G704: the operator names the Jelee server URL on the command line
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, logsAPIResponseLimit+1))
	if err != nil || len(data) > logsAPIResponseLimit {
		return errors.New("logs_response_invalid")
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
		return logsAPIProblem{status: response.StatusCode, code: code}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 {
		return errors.New("logs_response_invalid")
	}
	return json.Unmarshal(envelope.Data, target)
}

type logsScopeView struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Mandatory  bool     `json:"mandatory"`
	Configured *string  `json:"configured"`
	Effective  string   `json:"effective"`
	Override   *struct {
		Level     string     `json:"level"`
		ExpiresAt *time.Time `json:"expiresAt"`
	} `json:"override"`
}

type logsLevelsView struct {
	Production         bool            `json:"production"`
	DebugMaxTTLSeconds int64           `json:"debugMaxTtlSeconds"`
	Global             logsScopeView   `json:"global"`
	Components         []logsScopeView `json:"components"`
}

type logsRetentionView struct {
	LogDays       int  `json:"logDays"`
	LogMaxTotalMB int  `json:"logMaxTotalMB"`
	AuditDays     int  `json:"auditDays"`
	SecurityDays  int  `json:"securityDays"`
	FileLogging   bool `json:"fileLogging"`
}

func writeLogLevels(w io.Writer, view logsLevelsView, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(view)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SCOPE\tEFFECTIVE\tCONFIGURED\tOVERRIDE\tEXPIRES\tNOTE")
	for _, s := range append([]logsScopeView{view.Global}, view.Components...) {
		configured, override, expires, note := "-", "-", "-", ""
		if s.Configured != nil {
			configured = *s.Configured
		}
		if s.Override != nil {
			override = s.Override.Level
			if s.Override.ExpiresAt != nil {
				expires = s.Override.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		if s.Mandatory {
			note = "mandatory"
		}
		if len(s.Aliases) > 0 {
			note = strings.TrimSpace(note + " aliases=" + strings.Join(s.Aliases, ","))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Effective, configured, override, expires, note)
	}
	return tw.Flush()
}

func runLogsAPI(ctx context.Context, command string, argv []string, stdin io.Reader, stdout, stderr io.Writer, deps logsCLIDependencies, usage func() int) int {
	flags := flag.NewFlagSet("logs "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("url", "http://127.0.0.1:8097", "service origin")
	fromStdin := flags.Bool("token-stdin", false, "read an administrator bearer token from stdin")
	asJSON := flags.Bool("json", false, "print JSON")
	var component string
	var ttl time.Duration
	var logDays, logMax, auditDays, securityDays int
	switch command {
	case "level":
		flags.StringVar(&component, "component", "global", "scope, alias or global")
		flags.DurationVar(&ttl, "ttl", 0, "override lifetime (0: until reset; production DEBUG defaults to 1h, at most 4h)")
	case "retention":
		flags.IntVar(&logDays, "log-days", -1, "days rotated log files are kept (0: no age limit)")
		flags.IntVar(&logMax, "log-max-total-mb", -1, "MiB all log files may use (0: no cap)")
		flags.IntVar(&auditDays, "audit-days", -1, "audit category retention in days")
		flags.IntVar(&securityDays, "security-days", -1, "security category retention in days")
	}
	if flags.Parse(argv) != nil {
		return usage()
	}
	level := ""
	if command == "level" {
		// LEVEL may come before or after the flags.
		rest := flags.Args()
		if len(rest) == 0 {
			return usage()
		}
		level = rest[0]
		if flags.Parse(rest[1:]) != nil || flags.NArg() != 0 {
			return usage()
		}
		if _, err := logging.ParseLevel(level); err != nil && level != "reset" {
			return usage()
		}
		if ttl < 0 || ttl%time.Second != 0 || level == "reset" && ttl != 0 {
			return usage()
		}
	} else if flags.NArg() != 0 {
		return usage()
	}
	if !*fromStdin {
		_, _ = fmt.Fprintln(stderr, "logs_token_required: "+command+" changes or reads the running servers; pass an administrator token on stdin with --token-stdin")
		return 2
	}
	if _, err := jobsBaseURL(*base); err != nil {
		_, _ = fmt.Fprintln(stderr, "logs_url_invalid: use https, or http to a loopback address")
		return 2
	}
	token, err := readJobsToken(ctx, stdin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "logs_token_invalid")
		return 2
	}
	client, closeClient := logsHTTPClient(deps.client)
	defer closeClient()
	fail := func(err error) int {
		var problem logsAPIProblem
		switch {
		case errors.As(err, &problem):
			_, _ = fmt.Fprintf(stderr, "logs_request_rejected (HTTP %d %s)\n", problem.status, problem.code)
		case ctx.Err() != nil:
			_, _ = fmt.Fprintln(stderr, "logs_cancelled")
			return 130
		default:
			_, _ = fmt.Fprintln(stderr, "logs_service_unavailable")
		}
		return 1
	}
	switch command {
	case "levels", "level":
		var view logsLevelsView
		if command == "level" {
			body := map[string]any{"component": component, "level": level, "ttlSeconds": int64(ttl / time.Second)}
			err = logsCall(ctx, client, *base, token, http.MethodPut, "/api/v1/admin/logging/levels", body, &view)
		} else {
			err = logsCall(ctx, client, *base, token, http.MethodGet, "/api/v1/admin/logging/levels", nil, &view)
		}
		if err != nil {
			return fail(err)
		}
		if writeLogLevels(stdout, view, *asJSON) != nil {
			return 1
		}
		return 0
	}
	var view logsRetentionView
	if err = logsCall(ctx, client, *base, token, http.MethodGet, "/api/v1/admin/logging/retention", nil, &view); err != nil {
		return fail(err)
	}
	if logDays >= 0 || logMax >= 0 || auditDays >= 0 || securityDays >= 0 {
		for _, change := range []struct {
			value  int
			target *int
		}{{logDays, &view.LogDays}, {logMax, &view.LogMaxTotalMB}, {auditDays, &view.AuditDays}, {securityDays, &view.SecurityDays}} {
			if change.value >= 0 {
				*change.target = change.value
			}
		}
		body := map[string]int{"logDays": view.LogDays, "logMaxTotalMB": view.LogMaxTotalMB, "auditDays": view.AuditDays, "securityDays": view.SecurityDays}
		if err = logsCall(ctx, client, *base, token, http.MethodPut, "/api/v1/admin/logging/retention", body, &view); err != nil {
			return fail(err)
		}
	}
	if *asJSON {
		if json.NewEncoder(stdout).Encode(view) != nil {
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "log files: %s, %s (this instance writes a log file: %s)\naudit: %d days, security: %d days\n",
		daysText(view.LogDays), sizeText(view.LogMaxTotalMB), strconv.FormatBool(view.FileLogging), view.AuditDays, view.SecurityDays)
	return 0
}

func daysText(days int) string {
	if days == 0 {
		return "no age limit"
	}
	return strconv.Itoa(days) + " days"
}

func sizeText(mb int) string {
	if mb == 0 {
		return "no size cap"
	}
	return strconv.Itoa(mb) + " MiB in total"
}
