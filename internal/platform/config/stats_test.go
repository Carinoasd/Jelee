package config

import (
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestStatsConfigDefaultsAndEnvironment(t *testing.T) {
	var zero StatsConfig
	if zero.Validate() != nil || zero.Rules() != domain.DefaultWatchStatsRules() || zero.AggregateInterval() != time.Minute || zero.ExportRows() != 100_000 || zero.SundayWeeks() || zero.Retention() != 0 {
		t.Fatal("zero configuration does not follow the defaults")
	}
	if loc, err := zero.Location(); err != nil || loc.String() != "UTC" {
		t.Fatal("default zone", loc, err)
	}
	env := map[string]string{"JELEE_STATS_TIME_ZONE": "Asia/Taipei", "JELEE_STATS_WEEK_START": "sunday", "JELEE_STATS_AGGREGATE_SECONDS": "30",
		"JELEE_STATS_EXPORT_MAX_ROWS": "500", "JELEE_STATS_RETENTION_DAYS": "730", "JELEE_STATS_COMPLETED_RATIO": "0.95", "JELEE_STATS_POSITION_TOLERANCE_SECONDS": "5", "JELEE_STATS_MIN_VIEW_SECONDS": "60"}
	c := DefaultStatsConfig()
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	loc, err := c.Location()
	if c.Validate() != nil || err != nil || loc.String() != "Asia/Taipei" || !c.SundayWeeks() || c.AggregateInterval() != 30*time.Second || c.ExportRows() != 500 || c.Retention() != 730*24*time.Hour {
		t.Fatalf("environment not applied: %+v", c)
	}
	if r := c.Rules(); r.CompletedRatio != 0.95 || r.PositionTolerance != 5*time.Second || r.MinViewDuration != time.Minute {
		t.Fatalf("rules %+v", r)
	}
	for _, key := range []string{"JELEE_STATS_EXPORT_MAX_ROWS", "JELEE_STATS_MIN_VIEW_RATIO"} {
		if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == key }); err == nil {
			t.Fatalf("non-numeric %s accepted", key)
		}
	}
	for _, bad := range []func(*StatsConfig){
		func(c *StatsConfig) { c.TimeZone = "Mars/Olympus" },
		func(c *StatsConfig) { c.TimeZone = "Local" },
		func(c *StatsConfig) { c.WeekStart = "friday" },
		func(c *StatsConfig) { c.AggregateSeconds = 1 },
		func(c *StatsConfig) { c.ExportMaxRows = 0 },
		func(c *StatsConfig) { c.RetentionDays = -1 },
		func(c *StatsConfig) { c.CompletedRatio = 0.2 },
		func(c *StatsConfig) { c.MaxPlaybackRate = 10 },
		func(c *StatsConfig) { c.MaxReportGapSeconds = 5 },
	} {
		c := DefaultStatsConfig()
		bad(&c)
		if c.Validate() == nil {
			t.Errorf("invalid configuration accepted: %+v", c)
		}
	}
}
