// Package devmode owns the developer-mode state machine described by G45. The
// State machine is pure in-memory policy; the Controller shares one session
// between every instance and the CLI through a Store (PostgreSQL in
// production) and never reads HTTP requests or runs commands itself.
// Adapters translate their own facts into Inputs, and Stores turn events into
// audit records. See docs/developer-mode.md.
package devmode

import "strings"

// Environment variable names recognised by G45.1/G45.2.
const (
	EnvDevMode     = "JELEE_DEV_MODE"
	EnvEnvironment = "JELEE_ENV"

	// ProductionEnvironment is the JELEE_ENV value that forcibly disables
	// developer mode (G45.2).
	ProductionEnvironment = "production"
)

// Requirement names one of the four independent G45.1 thresholds.
type Requirement string

const (
	RequireEnvFlag       Requirement = "env_flag"       // JELEE_DEV_MODE=true
	RequireConfigEnabled Requirement = "config_enabled" // dev.enabled: true
	RequireCLIToken      Requirement = "cli_token"      // jelee-cli devmode enable --token <one-time token>
	RequireLoopbackEntry Requirement = "loopback_entry" // loopback-only magic path or dedicated port
)

// Requirements lists every threshold in evaluation order.
func Requirements() []Requirement {
	return []Requirement{RequireEnvFlag, RequireConfigEnabled, RequireCLIToken, RequireLoopbackEntry}
}

// Inputs are the facts an adapter has already established. Every field
// defaults to the closed state.
type Inputs struct {
	// Environment is the raw JELEE_ENV value.
	Environment string
	// ProductionImage reports a production-tagged container image, which
	// disables developer mode by default (G45.2).
	ProductionImage bool
	// EnvFlag is true only when JELEE_DEV_MODE is exactly "true".
	EnvFlag bool
	// ConfigEnabled mirrors dev.enabled in the configuration file.
	ConfigEnabled bool
	// TokenVerified is the result of verifying the one-time CLI token. The
	// token itself never enters this package.
	TokenVerified bool
	// LoopbackEntry is true only when the request arrived through the
	// loopback-only developer entry. URL-based enabling from a non-loopback
	// address must leave this false.
	LoopbackEntry bool
}

// ReadEnvironment fills the environment-derived Inputs fields from lookup
// (normally os.LookupEnv). Only the literal "true" (case-insensitive) counts.
func ReadEnvironment(lookup func(string) (string, bool)) Inputs {
	var in Inputs
	if lookup == nil {
		return in
	}
	if v, ok := lookup(EnvEnvironment); ok {
		in.Environment = v
	}
	if v, ok := lookup(EnvDevMode); ok {
		in.EnvFlag = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return in
}

// IsProduction reports whether the inputs carry a production marker.
func (in Inputs) IsProduction() bool {
	return in.ProductionImage || strings.EqualFold(strings.TrimSpace(in.Environment), ProductionEnvironment)
}

// Decision is the outcome of Evaluate.
type Decision struct {
	Allowed bool
	// Production is true when a production marker forced the denial; dev
	// configuration is ignored entirely in that case.
	Production bool
	// Alert is true when the denial must raise an operator alert (G45.2).
	Alert bool
	// Missing lists unmet thresholds in Requirements order. It is empty for
	// production denials so that dev configuration is not inspected.
	Missing []Requirement
}

// Evaluate applies the G45.1/G45.2 gate. It is a pure function: all four
// thresholds must hold, and a production marker always denies with an alert
// regardless of the other inputs.
func Evaluate(in Inputs) Decision {
	if in.IsProduction() {
		return Decision{Production: true, Alert: true}
	}
	var missing []Requirement
	for _, r := range Requirements() {
		if !in.satisfies(r) {
			missing = append(missing, r)
		}
	}
	return Decision{Allowed: len(missing) == 0, Missing: missing}
}

func (in Inputs) satisfies(r Requirement) bool {
	switch r {
	case RequireEnvFlag:
		return in.EnvFlag
	case RequireConfigEnabled:
		return in.ConfigEnabled
	case RequireCLIToken:
		return in.TokenVerified
	case RequireLoopbackEntry:
		return in.LoopbackEntry
	}
	return false
}
