package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func records(t *testing.T, text string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		out = append(out, rec)
	}
	return out
}

// Every component name a call site uses must be a scope or an alias, or
// the whitelist writes the component (and its level filter) away.
func TestEveryCallSiteComponentIsKnown(t *testing.T) {
	want := map[string]string{
		"http": "http", "jobs": "jobs", "db": "db", "gc": "gc", "scan": "scan", "probe": "probe", "nfo": "nfo", "images": "images", "webhook": "webhook",
		"devmode": "security", "selfcheck": "system", "watch_stats": "media", "playback": "media", "subtitle_ocr": "media", "matroska": "media",
		"webhooks": "webhook", "client_control": "access", "share": "access", "accounts": "auth", "setup": "auth", "metrics": "db", "ignore": "scan",
		"security": "security", "audit": "audit", "system": "system",
	}
	for component, scope := range want {
		got, ok := ScopeFor(component)
		if !ok || got != scope {
			t.Errorf("ScopeFor(%q) = %q, %v; want %q", component, got, ok, scope)
		}
	}
	if _, ok := ScopeFor("elsewhere"); ok {
		t.Fatal("unknown component accepted")
	}
	seen := map[string]bool{}
	for _, info := range ComponentTable() {
		if seen[info.Name] {
			t.Fatalf("duplicate scope %s", info.Name)
		}
		seen[info.Name] = true
		if info.Mandatory != (info.Name == "audit" || info.Name == "security") {
			t.Errorf("%s mandatory=%v", info.Name, info.Mandatory)
		}
	}
	for _, scope := range []string{"http", "auth", "access", "scan", "probe", "nfo", "images", "jobs", "webhook", "compat", "media", "db", "gc"} {
		if !seen[scope] {
			t.Errorf("G46.2 scope %s missing", scope)
		}
	}
}

// The fields of the developer mode controller, the startup self-check,
// statistics and client control records pass the whitelist (they used to
// be written as [redacted]); malformed values under the same keys do not.
func TestComponentFieldsPassTheWhitelist(t *testing.T) {
	r, out := openTest(t, Options{})
	log := r.Logger()
	log.Warn("developer mode event", "component", "devmode", "code", "devmode_toggle_changed", "source", "api", "reason", "disabled by an administrator",
		"toggle", "debug_sql_logging", "value", true, "missing", "env_flag,config_flag", "restored", "relax_host_strict", "expiresAt", "2026-10-06T12:00:00Z",
		"toggles", "debug_pprof,relax_host_strict", "ttl", "1h0m0s", "persistAcrossRestart", false)
	log.Warn("developer mode changed the global log level", "component", "devmode", "logLevel", "debug")
	log.Info("startup self-check", "component", "selfcheck", "check", "access_filter", "status", "ok", "code", "access_filter_assembled")
	log.Warn("watch statistics aggregation failed; retrying next interval", "component", "watch_stats")
	log.Warn("client requests blocked", "component", "client_control", "code", "client_blocked", "count", 3,
		"rules", "00000000-0000-4000-8000-000000000001=2,default=1", "rule", "00000000-0000-4000-8000-000000000001")
	log.Info("subtitle OCR job finished", "component", "subtitle_ocr", "tracks", 1, "pictures", 2, "cues", 3, "durationMs", 4)
	recs := records(t, flushed(t, r, out))
	if len(recs) != 6 {
		t.Fatalf("records: %v", recs)
	}
	for _, rec := range recs {
		for key, value := range rec {
			if value == Redacted {
				t.Errorf("%s: %s redacted", rec["msg"], key)
			}
		}
	}
	if recs[0]["component"] != "devmode" || recs[0]["reason"] != "disabled by an administrator" || recs[0]["toggles"] != "debug_pprof,relax_host_strict" || recs[2]["status"] != "ok" {
		t.Fatalf("fields changed: %v", recs)
	}
	log.Warn("bad values", "component", "devmode", "reason", "token=Abc123", "toggles", "A,B", "expiresAt", "tomorrow", "ttl", "forever",
		"rules", "x=1", "rule", "not-a-uuid", "status", "Hunter2!", "code", "password=secret", "logLevel", "verbose", "check", "Check1")
	rec := records(t, flushed(t, r, out))[0]
	for _, key := range []string{"reason", "toggles", "expiresAt", "ttl", "rules", "rule", "status", "code", "logLevel", "check"} {
		if rec[key] != Redacted {
			t.Errorf("%s = %v, want redacted", key, rec[key])
		}
	}
}

