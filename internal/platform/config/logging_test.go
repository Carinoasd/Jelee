package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"encoding/json"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"runtime"
)

func TestLoggingDefaults(t *testing.T) {
	c, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}))
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Logging.Options()
	if opts.Level != slog.LevelInfo || opts.Format != logging.FormatJSON || opts.Output != logging.OutputStdout || opts.IPMode != logging.IPRedact || opts.PathMode != logging.PathRedact || opts.BufferEntries != logging.DefaultBufferEntries {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
	if opts.File.MaxBytes != 100<<20 || opts.File.Interval != 24*time.Hour || opts.File.MaxBackups != 7 || !opts.File.Compress {
		t.Fatalf("rotation defaults: %+v", opts.File)
	}
	// A configuration built without LoadWith keeps working with defaults.
	if err := (LoggingConfig{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if (LoggingConfig{}).Options().Level != slog.LevelInfo {
		t.Fatal("zero configuration enabled DEBUG")
	}
}

func TestLoggingFileAndEnvironmentOverrides(t *testing.T) {
	values := map[string]string{
		"JELEE_DATABASE_URL": "postgres://localhost/jelee",
		"JELEE_CONFIG":       accountConfigFile(t, `{"logging":{"level":"warn","format":"console","components":{"scan":"debug","ignore":"info"},"file":{"maxBackups":3},"pathMode":"relative","pathRoots":[`+jsonString(testAbsPath("/srv/media"))+`]}}`),
		"JELEE_LOG_OUTPUT":   "both",
		"JELEE_LOG_FILE":     testAbsPath("/var/log/jelee/jelee.log"),
		"JELEE_LOG_IP_MODE":  "mask",
		"JELEE_LOG_COMPRESS": "false",
	}
	c, err := LoadWith(accountConfigLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Logging.Options()
	if opts.Level != slog.LevelWarn || opts.Format != logging.FormatConsole || opts.Output != logging.OutputBoth || opts.IPMode != logging.IPMask || opts.PathMode != logging.PathRelative || len(opts.PathRoots) != 1 {
		t.Fatalf("overrides: %+v", opts)
	}
	if opts.File.Path != testAbsPath("/var/log/jelee/jelee.log") || opts.File.MaxBackups != 3 || opts.File.Compress || opts.File.MaxBytes != 100<<20 {
		t.Fatalf("file overrides: %+v", opts.File)
	}
	if opts.Components["scan"] != slog.LevelDebug || opts.Components["ignore"] != slog.LevelInfo {
		t.Fatalf("components: %v", opts.Components)
	}
	values["JELEE_LOG_COMPONENTS"] = " http=error , metrics=debug ,"
	c, err = LoadWith(accountConfigLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Logging.Options().Components; len(got) != 2 || got["http"] != slog.LevelError || got["metrics"] != slog.LevelDebug {
		t.Fatalf("environment components: %v", got)
	}
	c.Logging.File.Path = filepath.Join(t.TempDir(), "jelee.log")
	var stdout strings.Builder
	r, err := logging.Open(c.Logging.Options(), &stdout)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Debug("db debug", "component", "metrics")
	r.Logger().Warn("http info", "component", "http")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "db debug") || strings.Contains(stdout.String(), "http info") {
		t.Fatalf("configured levels not applied: %q", stdout.String())
	}
}

func TestLoggingRejectsInvalidSettingsWithoutEchoingValues(t *testing.T) {
	for key, value := range map[string]string{
		"JELEE_LOG_LEVEL":          "secret",
		"JELEE_LOG_FORMAT":         "secret",
		"JELEE_LOG_OUTPUT":         "secret",
		"JELEE_LOG_IP_MODE":        "secret",
		"JELEE_LOG_PATH_MODE":      "secret",
		"JELEE_LOG_PATH_ROOTS":     "relative/secret",
		"JELEE_LOG_MAX_SIZE_MB":    "secret",
		"JELEE_LOG_ROTATE_HOURS":   "-1",
		"JELEE_LOG_MAX_BACKUPS":    "1001",
		"JELEE_LOG_BUFFER_ENTRIES": "-5",
		"JELEE_LOG_COMPRESS":       "secret",
		"JELEE_LOG_COMPONENTS":     "secret",
		"JELEE_DB_SLOW_QUERY_MS":   "secret",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", key: value}))
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("accepted or leaked: %v", err)
			}
		})
	}
	for name, body := range map[string]string{
		"unknown component": `{"logging":{"components":{"secret":"debug"}}}`,
		"component level":   `{"logging":{"components":{"scan":"secret"}}}`,
		"file without path": `{"logging":{"output":"file"}}`,
		"relative file":     `{"logging":{"output":"both","file":{"path":"secret.log"}}}`,
		"unknown field":     `{"logging":{"secret":true}}`,
		"slow query range":  `{"logging":{"slowQueryMs":600001}}`,
		"negative slow":     `{"logging":{"slowQueryMs":-1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": accountConfigFile(t, body)}))
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("accepted or leaked: %v", err)
			}
		})
	}
}

// testAbsPath turns a slash path into an absolute path on the running OS:
// Windows needs a volume, so the tests use C: there.
func testAbsPath(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return p
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// G46.10: a configuration that lowers or switches off the audit or
// security log is refused at startup, under the scope or an alias.
func TestLoggingRefusesMandatoryComponentLevels(t *testing.T) {
	for _, value := range []string{"audit=error", "security=warn", "devmode=error", "audit=debug"} {
		_, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_LOG_COMPONENTS": value}))
		if err == nil || !strings.Contains(err.Error(), "audit and security log levels cannot be configured") {
			t.Fatalf("%s: %v", value, err)
		}
	}
}

// G46.3: the slow query threshold defaults to 500 ms; 0 switches it off.
func TestSlowQueryThreshold(t *testing.T) {
	c, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}))
	if err != nil || c.Logging.SlowQueryThreshold() != 500*time.Millisecond {
		t.Fatalf("default: %v %v", c.Logging.SlowQueryThreshold(), err)
	}
	for value, want := range map[string]time.Duration{"0": 0, "1200": 1200 * time.Millisecond} {
		c, err := LoadWith(accountConfigLookup(map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_DB_SLOW_QUERY_MS": value}))
		if err != nil || c.Logging.SlowQueryThreshold() != want {
			t.Fatalf("%s: %v %v", value, c.Logging.SlowQueryThreshold(), err)
		}
	}
}
