package config

import (
	"testing"
	"time"
)

func TestPlaybackConfigDefaultsAndEnvironment(t *testing.T) {
	var zero PlaybackConfig
	if zero.Validate() != nil || zero.FlushInterval() != 10*time.Second || zero.SessionTimeout() != 5*time.Minute || zero.Retention() != 365*24*time.Hour {
		t.Fatal("zero configuration does not follow the defaults")
	}
	env := map[string]string{"JELEE_PLAYBACK_FLUSH_SECONDS": "5", "JELEE_PLAYBACK_MAX_BATCH": "50", "JELEE_PLAYBACK_RETENTION_DAYS": "0", "JELEE_PLAYBACK_SESSION_TIMEOUT_SECONDS": "60"}
	c := DefaultPlaybackConfig()
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.Validate() != nil || c.FlushInterval() != 5*time.Second || c.Batch() != 50 || c.Retention() != 0 || c.SessionTimeout() != time.Minute {
		t.Fatalf("environment not applied: %+v", c)
	}
	if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == "JELEE_PLAYBACK_MAX_SESSIONS" }); err == nil {
		t.Fatal("non-numeric value accepted")
	}
	for _, bad := range []func(*PlaybackConfig){
		func(c *PlaybackConfig) { c.FlushSeconds = 0 },
		func(c *PlaybackConfig) { c.SessionTimeoutSeconds = 2 * c.FlushSeconds },
		func(c *PlaybackConfig) { c.MaxBatch = 10001 },
		func(c *PlaybackConfig) { c.SampleIntervalSeconds = 1 },
		func(c *PlaybackConfig) { c.RetentionDays = -1 },
	} {
		c := DefaultPlaybackConfig()
		bad(&c)
		if c.Validate() == nil {
			t.Errorf("invalid configuration accepted: %+v", c)
		}
	}
}
