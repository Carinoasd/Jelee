package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
	_ "time/tzdata" // the reporting time zone must not depend on the host zoneinfo

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// StatsConfig controls the watch statistics roll-up (G23.3, G23.5) and the
// counting rules shared with playback progress. docs/watch-statistics.md
// defines every rule.
type StatsConfig struct {
	// TimeZone is the IANA zone whose midnight cuts days. Changing it
	// applies to sessions aggregated afterwards; days already rolled up keep
	// the zone they were cut in.
	TimeZone string `json:"timeZone"`
	// WeekStart is "monday" (ISO 8601) or "sunday"; it applies at query
	// time.
	WeekStart string `json:"weekStart"`
	// AggregateSeconds is the roll-up interval; ended sessions also wake it.
	AggregateSeconds int `json:"aggregateSeconds"`
	// ExportMaxRows bounds an administrator export.
	ExportMaxRows int `json:"exportMaxRows"`
	// RetentionDays deletes daily statistics of days older than this; 0
	// keeps them until users clear their history (the default: the roll-up
	// holds no reports, devices or positions).
	RetentionDays int `json:"retentionDays"`

	MaxReportGapSeconds      int     `json:"maxReportGapSeconds"`
	MaxPlaybackRate          float64 `json:"maxPlaybackRate"`
	PositionToleranceSeconds int     `json:"positionToleranceSeconds"`
	CompletedRatio           float64 `json:"completedRatio"`
	MinViewSeconds           int     `json:"minViewSeconds"`
	MinViewRatio             float64 `json:"minViewRatio"`
	MinResumeSeconds         int     `json:"minResumeSeconds"`
}

// DefaultStatsConfig cuts days in UTC, starts weeks on Monday, rolls up
// every minute and uses domain.DefaultWatchStatsRules.
func DefaultStatsConfig() StatsConfig {
	r := domain.DefaultWatchStatsRules()
	return StatsConfig{TimeZone: "UTC", WeekStart: "monday", AggregateSeconds: 60, ExportMaxRows: 100_000,
		MaxReportGapSeconds: int(r.MaxReportGap / time.Second), MaxPlaybackRate: r.MaxPlaybackRate, PositionToleranceSeconds: int(r.PositionTolerance / time.Second),
		CompletedRatio: r.CompletedRatio, MinViewSeconds: int(r.MinViewDuration / time.Second), MinViewRatio: r.MinViewRatio, MinResumeSeconds: int(r.MinResumePosition / time.Second)}
}

func (c StatsConfig) effective() StatsConfig {
	if c == (StatsConfig{}) {
		return DefaultStatsConfig()
	}
	return c
}

func (c StatsConfig) Validate() error {
	c = c.effective()
	if _, err := c.Location(); err != nil {
		return err
	}
	switch {
	case c.WeekStart != "monday" && c.WeekStart != "sunday":
		return errors.New("stats weekStart must be monday or sunday")
	case c.AggregateSeconds < 5 || c.AggregateSeconds > 3600:
		return errors.New("stats aggregateSeconds must be between 5 and 3600")
	case c.ExportMaxRows < 1 || c.ExportMaxRows > 5_000_000:
		return errors.New("stats exportMaxRows must be between 1 and 5000000")
	case c.RetentionDays < 0 || c.RetentionDays > 36500:
		return errors.New("stats retentionDays must be between 0 and 36500")
	}
	for _, v := range []float64{c.MaxPlaybackRate, c.CompletedRatio, c.MinViewRatio} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("stats ratios must be finite")
		}
	}
	if c.Rules().Validate() != nil {
		return errors.New("stats rules out of range: maxReportGapSeconds 10-3600, maxPlaybackRate 1-4, positionToleranceSeconds 0-30, completedRatio 0.5-1, minViewSeconds 0-1800, minViewRatio 0-1, minResumeSeconds 0-600")
	}
	return nil
}

// Location loads the reporting time zone.
func (c StatsConfig) Location() (*time.Location, error) {
	c = c.effective()
	if c.TimeZone == "" || c.TimeZone == "Local" {
		return nil, errors.New("stats timeZone must name an IANA time zone")
	}
	loc, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return nil, errors.New("stats timeZone is not a known IANA time zone")
	}
	return loc, nil
}

// SundayWeeks reports whether weeks start on Sunday.
func (c StatsConfig) SundayWeeks() bool { return c.effective().WeekStart == "sunday" }

func (c StatsConfig) AggregateInterval() time.Duration {
	return time.Duration(c.effective().AggregateSeconds) * time.Second
}

func (c StatsConfig) ExportRows() int { return c.effective().ExportMaxRows }

// Retention is zero when daily statistics are kept without limit.
func (c StatsConfig) Retention() time.Duration {
	return time.Duration(c.effective().RetentionDays) * 24 * time.Hour
}

// Rules are the counting rules shared by playback progress (resume points,
// kept samples) and the roll-up.
func (c StatsConfig) Rules() domain.WatchStatsRules {
	c = c.effective()
	return domain.WatchStatsRules{
		MaxReportGap: time.Duration(c.MaxReportGapSeconds) * time.Second, MaxPlaybackRate: c.MaxPlaybackRate,
		PositionTolerance: time.Duration(c.PositionToleranceSeconds) * time.Second, CompletedRatio: c.CompletedRatio,
		MinViewDuration: time.Duration(c.MinViewSeconds) * time.Second, MinViewRatio: c.MinViewRatio,
		MinResumePosition: time.Duration(c.MinResumeSeconds) * time.Second,
	}
}

func (c *StatsConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*string{
		"JELEE_STATS_TIME_ZONE":  &c.TimeZone,
		"JELEE_STATS_WEEK_START": &c.WeekStart,
	} {
		if value, ok := lookup(key); ok {
			*target = value
		}
	}
	for key, target := range map[string]*int{
		"JELEE_STATS_AGGREGATE_SECONDS":          &c.AggregateSeconds,
		"JELEE_STATS_EXPORT_MAX_ROWS":            &c.ExportMaxRows,
		"JELEE_STATS_RETENTION_DAYS":             &c.RetentionDays,
		"JELEE_STATS_MAX_REPORT_GAP_SECONDS":     &c.MaxReportGapSeconds,
		"JELEE_STATS_POSITION_TOLERANCE_SECONDS": &c.PositionToleranceSeconds,
		"JELEE_STATS_MIN_VIEW_SECONDS":           &c.MinViewSeconds,
		"JELEE_STATS_MIN_RESUME_SECONDS":         &c.MinResumeSeconds,
	} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	for key, target := range map[string]*float64{
		"JELEE_STATS_MAX_PLAYBACK_RATE": &c.MaxPlaybackRate,
		"JELEE_STATS_COMPLETED_RATIO":   &c.CompletedRatio,
		"JELEE_STATS_MIN_VIEW_RATIO":    &c.MinViewRatio,
	} {
		if value, ok := lookup(key); ok {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = f
		}
	}
	return nil
}