// G46.10: the audit and security scopes cannot be lowered or switched off,
// by configuration or at runtime, and their records pass at INFO whatever
// the global level.
func TestMandatoryComponentsCannotBeLowered(t *testing.T) {
	if _, err := Open(Options{Components: map[string]slog.Level{"audit": slog.LevelError}}, io.Discard); !errors.Is(err, ErrMandatoryComponent) {
		t.Fatalf("Open with audit=error: %v", err)
	}
	r, out := openTest(t, Options{Level: slog.LevelError})
	for _, scope := range []string{"audit", "security", "devmode"} {
		if err := r.SetLevel(scope, slog.LevelError); !errors.Is(err, ErrMandatoryComponent) {
			t.Errorf("SetLevel(%s): %v", scope, err)
		}
		if err := r.ResetLevel(scope); !errors.Is(err, ErrMandatoryComponent) {
			t.Errorf("ResetLevel(%s): %v", scope, err)
		}
		if err := r.SetOverrides([]LevelOverride{{Component: scope, Level: slog.LevelError}}); !errors.Is(err, ErrMandatoryComponent) {
			t.Errorf("SetOverrides(%s): %v", scope, err)
		}
	}
	log := r.Logger()
	log.Info("security event", "component", "security", "event", "login_failed")
	log.Info("audit event", "component", "audit", "event", "log_level_changed")
	log.Warn("developer mode event", "component", "devmode")
	log.Info("ordinary info dropped", "component", "http")
	got := flushed(t, r, out)
	for _, kept := range []string{"security event", "audit event", "developer mode event"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s dropped at global ERROR: %s", kept, got)
		}
	}
	if strings.Contains(got, "ordinary info dropped") {
		t.Fatal("global level ignored")
	}
	// A record-level debug stays filtered unless the global level allows it.
	log.Debug("security debug", "component", "security")
	if got := flushed(t, r, out); strings.Contains(got, "security debug") {
		t.Fatal("mandatory scope opened DEBUG")
	}
	// ApplySettings skips a stored mandatory override instead of failing.
	r.ApplySettings(domain.LogSettings{Overrides: []domain.LogLevelOverride{{Component: "audit", Level: "error"}, {Component: "http", Level: "debug"}}})
	if r.Level("audit") != slog.LevelInfo || r.Level("http") != slog.LevelDebug {
		t.Fatalf("audit=%v http=%v", r.Level("audit"), r.Level("http"))
	}
}

