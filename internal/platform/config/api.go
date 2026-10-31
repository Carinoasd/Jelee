package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// APIConfig hardens the request handling of the native API and the
// compatibility layer (G08.7): a per-client rate limit, one processing
// timeout and one request body ceiling. The zero value selects the defaults,
// so the limit and the timeout are on unless configured otherwise. Direct
// media delivery, item images, subtitle and attachment tracks and playback
// information are never limited, timed out or capped by this section.
type APIConfig struct {
	RateLimit APIRateLimitConfig `json:"rateLimit"`
	// TimeoutSeconds bounds the processing of one non-streaming request;
	// 0 selects max(30, requestTimeoutSeconds). It may not be shorter than
	// requestTimeoutSeconds, which bounds the individual lookups inside.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// MaxBodyBytes caps every request body; 0 selects 8 MiB. Routes keep
	// their own smaller limits; item image uploads are bounded by
	// images.maxSourceBytes alone.
	MaxBodyBytes int64 `json:"maxBodyBytes"`
}

// APIRateLimitConfig is the token bucket of each client: authenticated
// requests count against their user and session token, anonymous ones and
// failed authentications against the client address.
type APIRateLimitConfig struct {
	// Mode is "on" (the default when empty) or "off".
	Mode string `json:"mode"`
	// RequestsPerSecond refills the bucket; 0 selects 20.
	RequestsPerSecond int `json:"requestsPerSecond"`
	// Burst is the bucket size; 0 selects 100.
	Burst int `json:"burst"`
	// MaxClients bounds the buckets held in memory, least recently used
	// first out; 0 selects 65536.
	MaxClients int `json:"maxClients"`
}

const (
	defaultAPIRequestsPerSecond = 20
	defaultAPIBurst             = 100
	defaultAPIMaxClients        = 65536
	defaultAPITimeoutSeconds    = 30
	defaultAPIMaxBodyBytes      = 8 << 20
	// minAPIMaxBodyBytes keeps the largest per-route JSON limit (2 MiB,
	// site settings and item metadata) reachable.
	minAPIMaxBodyBytes = 2 << 20
	maxAPIMaxBodyBytes = 256 << 20
	maxAPITimeout      = 600
)

// Validate checks the section against the per-lookup request timeout. It
// never includes configured values in its errors.
func (c APIConfig) Validate(requestTimeoutSeconds int) error {
	r := c.RateLimit
	switch {
	case r.Mode != "" && r.Mode != "on" && r.Mode != "off":
		return errors.New("api rateLimit mode must be on or off")
	case r.RequestsPerSecond < 0 || r.RequestsPerSecond > 10000:
		return errors.New("api rateLimit requestsPerSecond must be between 1 and 10000")
	case r.Burst < 0 || r.Burst > 100000:
		return errors.New("api rateLimit burst must be between 1 and 100000")
	case r.MaxClients != 0 && (r.MaxClients < 16 || r.MaxClients > 1<<20):
		return errors.New("api rateLimit maxClients must be between 16 and 1048576")
	case c.TimeoutSeconds < 0 || c.TimeoutSeconds > maxAPITimeout:
		return errors.New("api timeoutSeconds must be between 1 and 600")
	case c.TimeoutSeconds != 0 && c.TimeoutSeconds < requestTimeoutSeconds:
		return errors.New("api timeoutSeconds must not be shorter than requestTimeoutSeconds")
	case c.MaxBodyBytes != 0 && (c.MaxBodyBytes < minAPIMaxBodyBytes || c.MaxBodyBytes > maxAPIMaxBodyBytes):
		return errors.New("api maxBodyBytes must be between 2097152 and 268435456")
	}
	return nil
}

// RateLimited reports whether the per-client rate limit applies.
func (c APIRateLimitConfig) RateLimited() bool { return c.Mode != "off" }

// Rate is the effective refill in requests per second.
func (c APIRateLimitConfig) Rate() int {
	if c.RequestsPerSecond == 0 {
		return defaultAPIRequestsPerSecond
	}
	return c.RequestsPerSecond
}

// BurstSize is the effective bucket size.
func (c APIRateLimitConfig) BurstSize() int {
	if c.Burst == 0 {
		return defaultAPIBurst
	}
	return c.Burst
}

// Clients is the effective bucket bound.
func (c APIRateLimitConfig) Clients() int {
	if c.MaxClients == 0 {
		return defaultAPIMaxClients
	}
	return c.MaxClients
}

// Timeout is the effective processing timeout of one request.
func (c APIConfig) Timeout(requestTimeoutSeconds int) time.Duration {
	if c.TimeoutSeconds != 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return time.Duration(max(defaultAPITimeoutSeconds, requestTimeoutSeconds)) * time.Second
}

// BodyLimit is the effective request body ceiling in bytes.
func (c APIConfig) BodyLimit() int64 {
	if c.MaxBodyBytes == 0 {
		return defaultAPIMaxBodyBytes
	}
	return c.MaxBodyBytes
}

func (c *APIConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_API_RATE_LIMIT"); ok {
		c.RateLimit.Mode = value
	}
	for key, target := range map[string]*int{
		"JELEE_API_RATE_LIMIT_RPS":         &c.RateLimit.RequestsPerSecond,
		"JELEE_API_RATE_LIMIT_BURST":       &c.RateLimit.Burst,
		"JELEE_API_RATE_LIMIT_MAX_CLIENTS": &c.RateLimit.MaxClients,
		"JELEE_API_TIMEOUT_SECONDS":        &c.TimeoutSeconds,
	} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	if value, ok := lookup("JELEE_API_MAX_BODY_BYTES"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("invalid JELEE_API_MAX_BODY_BYTES")
		}
		c.MaxBodyBytes = n
	}
	return nil
}
