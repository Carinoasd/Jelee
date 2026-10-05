package logging

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestOfflineLineRedactsEverySample feeds raw, never-redacted records (as an
// old or foreign writer might have left them) through the offline redactor.
func TestOfflineLineRedactsEverySample(t *testing.T) {
	red := NewRedactor(IPRedact, PathRedact, nil)
	for _, sample := range sensitiveSamples {
		record := map[string]any{"time": "2026-10-04T12:00:00Z", "level": "INFO", "msg": sample.value, "nested": map[string]any{"deeper": map[string]any{"token": sample.value}}, "list": []any{sample.value}}
		for _, key := range scanKeys {
			record[key] = sample.value
		}
		record[sample.value] = 1
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		out, ok := red.Line(line)
		if !ok {
			t.Fatalf("%s: line rejected", sample.name)
		}
		for _, needle := range append(append([]string(nil), sample.needles...), defaultOnlyNeedles...) {
			if strings.Contains(string(out), needle) {
				t.Fatalf("%s leaked %q: %s", sample.name, needle, out)
			}
		}
	}
}

func TestOfflineLineKeepsWhitelistedFields(t *testing.T) {
	red := NewRedactor(IPMask, PathRelative, []string{testRoot()})
	line := `{"time":"2026-10-04T12:00:00.5Z","level":"WARN","msg":"request done","component":"http","status":503,"durationMs":12,"clientIp":"203.0.113.77","path":` + quote(filepath.Join(testRoot(), "Movies", "a.mkv")) + `,"state":"failed","flag":true}`
	out, ok := red.Line([]byte(line))
	if !ok {
		t.Fatal("line rejected")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"time": "2026-10-04T12:00:00.5Z", "level": "WARN", "msg": "request done", "component": "http", "status": float64(503), "durationMs": float64(12), "clientIp": "203.0.113.0/24", "path": "Movies/a.mkv", "state": "failed", "flag": Redacted}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %#v want %#v (%s)", key, got[key], value, out)
		}
	}
	for _, bad := range []string{"", "not json", `{"a":1} {"b":2}`, `[1,2]`, `null`} {
		if _, ok := red.Line([]byte(bad)); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if red.Path(filepath.Join(testRoot(), "x")) != "x" || red.Path("/elsewhere/x") != Redacted {
		t.Fatal("path mapping")
	}
	if NewRedactor("raw", "absolute", nil).Path(filepath.Join(testRoot(), "x")) != Redacted {
		t.Fatal("unknown mode is not full redaction")
	}
	if a := NewRedactor(IPRedact, PathRedact, nil).Attr(slog.String("token", "x")); a.Value.String() != Redacted {
		t.Fatal("attr whitelist")
	}
}

func testRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\media`
	}
	return "/srv/media"
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
