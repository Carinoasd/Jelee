package logging

import (
	"log/slog"
	"testing"
)

func TestSpanAttrUsesTheRecordWhitelist(t *testing.T) {
	for _, c := range []struct {
		in   slog.Attr
		want string
	}{
		{slog.String("component", "scan"), "scan"},
		{slog.String("component", "not-a-component"), redacted},
		{slog.String("outcome", "lease_lost"), "lease_lost"},
		{slog.String("outcome", "Bearer secret"), redacted},
		{slog.String("span", "job.nfo_write"), "job.nfo_write"},
		{slog.String("span", "GET /library/a b"), redacted},
		{slog.String("path", "/media/a.mkv"), redacted},
		{slog.String("clientIp", "203.0.113.4"), redacted},
		{slog.Int64("durationMicros", 5), redacted}, // developer mode field, never on here
	} {
		if got := SpanAttr(c.in).Value.String(); got != c.want {
			t.Fatalf("%s=%v became %q, want %q", c.in.Key, c.in.Value, got, c.want)
		}
	}
	if got := SpanAttr(slog.Bool("forced", true)); got.Value.Kind() != slog.KindBool || !got.Value.Bool() {
		t.Fatalf("forced became %v", got)
	}
}
