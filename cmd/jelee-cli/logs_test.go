package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

const (
	logsTrace = "0123456789abcdef0123456789abcdef"
	logsOther = "fedcba9876543210fedcba9876543210"
)

func logLine(at time.Time, level, component, msg string, extra string) string {
	line := `{"time":"` + at.UTC().Format(time.RFC3339Nano) + `","level":"` + level + `","msg":"` + msg + `","component":"` + component + `"`
	if extra != "" {
		line += "," + extra
	}
	return line + "}\n"
}

// writeLogFixture writes a gzipped and a plain backup and the active file.
func writeLogFixture(t *testing.T, now time.Time) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jelee.log")
	old := now.Add(-48 * time.Hour)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(logLine(old, "INFO", "http", "request completed", `"traceId":"`+logsOther+`"`) + logLine(old.Add(time.Minute), "WARN", "ignore", "old scan warning", "")))
	_ = zw.Close()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("jelee-"+old.Add(time.Hour).UTC().Format("20060102T150405.000000000Z")+".000.log.gz", gz.String())
	recent := now.Add(-2 * time.Hour)
	write("jelee-"+now.Add(-time.Hour).UTC().Format("20060102T150405.000000000Z")+".000.log",
		logLine(recent, "ERROR", "scan", "scan failed", `"traceId":"`+logsTrace+`","code":"scan_io"`)+
			"not json\n"+
			logLine(recent.Add(time.Minute), "DEBUG", "scan", "scan detail", ""))
	write("jelee.log",
		logLine(now.Add(-30*time.Minute), "INFO", "http", "request completed", `"traceId":"`+logsTrace+`","token":"secret-token-value"`)+
			logLine(now.Add(-20*time.Minute), "WARN", "security", "security event", `"event":"login_failed"`)+
			strings.Repeat("x", logsMaxLine+10)+"\n"+
			logLine(now.Add(-10*time.Minute), "INFO", "jobs", "job started", ""))
	return path
}

func logsDeps(now time.Time, path string) logsCLIDependencies {
	return logsCLIDependencies{now: func() time.Time { return now }, followInterval: 10 * time.Millisecond, load: func() (config.Config, error) {
		cfg := config.Config{Logging: config.DefaultLoggingConfig()}
		cfg.Logging.File.Path = path
		return cfg, nil
	}}
}

func runLogs(t *testing.T, deps logsCLIDependencies, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runLogsCLIWith(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, deps)
	return code, stdout.String(), stderr.String()
}

func messages(t *testing.T, ndjson string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(ndjson), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not NDJSON: %q", line)
		}
		out = append(out, rec["msg"].(string))
	}
	return out
}

