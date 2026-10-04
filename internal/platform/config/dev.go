package config

import (
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
)

// DevConfig is the configuration-file half of the developer mode gate
// (G45.1). Enabling still needs JELEE_DEV_MODE=true and a one-time token
// redeemed with jelee-cli devmode enable; JELEE_ENV=production ignores all of
// it (G45.2). See docs/developer-mode.md.
type DevConfig struct {
	// Enabled is dev.enabled.
	Enabled bool `json:"enabled"`
	// TTLMinutes bounds every session (G45.7); 0 selects 720 (12 hours) and
	// the maximum is 1440.
	TTLMinutes int `json:"ttlMinutes"`
	// PersistAcrossRestart keeps an active session when a process restarts.
	// Off by default; every start that keeps a session logs a WARN.
	PersistAcrossRestart bool `json:"persistAcrossRestart"`

	// EnvFlag is JELEE_DEV_MODE=true; Environment is JELEE_ENV. Neither can
	// come from the configuration file.
	EnvFlag     bool   `json:"-"`
	Environment string `json:"-"`
}

func (c *DevConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup(devmode.EnvDevMode); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "false":
		case "true":
			c.EnvFlag = true
		default:
			return errors.New("invalid JELEE_DEV_MODE")
		}
	}
	if value, ok := lookup(devmode.EnvEnvironment); ok {
		c.Environment = value
	}
	return nil
}

func (c DevConfig) Validate() error {
	if c.TTLMinutes < 0 || time.Duration(c.TTLMinutes)*time.Minute > devmode.MaxTTL {
		return errors.New("dev.ttlMinutes must be between 0 and 1440")
	}
	return nil
}

// TTL is the session lifetime.
func (c DevConfig) TTL() time.Duration {
	if c.TTLMinutes == 0 {
		return devmode.DefaultTTL
	}
	return time.Duration(c.TTLMinutes) * time.Minute
}

// Inputs are this process's own gate facts.
func (c DevConfig) Inputs() devmode.Inputs {
	return devmode.Inputs{Environment: c.Environment, EnvFlag: c.EnvFlag, ConfigEnabled: c.Enabled}
}

// Capable reports whether this process meets its own thresholds (both
// switches set, not production). Only a capable process mounts developer
// routes or applies a session.
func (c DevConfig) Capable() bool {
	in := c.Inputs()
	in.TokenVerified, in.LoopbackEntry = true, true
	return devmode.Evaluate(in).Allowed
}

// Production reports whether JELEE_ENV marks a production deployment.
func (c DevConfig) Production() bool { return c.Inputs().IsProduction() }
