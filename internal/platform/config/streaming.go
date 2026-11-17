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
	// BufferKiB is the per-stream copy buffer of the buffered delivery path
	// (TLS, bandwidth limits, platforms without sendfile, multipart ranges),
	// 16 to 1024 KiB; zero keeps the 32 KiB default (G10.6).
	BufferKiB int `json:"bufferKiB"`
	// PrefetchKiB is the read-ahead advice a GET gives when it opens a file:
	// that much from the first requested offset, plus the recorded seek
	// index (MP4 moov, Matroska Cues) for a request from the start. 64 to
	// 65536 KiB; zero keeps the 2048 KiB default. Linux only; it never
	// changes the bytes sent.
	PrefetchKiB int `json:"prefetchKiB"`
	// DisablePrefetch turns the read-ahead advice off.
	DisablePrefetch bool `json:"disablePrefetch"`
}

const (
	defaultRevokeCheckSeconds = 5
	defaultBufferKiB          = 32
	defaultPrefetchKiB        = 2048
)

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
	if c.BufferKiB != 0 && (c.BufferKiB < 16 || c.BufferKiB > 1024) {
		return errors.New("streaming bufferKiB must be between 16 and 1024")
	}
	if c.PrefetchKiB != 0 && (c.PrefetchKiB < 64 || c.PrefetchKiB > 65536) {
		return errors.New("streaming prefetchKiB must be between 64 and 65536")
	}
	return nil
}

// BufferBytes is the effective copy buffer size of the buffered path.
func (c StreamingConfig) BufferBytes() int {
	if c.BufferKiB == 0 {
		return defaultBufferKiB << 10
	}
	return c.BufferKiB << 10
}

// PrefetchBytes is the effective read-ahead advice size; zero when disabled.
func (c StreamingConfig) PrefetchBytes() int64 {
	switch {
	case c.DisablePrefetch:
		return 0
	case c.PrefetchKiB == 0:
		return defaultPrefetchKiB << 10
	}
	return int64(c.PrefetchKiB) << 10
}

// RevokeCheckInterval is the effective session recheck interval.
func (c StreamingConfig) RevokeCheckInterval() time.Duration {
	if c.RevokeCheckSeconds == 0 {
		return defaultRevokeCheckSeconds * time.Second
	}
	return time.Duration(c.RevokeCheckSeconds) * time.Second
}

func (c *StreamingConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for key, target := range map[string]*bool{"JELEE_STREAM_LIMIT_ENABLED": &c.EnableStreamLimit, "JELEE_BANDWIDTH_LIMIT_ENABLED": &c.EnableBandwidthLimit, "JELEE_STREAM_PREFETCH_DISABLED": &c.DisablePrefetch} {
		if value, ok := lookup(key); ok {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = b
		}
	}
	for key, target := range map[string]*int{"JELEE_STREAM_REVOKE_CHECK_SECONDS": &c.RevokeCheckSeconds, "JELEE_STREAM_MAX_PER_USER": &c.MaxStreamsPerUser, "JELEE_STREAM_MAX_PER_DEVICE": &c.MaxStreamsPerDevice,
		"JELEE_STREAM_BUFFER_KIB": &c.BufferKiB, "JELEE_STREAM_PREFETCH_KIB": &c.PrefetchKiB} {
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
