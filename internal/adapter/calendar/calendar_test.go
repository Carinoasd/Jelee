package calendar

import (
	"errors"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestNextOccurrence(t *testing.T) {
	for _, tc := range []struct{ name, expr, zone, after, want string }{
		{"Taipei", "0 9 * * *", "Asia/Taipei", "2026-10-02T00:00:00Z", "2026-10-02T01:00:00Z"},
		{"strictly after", "0 9 * * *", "Asia/Taipei", "2026-10-02T01:00:00Z", "2026-10-03T01:00:00Z"},
		{"spring gap", "30 2 * * *", "America/New_York", "2026-03-08T06:00:00Z", "2026-03-09T06:30:00Z"},
		{"fall first", "30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"fall repeated", "30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"leap day", "0 0 29 2 *", "UTC", "2025-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, _ := time.Parse(time.RFC3339, tc.after)
			next, err := (Calendar{}).Next(domain.ScheduleTiming{Mode: "cron", Cron: tc.expr, Timezone: tc.zone}, after)
			if err != nil || next.Format(time.RFC3339) != tc.want {
				t.Fatalf("got %s, %v; want %s", next, err, tc.want)
			}
		})
	}
	after := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)
	next, err := (Calendar{}).Next(domain.ScheduleTiming{Mode: "interval", IntervalSeconds: 86400, Timezone: "America/New_York"}, after)
	if err != nil || next.Sub(after) != 24*time.Hour {
		t.Fatal("interval changed across DST", next, err)
	}
}

func TestInvalidSchedule(t *testing.T) {
	base := domain.ScheduleTiming{Mode: "cron", Cron: "* * * * *", Timezone: "UTC"}
	for _, mutate := range []func(*domain.ScheduleTiming){
		func(v *domain.ScheduleTiming) { v.Timezone = "Local" }, func(v *domain.ScheduleTiming) { v.Timezone = "" }, func(v *domain.ScheduleTiming) { v.Timezone = "../UTC" }, func(v *domain.ScheduleTiming) { v.Timezone = "Mars/Nowhere" },
		func(v *domain.ScheduleTiming) { v.Cron = "TZ=UTC" }, func(v *domain.ScheduleTiming) { v.Cron = "CRON_TZ=UTC * * * * *" }, func(v *domain.ScheduleTiming) { v.Cron = "@every 1s" }, func(v *domain.ScheduleTiming) { v.Cron = "* * * * * *" }, func(v *domain.ScheduleTiming) { v.Cron = "0 0 31 2 *" }, func(v *domain.ScheduleTiming) { v.Cron = "*/0 * * * *" }, func(v *domain.ScheduleTiming) { v.Cron = strings.Repeat("1", 257) }, func(v *domain.ScheduleTiming) { v.IntervalSeconds = 60 }, func(v *domain.ScheduleTiming) { v.Mode = "unknown" },
		func(v *domain.ScheduleTiming) { v.Mode = "interval"; v.Cron = ""; v.IntervalSeconds = 59 }, func(v *domain.ScheduleTiming) { v.Mode = "interval"; v.Cron = ""; v.IntervalSeconds = 31536001 },
	} {
		v := base
		mutate(&v)
		if _, err := (Calendar{}).Next(v, time.Now()); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("accepted invalid schedule: %+v", v)
		}
	}
}
