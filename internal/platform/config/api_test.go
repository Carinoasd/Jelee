package config

import (
	"testing"
	"time"
)

func TestAPIConfigDefaultsEnvironmentAndBounds(t *testing.T) {
	var zero APIConfig
	r := zero.RateLimit
	if !r.RateLimited() || r.Rate() != 20 || r.BurstSize() != 100 || r.Clients() != 65536 || zero.BodyLimit() != 8<<20 || zero.Validate(15) != nil {
		t.Fatalf("zero value must select the documented defaults: %+v", zero)
	}
	if zero.Timeout(15) != 30*time.Second || zero.Timeout(60) != time.Minute {
		t.Fatal("the default timeout must cover requestTimeoutSeconds")
	}
	env := map[string]string{"JELEE_API_RATE_LIMIT": "off", "JELEE_API_RATE_LIMIT_RPS": "5", "JELEE_API_RATE_LIMIT_BURST": "7",
		"JELEE_API_RATE_LIMIT_MAX_CLIENTS": "64", "JELEE_API_TIMEOUT_SECONDS": "45", "JELEE_API_MAX_BODY_BYTES": "4194304"}
	var c APIConfig
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.RateLimit.RateLimited() || c.RateLimit.Rate() != 5 || c.RateLimit.BurstSize() != 7 || c.RateLimit.Clients() != 64 || c.Timeout(15) != 45*time.Second || c.BodyLimit() != 4<<20 || c.Validate(15) != nil {
		t.Fatalf("environment not applied: %+v", c)
	}
	for _, bad := range []APIConfig{{RateLimit: APIRateLimitConfig{Mode: "maybe"}}, {RateLimit: APIRateLimitConfig{RequestsPerSecond: -1}}, {RateLimit: APIRateLimitConfig{RequestsPerSecond: 10001}},
		{RateLimit: APIRateLimitConfig{Burst: -1}}, {RateLimit: APIRateLimitConfig{Burst: 100001}}, {RateLimit: APIRateLimitConfig{MaxClients: 15}}, {RateLimit: APIRateLimitConfig{MaxClients: 1<<20 + 1}},
		{TimeoutSeconds: -1}, {TimeoutSeconds: 601}, {TimeoutSeconds: 10}, {MaxBodyBytes: 1 << 20}, {MaxBodyBytes: 256<<20 + 1}} {
		if bad.Validate(15) == nil {
			t.Errorf("%+v must be rejected", bad)
		}
	}
	for _, key := range []string{"JELEE_API_RATE_LIMIT_RPS", "JELEE_API_RATE_LIMIT_BURST", "JELEE_API_RATE_LIMIT_MAX_CLIENTS", "JELEE_API_TIMEOUT_SECONDS", "JELEE_API_MAX_BODY_BYTES"} {
		var c APIConfig
		if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == key }); err == nil {
			t.Errorf("%s must be numeric", key)
		}
	}
	cfg, err := LoadWith(func(k string) (string, bool) {
		v, ok := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_API_TIMEOUT_SECONDS": "5"}[k]
		return v, ok
	})
	if err == nil {
		t.Fatalf("a timeout below requestTimeoutSeconds must fail the configuration: %+v", cfg.API)
	}
	cfg, err = LoadWith(func(k string) (string, bool) {
		v, ok := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_API_RATE_LIMIT": "off"}[k]
		return v, ok
	})
	if err != nil || cfg.API.RateLimit.RateLimited() {
		t.Fatalf("rate limit switch: %v %+v", err, cfg.API)
	}
	cfg.API.RateLimit.Mode = "bad"
	if cfg.Validate() == nil {
		t.Fatal("Validate must check the api section")
	}
}
