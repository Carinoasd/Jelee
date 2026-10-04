package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	logger.Info("request completed", "Authorization", "secret-token", "password", "secret-password", "path", "C:/private/media.mkv", "database", "postgres://user:secret@localhost/db", "requestId", "1234")
	for _, secret := range []string{"secret-token", "secret-password", "C:/private", "postgres://"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if !strings.Contains(buf.String(), "1234") {
		t.Fatal("request correlation lost")
	}
}

func TestSafeMethodAllowsOnlyKnownVerbs(t *testing.T) {
	for _, method := range []string{"GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		for _, input := range []string{method, strings.ToLower(method)} {
			if got := SafeMethod(input); got != method {
				t.Errorf("SafeMethod(%q)=%q want=%q", input, got, method)
			}
		}
	}
	for _, method := range []string{"", "TRACE", "CONNECT", "PROPFIND", "CUSTOM", "GET ", " GET", "GET\r\nAuthorization: secret", "GET\x00", "post?api_key=secret", "ＰＯＳＴ", strings.Repeat("secret-token", 1000)} {
		if got := SafeMethod(method); got != "OTHER" {
			t.Errorf("unsupported request method reached logs: %q", got)
		}
	}
}

func TestOperationalContextSurvivesRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output).With("component", "http", "requestId", "test-request", "authorization", "hidden-credential")
	logger.InfoContext(context.Background(), "request completed",
		"method", SafeMethod("get"), "status", 206, "durationMs", 12, "event", "direct_delivery", "count", 3,
		"error", "private database structure", "path", "/private/library/original.mkv")
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"component": "http", "requestId": "test-request", "method": "GET", "status": float64(206), "durationMs": float64(12), "event": "direct_delivery", "count": float64(3), "level": "INFO", "msg": "request completed"} {
		if entry[key] != want {
			t.Errorf("operational field %s=%v want=%v", key, entry[key], want)
		}
	}
	if timestamp, ok := entry["time"].(string); !ok || timestamp == "" {
		t.Fatal("log timestamp lost")
	}
	for _, key := range []string{"authorization", "error", "path"} {
		if entry[key] != "[redacted]" {
			t.Errorf("unsafe field %s was not redacted: %v", key, entry[key])
		}
	}
	for _, secret := range []string{"hidden-credential", "private database structure", "/private/library"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("scoped attribute leaked: %s", secret)
		}
	}
}

func TestGroupedAttributesCannotExposeSensitiveValues(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output).WithGroup("request").With("requestId", "group-request")
	logger.LogAttrs(context.Background(), slog.LevelWarn, "request rejected",
		slog.String("method", SafeMethod("GET\r\nsecret-token")),
		slog.Any("credentials", map[string]string{"token": "map-secret", "password": "map-password"}),
		slog.Group("diagnostics", slog.String("connection", "postgres://db-secret"), slog.Int("count", 2)))
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	request, ok := entry["request"].(map[string]any)
	if !ok {
		t.Fatal("scoped log group lost")
	}
	if request["requestId"] != "group-request" || request["method"] != "OTHER" || request["credentials"] != "[redacted]" {
		t.Fatalf("unsafe or incomplete request group: %v", request)
	}
	diagnostics, ok := request["diagnostics"].(map[string]any)
	if !ok || diagnostics["connection"] != "[redacted]" || diagnostics["count"] != float64(2) {
		t.Fatalf("nested field filtering failed: %v", request["diagnostics"])
	}
	for _, secret := range []string{"secret-token", "map-secret", "map-password", "db-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("grouped field leaked: %s", secret)
		}
	}
}

func TestJobCorrelationOnlyAcceptsValidatedFields(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out)
	id := "11111111-1111-4111-8111-111111111111"
	logger.Info("inventory job completed", "taskId", id, "state", "succeeded", "code", "scan_io")
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), "succeeded") {
		t.Fatal("valid correlation removed")
	}
	out.Reset()
	logger.Info("job state could not be persisted", "taskId", "secret-token", "state", "/private/path", "code", "password=secret")
	for _, secret := range []string{"secret-token", "/private/path", "password=secret"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("unvalidated job log field leaked")
		}
	}
}

func TestRedactorRenderPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	relative := NewRedactor(IPRedact, PathRelative, []string{root})
	if got := relative.RenderPath(root, "Movie/a.mkv"); got != "Movie/a.mkv" {
		t.Fatalf("relative mode rendered %q", got)
	}
	if got := relative.RenderPath(filepath.Join(t.TempDir(), "other"), "a.mkv"); got != Redacted {
		t.Fatalf("a path outside the logging roots rendered %q", got)
	}
	for _, args := range [][2]string{{"", "a.mkv"}, {root, ""}, {"relative", "a.mkv"}} {
		if got := relative.RenderPath(args[0], args[1]); got != Redacted {
			t.Fatalf("invalid input %v rendered %q", args, got)
		}
	}
	if got := NewRedactor(IPRedact, PathRedact, []string{root}).RenderPath(root, "Movie/a.mkv"); got != Redacted {
		t.Fatalf("redact mode rendered %q", got)
	}
}