func TestLogsFilterReadsBackupsInOrderWithFilters(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	path := writeLogFixture(t, now)
	deps := logsDeps(now, path)
	code, out, errText := runLogs(t, deps, "", "filter")
	if code != 0 {
		t.Fatalf("filter: %d %s", code, errText)
	}
	all := messages(t, out)
	want := []string{"request completed", "old scan warning", "scan failed", "scan detail", "request completed", "security event", "job started"}
	if strings.Join(all, "|") != strings.Join(want, "|") {
		t.Fatalf("order %v", all)
	}
	if strings.Contains(out, "secret-token-value") || !strings.Contains(out, `"token":"[redacted]"`) {
		t.Fatalf("whitelist not applied: %s", out)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--level", "warn"}, "old scan warning|scan failed|security event"},
		{[]string{"--component", "scan"}, "old scan warning|scan failed|scan detail"},
		{[]string{"--component", "ignore", "--level", "error"}, "scan failed"},
		{[]string{"--since", "3h"}, "scan failed|scan detail|request completed|security event|job started"},
		{[]string{"--since", "3h", "--until", now.Add(-25 * time.Minute).Format(time.RFC3339)}, "scan failed|scan detail|request completed"},
		{[]string{"--trace", logsTrace}, "scan failed|request completed"},
		{[]string{"--component", "devmode"}, "security event"},
		{[]string{"--limit", "2"}, "request completed|old scan warning"},
	} {
		code, out, errText := runLogs(t, deps, "", append([]string{"filter"}, tc.args...)...)
		if code != 0 || strings.Join(messages(t, out), "|") != tc.want {
			t.Errorf("filter %v: %d %q %s", tc.args, code, messages(t, out), errText)
		}
	}
	code, out, _ = runLogs(t, deps, "", "filter", "--format", "console", "--level", "error")
	if code != 0 || !strings.Contains(out, "ERROR scan scan failed") || !strings.Contains(out, `code="scan_io"`) {
		t.Fatalf("console: %s", out)
	}
	for _, args := range [][]string{{"filter", "--level", "loud"}, {"filter", "--trace", "XYZ"}, {"filter", "--since", "yesterday"}, {"filter", "--format", "xml"}, {"filter", "--limit", "0"}, {"filter", "stray"}, {"tail", "--lines", "0"}, {"export"}, {"nothing"}} {
		if code, _, _ := runLogs(t, deps, "", args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if code, _, errText := runLogs(t, deps, "", "filter", "--component", "nowhere"); code != 2 || !strings.Contains(errText, "logs_component_unknown") {
		t.Fatalf("unknown component: %d %s", code, errText)
	}
	none := logsDeps(now, "")
	if code, _, errText := runLogs(t, none, "", "filter"); code != 1 || !strings.Contains(errText, "logs_no_file_configured") {
		t.Fatalf("no file: %d %s", code, errText)
	}
}

func TestLogsExportIsBoundedNDJSON(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	path := writeLogFixture(t, now)
	deps := logsDeps(now, path)
	out := filepath.Join(t.TempDir(), "export.ndjson")
	code, _, errText := runLogs(t, deps, "", "export", "--out", out, "--level", "info")
	if code != 0 || !strings.Contains(errText, "exported 6 records") {
		t.Fatalf("export: %d %s", code, errText)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(messages(t, string(data)), "|"); got != "request completed|old scan warning|scan failed|request completed|security event|job started" {
		t.Fatalf("exported %s", got)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode %v", info.Mode())
	}
	if code, _, errText := runLogs(t, deps, "", "export", "--out", out); code != 1 || !strings.Contains(errText, "logs_export_unwritable") {
		t.Fatalf("overwrite: %d %s", code, errText)
	}
	code, stdout, errText := runLogs(t, deps, "", "export", "--out", "-", "--max-bytes", "300")
	if code != 0 || len(stdout) > 300 || !strings.Contains(errText, "logs_export_truncated") || len(messages(t, stdout)) == 0 {
		t.Fatalf("bounded export: %d %d %s", code, len(stdout), errText)
	}
}

// syncWriter is a goroutine-safe stdout for tail --follow.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestLogsTailAndFollowAcrossRotation(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	path := writeLogFixture(t, now)
	deps := logsDeps(now, path)
	code, out, _ := runLogs(t, deps, "", "tail", "--lines", "2")
	if code != 0 || strings.Join(messages(t, out), "|") != "security event|job started" {
		t.Fatalf("tail: %d %s", code, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stdout := &syncWriter{}
	done := make(chan int)
	go func() {
		done <- runLogsCLIWith(ctx, []string{"tail", "--lines", "1", "--follow", "--level", "warn"}, nil, stdout, io.Discard, deps)
	}()
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(stdout.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("missing %q in %s", want, stdout.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor("security event")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(logLine(now, "INFO", "http", "appended info", "") + logLine(now, "ERROR", "db", "appended error", ""))
	_ = f.Close()
	waitFor("appended error")
	// Rotation: the active file is renamed and a new one starts.
	if err := os.Rename(path, path+".rotated"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(logLine(now, "WARN", "gc", "after rotation", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor("after rotation")
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("follow exit %d", code)
	}
	if strings.Contains(stdout.String(), "appended info") {
		t.Fatal("follow ignored the level filter")
	}
}

// fakeLogsServer answers the administration routes like the server does.
type fakeLogsServer struct {
	mu        sync.Mutex
	requests  []string
	bodies    []map[string]any
	retention map[string]int
}

func (f *fakeLogsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.bodies = append(f.bodies, body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/v1/admin/logging/levels" && r.Method == http.MethodPut && body["component"] == "audit":
		w.WriteHeader(409)
		_, _ = io.WriteString(w, `{"error":{"code":"log_component_mandatory","message":"x","details":{},"traceId":"t"}}`)
	case r.URL.Path == "/api/v1/admin/logging/levels":
		_, _ = io.WriteString(w, `{"data":{"production":true,"debugMaxTtlSeconds":14400,"global":{"name":"global","aliases":[],"mandatory":false,"configured":"info","effective":"debug","override":{"level":"debug","expiresAt":"2026-10-06T13:00:00Z"}},"components":[{"name":"scan","aliases":["ignore"],"mandatory":false,"configured":null,"effective":"debug","override":null},{"name":"audit","aliases":[],"mandatory":true,"configured":null,"effective":"info","override":null}]}}`)
	case r.URL.Path == "/api/v1/admin/logging/retention":
		if r.Method == http.MethodPut {
			for key, value := range body {
				f.retention[key] = int(value.(float64))
			}
		}
		data, _ := json.Marshal(map[string]any{"data": map[string]any{"logDays": f.retention["logDays"], "logMaxTotalMB": f.retention["logMaxTotalMB"], "auditDays": f.retention["auditDays"], "securityDays": f.retention["securityDays"], "fileLogging": true}})
		_, _ = w.Write(data)
	default:
		w.WriteHeader(404)
	}
}

func TestLogsAPICommands(t *testing.T) {
	fake := &fakeLogsServer{retention: map[string]int{"logDays": 30, "logMaxTotalMB": 2048, "auditDays": 365, "securityDays": 365}}
	server := httptest.NewServer(fake)
	defer server.Close()
	deps := logsCLIDependencies{load: func() (config.Config, error) { return config.Config{}, nil }, client: server.Client()}
	token := strings.Repeat("t", 43) + "\n"
	code, out, errText := runLogs(t, deps, token, "level", "--component", "ignore", "debug", "--ttl", "30m", "--token-stdin", "--url", server.URL)
	if code != 0 || !strings.Contains(out, "scan") || !strings.Contains(out, "aliases=ignore") || !strings.Contains(out, "mandatory") {
		t.Fatalf("level: %d %s %s", code, out, errText)
	}
	if fake.requests[0] != "PUT /api/v1/admin/logging/levels Bearer "+strings.Repeat("t", 43) || fake.bodies[0]["component"] != "ignore" || fake.bodies[0]["level"] != "debug" || fake.bodies[0]["ttlSeconds"] != float64(1800) {
		t.Fatalf("request %v %v", fake.requests, fake.bodies)
	}
	if code, _, errText := runLogs(t, deps, token, "level", "--component", "audit", "error", "--token-stdin", "--url", server.URL); code != 1 || !strings.Contains(errText, "HTTP 409 log_component_mandatory") {
		t.Fatalf("mandatory: %d %s", code, errText)
	}
	if code, out, _ := runLogs(t, deps, token, "levels", "--json", "--token-stdin", "--url", server.URL); code != 0 || !strings.Contains(out, `"debugMaxTtlSeconds":14400`) {
		t.Fatalf("levels: %d %s", code, out)
	}
	code, out, _ = runLogs(t, deps, token, "retention", "--log-days", "7", "--security-days", "90", "--token-stdin", "--url", server.URL)
	if code != 0 || !strings.Contains(out, "7 days") || !strings.Contains(out, "security: 90 days") || fake.retention["auditDays"] != 365 || fake.retention["logMaxTotalMB"] != 2048 {
		t.Fatalf("retention: %d %s %v", code, out, fake.retention)
	}
	for _, args := range [][]string{
		{"level", "debug", "--url", server.URL},
		{"level", "loud", "--token-stdin", "--url", server.URL},
		{"level", "reset", "--ttl", "1m", "--token-stdin", "--url", server.URL},
		{"level", "--token-stdin", "--url", server.URL},
		{"level", "debug", "--token-stdin", "--url", "http://example.com"},
		{"levels", "extra", "--token-stdin", "--url", server.URL},
	} {
		if code, _, _ := runLogs(t, deps, token, args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	if code, _, errText := runLogs(t, deps, "short\n", "levels", "--token-stdin", "--url", server.URL); code != 2 || !strings.Contains(errText, "logs_token_invalid") {
		t.Fatalf("token: %d %s", code, errText)
	}
}
