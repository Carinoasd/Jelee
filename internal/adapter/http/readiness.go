package httpapi

import (
	"context"
)

// WithReadiness adds dependency states to /readyz (G50.5). The function
// returns fixed codes such as "ok", "current" or "unavailable"; anything
// that is not a short lowercase code is dropped, so a version, count,
// address or error text can never reach an unauthenticated caller.
func WithReadiness(states func(context.Context) map[string]string) Option {
	return func(s *Server) { s.readiness = states }
}

// readinessDetails returns {"checks": {...}} or an empty map.
func (s *Server) readinessDetails(ctx context.Context) map[string]any {
	if s.readiness == nil {
		return map[string]any{}
	}
	checks := map[string]string{}
	for name, state := range s.readiness(ctx) {
		if readinessCode(name) && readinessCode(state) {
			checks[name] = state
		}
	}
	if len(checks) == 0 {
		return map[string]any{}
	}
	return map[string]any{"checks": checks}
}

func readinessCode(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' {
			return false
		}
	}
	return true
}
