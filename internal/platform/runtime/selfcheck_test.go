package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// selfCheckHandler answers like an assembled server: protected routes
// refuse anonymous callers and transformation routes are disabled. Each
// override replaces the status of one path.
func selfCheckHandler(overrides map[string]int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RemoteAddr != "127.0.0.1:1" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		if status, ok := overrides[r.URL.Path]; ok {
			if status == http.StatusOK {
				_, _ = w.Write([]byte("{}"))
				return
			}
			w.WriteHeader(status)
			return
		}
		switch {
		case r.URL.Path == "/transcode" || strings.Contains(r.URL.Path, "/hls/"):
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
}

func selfCheckConfig() config.Config {
	cfg := config.Config{EnableAccounts: true, EnableCatalog: true, EnableJobs: true, AllowedHosts: []string{"::1"}}
	return cfg
}

func TestSelfCheckPassesAnAssembledServer(t *testing.T) {
	var logs bytes.Buffer
	report, err := runSelfCheck(selfCheckHandler(nil), selfCheckEnvironment{config: selfCheckConfig(), lookPath: func(string) bool { return false }}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil || report.status() != selfCheckOK || len(report) != 4 {
		t.Fatalf("report %+v: %v", report, err)
	}
	for _, code := range []string{"access_filter_assembled", "transcode_guard_active", "encoder_not_found", "dev_disabled"} {
		if !strings.Contains(logs.String(), code) {
			t.Fatalf("log misses %s: %s", code, logs.String())
		}
	}
}

func TestSelfCheckRefusesAMissingGate(t *testing.T) {
	for _, c := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/v1/items", http.StatusOK, "access_filter_missing"},
		{"/api/v1/users/me", http.StatusNotFound, "access_filter_missing"},
		{"/api/v1/jobs", http.StatusFound, "access_filter_missing"},
		{"/transcode", http.StatusNotFound, "transcode_guard_missing"},
	} {
		report, err := runSelfCheck(selfCheckHandler(map[string]int{c.path: c.status}), selfCheckEnvironment{config: selfCheckConfig(), lookPath: func(string) bool { return false }}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), c.code) || report.status() != selfCheckFail {
			t.Fatalf("%s %d: %+v %v", c.path, c.status, report, err)
		}
	}
	// Before initial setup protected routes answer 503 setup_required.
	if _, err := runSelfCheck(selfCheckHandler(map[string]int{"/api/v1/items": http.StatusServiceUnavailable, "/api/v1/users/me": http.StatusForbidden}), selfCheckEnvironment{config: selfCheckConfig(), lookPath: func(string) bool { return false }}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
}

func TestSelfCheckWarnsWithoutRefusing(t *testing.T) {
	cfg := config.Config{AllowedHosts: []string{"localhost"}}
	cfg.Dev.Enabled = true
	report, err := runSelfCheck(selfCheckHandler(nil), selfCheckEnvironment{config: cfg, lookPath: func(name string) bool { return name == "ffmpeg" }}, slog.New(slog.DiscardHandler))
	if err != nil || report.status() != selfCheckWarn {
		t.Fatalf("report %+v: %v", report, err)
	}
	codes := map[string]string{}
	for _, c := range report {
		codes[c.Name] = c.Code
	}
	if codes["access_filter"] != "access_no_protected_routes" || codes["encoder_absent"] != "encoder_on_path" {
		t.Fatalf("codes %v", codes)
	}
	if cfg.Dev.Capable() != (codes["dev_mode"] == "dev_capable") {
		t.Fatalf("dev mode code %q for capable=%t", codes["dev_mode"], cfg.Dev.Capable())
	}
	var state *selfCheckState
	if state.status() != "pending" || (&selfCheckState{report: report}).status() != selfCheckWarn {
		t.Fatal("state status")
	}
}

func TestExecutableOnPathReadsMetadataOnly(t *testing.T) {
	directory := t.TempDir()
	name := "jelee-selfcheck-probe"
	file := name
	if goruntime.GOOS == "windows" {
		file += ".exe"
	}
	t.Setenv("PATH", strings.Join([]string{"relative", directory}, string(os.PathListSeparator)))
	if executableOnPath(name) {
		t.Fatal("found a missing executable")
	}
	if err := os.WriteFile(filepath.Join(directory, file), []byte("not run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if goruntime.GOOS != "windows" && executableOnPath(name) {
		t.Fatal("a non-executable file counted")
	}
	if err := os.Chmod(filepath.Join(directory, file), 0o700); err != nil {
		t.Fatal(err)
	}
	if !executableOnPath(name) {
		t.Fatal("executable not found")
	}
}

type readinessFake map[string]string

func (f readinessFake) Readiness(context.Context) map[string]string {
	out := map[string]string{}
	for k, v := range f {
		out[k] = v
	}
	return out
}

func TestReadinessStatesAreFixedCodes(t *testing.T) {
	states := readinessStates(config.Config{}, readinessFake{"database": "ok", "schema": "current", "jobs": "idle"}, nil, nil, nil, &selfCheckState{report: selfCheckReport{{Status: selfCheckOK}}})(context.Background())
	want := map[string]string{"database": "ok", "schema": "current", "jobs": "disabled", "probe": "disabled", "images": "disabled", "devMode": "off", "startup": "ok"}
	for k, v := range want {
		if states[k] != v {
			t.Fatalf("%s=%q, want %q (%v)", k, states[k], v, states)
		}
	}
	states = readinessStates(config.Config{EnableImages: true}, readinessFake{}, nil, nil, nil, &selfCheckState{})(context.Background())
	if states["images"] != "no_store" || states["startup"] != "pending" {
		t.Fatalf("states %v", states)
	}
}
