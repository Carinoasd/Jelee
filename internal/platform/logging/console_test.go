package logging

import (
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConsoleFormat(t *testing.T) {
	r, out := openTest(t, Options{Format: FormatConsole, Level: slog.LevelDebug})
	log := r.Logger().With("component", "jobs").WithGroup("job")
	log.Warn("job claim unavailable", "taskId", "11111111-1111-4111-8111-111111111111", "state", "running",
		slog.Group("progress", "count", 3, "durationMs", 1500*time.Millisecond), "password", "hunter2")
	r.Logger().Debug("waiting for metrics cleanup", "component", "metrics", "event", "direct_delivery")
	r.Logger().Error("bad message http://user:pw@example/ token", "requestId", "abc 123")
	lines := strings.Split(strings.TrimSuffix(flushed(t, r, out), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%q", lines)
	}
	stamp := `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2}) `
	want := []string{
		stamp + regexp.QuoteMeta(`WARN  job claim unavailable component=jobs job.taskId=11111111-1111-4111-8111-111111111111 job.state=running job.progress.count=3 job.progress.durationMs=1.5s job.password=[redacted]`) + `$`,
		stamp + regexp.QuoteMeta(`DEBUG waiting for metrics cleanup component=metrics event=direct_delivery`) + `$`,
		stamp + regexp.QuoteMeta(`ERROR [redacted] requestId=[redacted]`) + `$`,
	}
	for i, pattern := range want {
		if !regexp.MustCompile(`^` + pattern).MatchString(lines[i]) {
			t.Errorf("line %d = %q", i, lines[i])
		}
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Fatal("console leaked a password")
	}
}

func TestConsoleQuotesAmbiguousValues(t *testing.T) {
	var b []byte
	for value, want := range map[string]string{"plain": "plain", "": `""`, "two words": `"two words"`, "a=b": `"a=b"`, "line\nbreak": `"line\nbreak"`} {
		b = appendConsoleValue(b[:0], slog.StringValue(value))
		if string(b) != want {
			t.Errorf("%q rendered %s want %s", value, b, want)
		}
	}
}
