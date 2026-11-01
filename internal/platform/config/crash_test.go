package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// G46.3 crash archive: crash/ next to the log file by default, none
// without a log file, an absolute crashDir and a keep count of 0–1000 (0
// switches the archive off) from the file or the environment.
func TestCrashArchiveOptions(t *testing.T) {
	base := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	load := func(extra map[string]string) (Config, error) {
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range extra {
			values[k] = v
		}
		return LoadWith(accountConfigLookup(values))
	}
	c, err := load(nil)
	if err != nil || c.Logging.CrashOptions() != (logging.CrashOptions{}) {
		t.Fatalf("stdout default: %+v %v", c.Logging.CrashOptions(), err)
	}
	logFile := testAbsPath("/var/log/jelee/jelee.log")
	c, err = load(map[string]string{"JELEE_LOG_OUTPUT": "file", "JELEE_LOG_FILE": logFile})
	if err != nil || c.Logging.CrashOptions() != (logging.CrashOptions{Dir: filepath.Join(filepath.Dir(logFile), "crash"), Keep: logging.DefaultCrashKeep}) {
		t.Fatalf("file default: %+v %v", c.Logging.CrashOptions(), err)
	}
	crashDir := testAbsPath("/srv/jelee/crash")
	c, err = load(map[string]string{"JELEE_CONFIG": accountConfigFile(t, `{"logging":{"crashDir":`+jsonString(crashDir)+`,"crashKeep":5}}`)})
	if err != nil || c.Logging.CrashOptions() != (logging.CrashOptions{Dir: crashDir, Keep: 5}) {
		t.Fatalf("file values: %+v %v", c.Logging.CrashOptions(), err)
	}
	c, err = load(map[string]string{"JELEE_LOG_CRASH_DIR": crashDir, "JELEE_LOG_CRASH_KEEP": "0"})
	if err != nil || c.Logging.CrashOptions() != (logging.CrashOptions{}) {
		t.Fatalf("keep 0: %+v %v", c.Logging.CrashOptions(), err)
	}
	for name, extra := range map[string]map[string]string{
		"relative dir":  {"JELEE_LOG_CRASH_DIR": "relative/crash"},
		"negative keep": {"JELEE_LOG_CRASH_KEEP": "-1"},
		"too many":      {"JELEE_LOG_CRASH_KEEP": "1001"},
		"not a number":  {"JELEE_LOG_CRASH_KEEP": "twenty"},
	} {
		_, err := load(extra)
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
		for _, value := range extra {
			if strings.Contains(err.Error(), value) {
				t.Fatalf("%s: error echoes the value: %v", name, err)
			}
		}
	}
}
