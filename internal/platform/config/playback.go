package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// PlaybackConfig controls how playback reports are buffered and kept
// (G23.2, G23.4). Reports are coalesced in memory per session and written in
// batches; no report causes a database write of its own, except a stop,
// which is written at once so the resume point is current when the client
// asks for it.
type PlaybackConfig struct {
	// FlushSeconds is the interval between batch writes of buffered progress.
	FlushSeconds int `json:"flushSeconds"`
	// MaxBatch bounds the sessions written by one statement; a buffer holding
	// this many dirty sessions is flushed before the interval ends.
	MaxBatch int `json:"maxBatch"`
	// MaxSessions bounds the sessions held in memory; new sessions beyond it
	// are refused with playback_busy.
	MaxSessions int `json:"maxSessions"`
	// SessionTimeoutSeconds closes a session (timed_out) that has not
	// reported for this long. It must be at least three flush intervals so
	// another instance never closes a session that is still being flushed.
	SessionTimeoutSeconds int `json:"sessionTimeoutSeconds"`
	// ReportIntervalSeconds is the progress report interval clients are told
	// to keep (client side throttling). Faster reports are coalesced.
	ReportIntervalSeconds int `json:"reportIntervalSeconds"`
	// SampleIntervalSeconds is the spacing of the progress samples kept for
	// statistics; state changes and discontinuities are always kept.
	SampleIntervalSeconds int `json:"sampleIntervalSeconds"`
	// RetentionDays deletes ended sessions and their samples after this many
	// days; 0 keeps them until users clear their history. Resume points and
	// played states are user data and are not subject to retention.
	RetentionDays int `json:"retentionDays"`
}

// DefaultPlaybackConfig flushes every 10 seconds, closes sessions silent
// for 5 minutes and keeps history for a year.
func DefaultPlaybackConfig() PlaybackConfig {
	return PlaybackConfig{FlushSeconds: 10, MaxBatch: 500, MaxSessions: 10000, SessionTimeoutSeconds: 300, ReportIntervalSeconds: 10, SampleIntervalSeconds: 60, RetentionDays: 365}
}

// effective fills zero values (a configuration built without this section)
// with the defaults.
func (c PlaybackConfig) effective() PlaybackConfig {
	d := DefaultPlaybackConfig()
	if c == (PlaybackConfig{}) {
		return d
	}
	return c
}

func (c PlaybackConfig) Validate() error {
	c = c.effective()
	switch {
	case c.FlushSeconds < 1 || c.FlushSeconds > 300:
		return errors.New("playback flushSeconds must be between 1 and 300")
	case c.MaxBatch < 1 || c.MaxBatch > 10000:
		return errors.New("playback maxBatch must be between 1 and 10000")
	case c.MaxSessions < 1 || c.MaxSessions > 1_000_000:
		return errors.New("playback maxSessions must be between 1 and 1000000")
	case c.SessionTimeoutSeconds < 3*c.FlushSeconds || c.SessionTimeoutSeconds < 30 || c.SessionTimeoutSeconds > 86400:
		return errors.New("playback sessionTimeoutSeconds must be between 30 and 86400 and at least three flush intervals")
	case c.ReportIntervalSeconds < 1 || c.ReportIntervalSeconds > 300:
		return errors.New("playback reportIntervalSeconds must be between 1 and 300")
	case c.SampleIntervalSeconds < 5 || c.SampleIntervalSeconds > 300:
		return errors.New("playback sampleIntervalSeconds must be between 5 and 300")
	case c.RetentionDays < 0 || c.RetentionDays > 36500:
		return errors.New("playback retentionDays must be between 0 and 36500")
	}
	return nil
}

func (c PlaybackConfig) FlushInterval() time.Duration {
	return time.Duration(c.effective().FlushSeconds) * time.Second
}

func (c PlaybackConfig) SessionTimeout() time.Duration {
	return time.Duration(c.effective().SessionTimeoutSeconds) * time.Second
}

func (c PlaybackConfig) ReportInterval() time.Duration {
	return time.Duration(c.effective().ReportIntervalSeconds) * time.Second
}

func (c PlaybackConfig) SampleInterval() time.Duration {
	return time.Duration(c.effective().SampleIntervalSeconds) * time.Second
}

// Retention is zero when history is kept without limit.
func (c PlaybackConfig) Retention() time.Duration {
	return time.Duration(c.effective().RetentionDays) * 24 * time.Hour
}

func (c PlaybackConfig) Batch() int { return c.effective().MaxBatch }

func (c PlaybackConfig) Sessions() int { return c.effective().MaxSessions }

func (c *PlaybackConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*int{
		"JELEE_PLAYBACK_FLUSH_SECONDS":           &c.FlushSeconds,
		"JELEE_PLAYBACK_MAX_BATCH":               &c.MaxBatch,
		"JELEE_PLAYBACK_MAX_SESSIONS":            &c.MaxSessions,
		"JELEE_PLAYBACK_SESSION_TIMEOUT_SECONDS": &c.SessionTimeoutSeconds,
		"JELEE_PLAYBACK_REPORT_INTERVAL_SECONDS": &c.ReportIntervalSeconds,
		"JELEE_PLAYBACK_SAMPLE_INTERVAL_SECONDS": &c.SampleIntervalSeconds,
		"JELEE_PLAYBACK_RETENTION_DAYS":          &c.RetentionDays,
	} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	return nil
}