// G46.2: administrator overrides win over the configured level and the
// developer verbose switch, expire on their own, and reset cleanly.
func TestLevelOverridesLayerAndExpire(t *testing.T) {
	r, out := openTest(t, Options{Level: slog.LevelWarn, Components: map[string]slog.Level{"scan": slog.LevelError}})
	if !r.SetDeveloperVerbose(true) || r.Level(GlobalComponent) != slog.LevelDebug || r.SetDeveloperVerbose(true) {
		t.Fatal("developer verbose switch")
	}
	expires := time.Now().Add(150 * time.Millisecond)
	if err := r.SetOverrides([]LevelOverride{{Component: GlobalComponent, Level: slog.LevelError}, {Component: "ignore", Level: slog.LevelDebug, ExpiresAt: expires}}); err != nil {
		t.Fatal(err)
	}
	if r.Level(GlobalComponent) != slog.LevelError || r.Level("scan") != slog.LevelDebug {
		t.Fatalf("overrides not applied: global=%v scan=%v", r.Level(GlobalComponent), r.Level("scan"))
	}
	r.Logger().Debug("scan debug while overridden", "component", "scan")
	if got := flushed(t, r, out); !strings.Contains(got, "scan debug while overridden") {
		t.Fatalf("override did not open scan DEBUG: %s", got)
	}
	report := r.LevelReport()
	if report[0].Component != GlobalComponent || report[0].Override == nil || report[0].Configured != slog.LevelWarn {
		t.Fatalf("global report %+v", report[0])
	}
	for _, s := range report {
		if s.Component == "scan" && (s.Override == nil || !s.ConfiguredSet || s.Configured != slog.LevelError || s.Effective != slog.LevelDebug || !slicesContain(s.Aliases, "ignore")) {
			t.Fatalf("scan report %+v", s)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.Level("scan") != slog.LevelError {
		if time.Now().After(deadline) {
			t.Fatal("override did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Logger().Debug("scan debug after expiry", "component", "scan")
	if got := flushed(t, r, out); strings.Contains(got, "scan debug after expiry") {
		t.Fatal("expired override still applied")
	}
	if err := r.SetOverrides(nil); err != nil || r.Level(GlobalComponent) != slog.LevelDebug {
		t.Fatalf("reset: global=%v err=%v", r.Level(GlobalComponent), err)
	}
	r.SetDeveloperVerbose(false)
	if r.Level(GlobalComponent) != slog.LevelWarn {
		t.Fatal("configured level not restored")
	}
	if err := r.SetOverrides([]LevelOverride{{Component: "nowhere", Level: slog.LevelDebug}}); !errors.Is(err, ErrUnknownComponent) {
		t.Fatalf("unknown scope: %v", err)
	}
}

func slicesContain(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// G46.4: fields attached to a context reach every record logged with it or
// with a context derived from it, also from other goroutines; explicit
// attributes win; malformed identifiers are redacted.
func TestContextFieldsAreInjectedAcrossGoroutines(t *testing.T) {
	r, out := openTest(t, Options{})
	user, item, job := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"
	ctx := domain.WithLogFields(context.Background(), domain.LogFields{UserID: user, ClientID: user, DeviceID: "device one"})
	ctx = domain.WithLogFields(ctx, domain.LogFields{ItemID: item, TaskID: job, JobRunID: job + ":7"})
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.Logger().InfoContext(derived, "worker record", "component", "jobs")
	}()
	wg.Wait()
	r.Logger().InfoContext(ctx, "explicit wins", "component", "http", "itemId", job)
	r.Logger().InfoContext(domain.WithLogFields(context.Background(), domain.LogFields{UserID: "not-a-uuid"}), "malformed", "component", "http")
	r.Logger().Info("no context fields", "component", "http")
	recs := records(t, flushed(t, r, out))
	if len(recs) != 4 {
		t.Fatalf("records %v", recs)
	}
	worker := recs[0]
	if worker["userId"] != user || worker["clientId"] != user || worker["itemId"] != item || worker["taskId"] != job || worker["jobRunId"] != job+":7" ||
		worker["deviceId"] != DeviceDigest("device one") || strings.Contains(flushed(t, r, out), "device one") {
		t.Fatalf("worker record %v", worker)
	}
	if recs[1]["itemId"] != job {
		t.Fatalf("explicit attribute lost: %v", recs[1])
	}
	if recs[2]["userId"] != Redacted {
		t.Fatalf("malformed field kept: %v", recs[2])
	}
	if _, ok := recs[3]["userId"]; ok {
		t.Fatalf("fields without context: %v", recs[3])
	}
}

// G46.3: the slow query log writes a statement template; literals and
// secrets never reach the output, and only the marker passes.
func TestSQLTemplate(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT id FROM users WHERE name = $1 AND age > 30":                  "SELECT id FROM users WHERE name = $1 AND age > ?",
		"UPDATE x SET a='secret value', b=E'esc\\'' WHERE id=$2":             "UPDATE x SET a=?, b=? WHERE id=$2",
		"SELECT $$dollar quoted$$, $tag$ also $tag$ FROM t LIMIT 10":         "SELECT ?, ? FROM t LIMIT ?",
		"  SELECT\n\t1.5e3,  col2   FROM t2 ":                                "SELECT ?, col2 FROM t2",
		"ALTER ROLE r PASSWORD 'hunter2'; SET x.dsn = 'postgres://u:p@h/db'": "ALTER ROLE r PASSWORD ?; SET x.dsn = ?",
	} {
		if got := NormalizeSQL(in); got != want {
			t.Errorf("NormalizeSQL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeSQL(strings.Repeat("SELECT a FROM b; ", 100)); len(got) > sqlTemplateMax+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("not bounded: %d", len(got))
	}
	r, out := openTest(t, Options{})
	r.Logger().Warn("slow database query", "component", "db", "code", "db_slow_query", "sql", SQLTemplate("SELECT * FROM sessions WHERE token_hash='deadbeef' AND user_id=$1"),
		"durationMs", 812, "thresholdMs", 500, "count", 1, "outcome", "ok")
	r.Logger().Warn("plain string refused", "component", "db", "sql", "SELECT 'deadbeef'")
	recs := records(t, flushed(t, r, out))
	if recs[0]["sql"] != "SELECT * FROM sessions WHERE token_hash=? AND user_id=$1" || recs[0]["code"] != "db_slow_query" || recs[0]["thresholdMs"] != float64(500) {
		t.Fatalf("slow query record %v", recs[0])
	}
	if recs[1]["sql"] != Redacted {
		t.Fatalf("unmarked SQL logged: %v", recs[1])
	}
}

// G46.3: a panic stack is archived with function names and module-relative
// file positions only: no argument values, no absolute directories.
func TestPanicStackIsMasked(t *testing.T) {
	var stack []byte
	func() {
		defer func() {
			if recover() != nil {
				stack = debug.Stack()
			}
		}()
		panicWith("secret-panic-value")
	}()
	masked := MaskStack(string(stack))
	wd, _ := os.Getwd()
	if !strings.Contains(masked, "logging.panicWith(...)") || !strings.Contains(masked, "internal/platform/logging/g46_test.go:") {
		t.Fatalf("frames missing:\n%s", masked)
	}
	for _, leak := range []string{wd, "secret-panic-value", "0x", "goroutine "} {
		if strings.Contains(masked, leak) {
			t.Fatalf("stack leaked %q:\n%s", leak, masked)
		}
	}
	if got := MaskStack(strings.Repeat("pkg.f(0x1)\n\t/abs/dir/internal/x.go:1 +0x2\n", 500)); len(got) > stackMaxBytes+8 {
		t.Fatalf("stack not bounded: %d", len(got))
	}
	r, out := openTest(t, Options{})
	r.Logger().Error("request panic", "component", "http", "panicType", "*errors.errorString", "stack", PanicStack(stack))
	r.Logger().Error("request panic", "component", "http", "panicType", "jk_live_8f3a9c2e", "stack", string(stack))
	recs := records(t, flushed(t, r, out))
	if recs[0]["stack"] != masked || recs[0]["panicType"] != "*errors.errorString" || recs[1]["stack"] != Redacted || recs[1]["panicType"] != Redacted {
		t.Fatalf("panic records %v", recs)
	}
}

//go:noinline
func panicWith(value string) { panic(errors.New(value)) }

// G46.3: the access log's route passes only for registered patterns.
func TestRouteFieldNeedsARegisteredPattern(t *testing.T) {
	RegisterRoutePatterns("/api/v1/items/{id}", "not a pattern", "relative/x")
	if !KnownRoute("/api/v1/items/{id}") || !KnownRoute("unmatched") || KnownRoute("not a pattern") || KnownRoute("relative/x") || KnownRoute("/api/v1/items/00000000-0000-4000-8000-000000000001") {
		t.Fatal("route registry")
	}
	r, out := openTest(t, Options{})
	r.Logger().Info("request completed", "component", "http", "route", "/api/v1/items/{id}")
	r.Logger().Info("request completed", "component", "http", "route", "/srv/media/file.mkv")
	recs := records(t, flushed(t, r, out))
	if recs[0]["route"] != "/api/v1/items/{id}" || recs[1]["route"] != Redacted {
		t.Fatalf("route records %v", recs)
	}
}

// G46.9: backups older than the age limit, and the oldest ones while the
// files exceed the size cap, are removed; the active file never is.
func TestFileRetentionByAgeAndTotalSize(t *testing.T) {
	clock := newClock()
	dir := t.TempDir()
	path := filepath.Join(dir, "jelee.log")
	f, err := openRotatingFile(RotateOptions{Path: path, MaxBackups: 100}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte(strings.Repeat("a", 100) + "\n")); err != nil {
		t.Fatal(err)
	}
	var names []string
	for i, age := range []time.Duration{40 * 24 * time.Hour, 20 * 24 * time.Hour, 3 * 24 * time.Hour, time.Hour} {
		name := filepath.Join(dir, "jelee-"+clock.Now().Add(-age).UTC().Format(backupStamp)+".000.log")
		if i%2 == 1 {
			name += ".gz"
		}
		if err := os.WriteFile(name, []byte(strings.Repeat("b", 1000)), 0o600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := ListLogFiles(path)
	if err != nil || len(listed) != 5 || listed[4] != path || listed[0] != names[0] {
		t.Fatalf("ListLogFiles = %v, %v", listed, err)
	}
	f.SetRetention(30*24*time.Hour, 0)
	if err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(names[0]); !os.IsNotExist(err) {
		t.Fatal("backup past the age limit kept")
	}
	// 2 backups of 1000 bytes and the 101 byte active file: a 1500 byte
	// cap keeps the newest backup only.
	f.SetRetention(0, 1500)
	if err := f.Prune(); err != nil {
		t.Fatal(err)
	}
	backups, _ := f.Backups()
	if len(backups) != 1 || backups[0] != names[3] {
		t.Fatalf("backups after size cap: %v", backups)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active file removed")
	}
	if stamp, ok := BackupTime(path, names[3]); !ok || !stamp.Equal(clock.Now().Add(-time.Hour).Truncate(time.Nanosecond)) {
		t.Fatalf("BackupTime = %v %v", stamp, ok)
	}
	if _, ok := BackupTime(path, filepath.Join(dir, "unrelated.log")); ok {
		t.Fatal("foreign file read as backup")
	}
	// The router forwards retention to its file; without a file it is a no-op.
	r, _ := openTest(t, Options{Output: OutputFile, File: RotateOptions{Path: filepath.Join(t.TempDir(), "r.log")}})
	r.ApplySettings(domain.LogSettings{LogDays: 7, LogMaxTotalMB: 1})
	if !r.HasFile() || r.file.maxAge.Load() != int64(7*24*time.Hour) || r.file.maxTotal.Load() != 1<<20 || r.PruneFiles() != nil {
		t.Fatal("router retention not applied")
	}
	plain, _ := openTest(t, Options{})
	plain.ApplySettings(domain.LogSettings{LogDays: 7})
	if plain.HasFile() || plain.PruneFiles() != nil {
		t.Fatal("stdout router has a file")
	}
}
