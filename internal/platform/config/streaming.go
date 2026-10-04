package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// StreamingConfig controls direct delivery enforcement (G07.4). The
// concurrent playback and bandwidth limits each have their own switch
// (G45.4); per-user overrides apply only while the matching switch is on.
type StreamingConfig struct {
	// RevokeCheckSeconds is how often a running stream rechecks its session in
	// the database, so revoked sessions lose their streams within this time.
	// Zero keeps the 5 second default for configurations built without this
	// section. It cannot be disabled.
	RevokeCheckSeconds int `json:"revokeCheckSeconds"`
	// EnableStreamLimit turns the concurrent playback limits on.
	EnableStreamLimit bool `json:"enableStreamLimit"`
	// MaxStreamsPerUser bounds distinct playbacks per user; 0 is unlimited
	// for users without an override.
	MaxStreamsPerUser int `json:"maxStreamsPerUser"`
	// MaxStreamsPerDevice bounds distinct playbacks per device; 0 disables it.
	MaxStreamsPerDevice int `json:"maxStreamsPerDevice"`
	// EnableBandwidthLimit turns the bandwidth limit on.
	EnableBandwidthLimit bool `json:"enableBandwidthLimit"`
	// MaxKbpsPerUser is the rate in kilobits per second shared by all streams
	// of a user (or of a device, see BandwidthScope); 0 is unlimited for users
	// without an override.
	MaxKbpsPerUser int64 `json:"maxKbpsPerUser"`
	// BandwidthScope is "user" (default) or "device".
	BandwidthScope string `json:"bandwidthScope"`
}

const defaultRevokeCheckSeconds = 5

// DefaultStreamingConfig limits each user to four simultaneous playbacks and
// enables the bandwidth limit without a server-wide rate, so administrators
// can cap individual users while everyone else stays unthrottled.
func DefaultStreamingConfig() StreamingConfig {
	return StreamingConfig{RevokeCheckSeconds: defaultRevokeCheckSeconds, EnableStreamLimit: true, MaxStreamsPerUser: 4, EnableBandwidthLimit: true, BandwidthScope: "user"}
}

func (c StreamingConfig) Validate() error {
	if c.RevokeCheckSeconds < 0 || c.RevokeCheckSeconds > 300 {
		return errors.New("streaming revokeCheckSeconds must be between 1 and 300")
	}
	if c.MaxStreamsPerUser < 0 || c.MaxStreamsPerUser > 128 || c.MaxStreamsPerDevice < 0 || c.MaxStreamsPerDevice > 128 {
		return errors.New("streaming stream limits must be between 0 and 128")
	}
	if c.MaxKbpsPerUser < 0 || c.MaxKbpsPerUser > 10_000_000 {
		return errors.New("streaming maxKbpsPerUser must be between 0 and 10000000")
	}
	if c.BandwidthScope != "" && c.BandwidthScope != "user" && c.BandwidthScope != "device" {
		return errors.New("streaming bandwidthScope must be user or device")
	}
	return nil
}

// RevokeCheckInterval is the effective session recheck interval.
func (c StreamingConfig) RevokeCheckInterval() time.Duration {
	if c.RevokeCheckSeconds == 0 {
		return defaultRevokeCheckSeconds * time.Second
	}
	return time.Duration(c.RevokeCheckSeconds) * time.Second
}

func (c *StreamingConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*bool{"JELEE_STREAM_LIMIT_ENABLED": &c.EnableStreamLimit, "JELEE_BANDWIDTH_LIMIT_ENABLED": &c.EnableBandwidthLimit} {
		if value, ok := lookup(key); ok {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = b
		}
	}
	for key, target := range map[string]*int{"JELEE_STREAM_REVOKE_CHECK_SECONDS": &c.RevokeCheckSeconds, "JELEE_STREAM_MAX_PER_USER": &c.MaxStreamsPerUser, "JELEE_STREAM_MAX_PER_DEVICE": &c.MaxStreamsPerDevice} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil || key == "JELEE_STREAM_REVOKE_CHECK_SECONDS" && n == 0 {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	if value, ok := lookup("JELEE_BANDWIDTH_MAX_KBPS_PER_USER"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("invalid JELEE_BANDWIDTH_MAX_KBPS_PER_USER")
		}
		c.MaxKbpsPerUser = n
	}
	if value, ok := lookup("JELEE_BANDWIDTH_SCOPE"); ok {
		c.BandwidthScope = value
	}
	return nil
}
