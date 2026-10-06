package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/go-chi/chi/v5"
)

// Logging administration (G46.2, G46.9, G46.10). Administrators read and
// change the runtime log levels, globally or per scope, and the retention of
// ordinary log files and of the audit trail. Changes are stored, audited and
// applied at once on this instance; the others apply the stored row within
// their refresh interval (internal/platform/runtime/logsettings.go).
// docs/logging.md describes the rules.

// loggingStore is the storage side of the logging routes.
type loggingStore interface {
	AdminLogSettings(ctx context.Context, actor domain.Actor) (domain.LogSettings, domain.AuditRetention, error)
	SetLogLevelOverride(ctx context.Context, actor domain.Actor, change domain.LogLevelOverride, reset bool) (domain.LogSettings, error)
	RecordLogLevelRefused(ctx context.Context, actor domain.Actor, component, level string) error
	SetLogRetention(ctx context.Context, actor domain.Actor, r domain.LogRetention) (domain.LogSettings, domain.AuditRetention, error)
}

// loggingControl is the log router of this process.
type loggingControl interface {
	ApplySettings(domain.LogSettings)
	LevelReport() []logging.ScopeLevel
	HasFile() bool
}

// WithLogging mounts the logging administration on store and the process
// log router.
func WithLogging(store loggingStore, control loggingControl) Option {
	return func(s *Server) { s.logStore, s.logControl = store, control }
}

// Level override bounds. In production a DEBUG override must expire
// (G46.8: DEBUG is never on by default there): without ttlSeconds it lasts
// one hour, and it can last at most four.
const (
	logOverrideMaxTTL         = 24 * time.Hour
	logProductionDebugTTL     = time.Hour
	logProductionDebugMaxTTL  = 4 * time.Hour
	logLevelReset             = "reset"
	logGlobalScope            = "global"
	errLogComponentMandatoryC = "log_component_mandatory"
)

// errLogComponentMandatory refuses lowering or switching off the audit or
// security log (G46.10).
var errLogComponentMandatory = errors.New(errLogComponentMandatoryC)

func (s *Server) loggingRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget)
		r.Use(s.authenticate)
		r.Get("/api/v1/admin/logging/levels", s.accountEndpoint(true, false, s.logLevels))
		r.Put("/api/v1/admin/logging/levels", s.accountEndpoint(true, false, s.setLogLevel))
		r.Get("/api/v1/admin/logging/retention", s.accountEndpoint(true, false, s.logRetention))
		r.Put("/api/v1/admin/logging/retention", s.accountEndpoint(true, false, s.setLogRetention))
	})
}

type logOverrideView struct {
	Level     string     `json:"level"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type logScopeView struct {
	Name       string           `json:"name"`
	Aliases    []string         `json:"aliases"`
	Mandatory  bool             `json:"mandatory"`
	Configured *string          `json:"configured"`
	Effective  string           `json:"effective"`
	Override   *logOverrideView `json:"override"`
}

type logLevelsView struct {
	Production         bool           `json:"production"`
	DebugMaxTTLSeconds int64          `json:"debugMaxTtlSeconds"`
	Global             logScopeView   `json:"global"`
	Components         []logScopeView `json:"components"`
}

func scopeView(level logging.ScopeLevel) logScopeView {
	v := logScopeView{Name: level.Component, Aliases: level.Aliases, Mandatory: level.Mandatory, Effective: logging.LevelName(level.Effective)}
	if v.Name == "" {
		v.Name = logGlobalScope
	}
	if v.Aliases == nil {
		v.Aliases = []string{}
	}
	if level.ConfiguredSet {
		name := logging.LevelName(level.Configured)
		v.Configured = &name
	}
	if level.Override != nil {
		v.Override = &logOverrideView{Level: logging.LevelName(level.Override.Level)}
		if !level.Override.ExpiresAt.IsZero() {
			at := level.Override.ExpiresAt.UTC()
			v.Override.ExpiresAt = &at
		}
	}
	return v
}

func (s *Server) logLevelsView() logLevelsView {
	v := logLevelsView{Production: s.cfg.Dev.Production(), Components: []logScopeView{}}
	if v.Production {
		v.DebugMaxTTLSeconds = int64(logProductionDebugMaxTTL / time.Second)
	} else {
		v.DebugMaxTTLSeconds = int64(logOverrideMaxTTL / time.Second)
	}
	for _, level := range s.logControl.LevelReport() {
		if level.Component == logging.GlobalComponent {
			v.Global = scopeView(level)
			continue
		}
		v.Components = append(v.Components, scopeView(level))
	}
	return v
}

// errLoggingUnwired answers when the process did not wire the logging
// administration; the runtime always does with accounts enabled.
var errLoggingUnwired = domain.ErrDatabase

func (s *Server) logLevels(_ http.ResponseWriter, _ *http.Request, _ domain.Actor) (any, int, error) {
	if s.logStore == nil || s.logControl == nil {
		return nil, 0, errLoggingUnwired
	}
	return s.logLevelsView(), http.StatusOK, nil
}

func (s *Server) setLogLevel(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if s.logStore == nil || s.logControl == nil {
		return nil, 0, errLoggingUnwired
	}
	var input struct {
		Component  *string `json:"component"`
		Level      *string `json:"level"`
		TTLSeconds int64   `json:"ttlSeconds"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if input.Component == nil || input.Level == nil || input.TTLSeconds < 0 || time.Duration(input.TTLSeconds)*time.Second > logOverrideMaxTTL {
		return nil, 0, domain.ErrInvalid
	}
	scope := *input.Component
	if scope == logGlobalScope {
		scope = logging.GlobalComponent
	}
	if scope != logging.GlobalComponent {
		resolved, ok := logging.ScopeFor(scope)
		if !ok {
			return nil, 0, domain.ErrInvalid
		}
		if logging.Mandatory(resolved) {
			return nil, 0, s.refuseMandatoryLevel(r, a, resolved, *input.Level)
		}
		scope = resolved
	}
	reset := *input.Level == logLevelReset
	change := domain.LogLevelOverride{Component: scope}
	if !reset {
		level, err := logging.ParseLevel(*input.Level)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
		change.Level = logging.LevelName(level)
		ttl := time.Duration(input.TTLSeconds) * time.Second
		if level <= slog.LevelDebug && s.cfg.Dev.Production() {
			// G46.8: DEBUG in production is temporary and bounded.
			if ttl == 0 {
				ttl = logProductionDebugTTL
			}
			if ttl > logProductionDebugMaxTTL {
				return nil, 0, domain.ErrInvalid
			}
		}
		if ttl > 0 {
			change.ExpiresAt = time.Now().Add(ttl).UTC().Truncate(time.Second)
		}
	} else if input.TTLSeconds != 0 {
		return nil, 0, domain.ErrInvalid
	}
	settings, err := s.logStore.SetLogLevelOverride(r.Context(), a, change, reset)
	if err != nil {
		return nil, 0, err
	}
	s.logControl.ApplySettings(settings)
	logScope := scope
	if logScope == logging.GlobalComponent {
		logScope = logGlobalScope
	}
	attrs := []any{"component", "audit", "event", "log_level_changed", "scope", logScope}
	if !reset {
		attrs = append(attrs, "logLevel", change.Level)
		if !change.ExpiresAt.IsZero() {
			attrs = append(attrs, "expiresAt", change.ExpiresAt.Format(time.RFC3339))
		}
	}
	s.logger.WarnContext(r.Context(), "log level changed by an administrator", attrs...)
	return s.logLevelsView(), http.StatusOK, nil
}

// refuseMandatoryLevel records a refused attempt to change the audit or
// security log (G46.10): a security audit row and an ERROR record in the
// security log, then 409 log_component_mandatory.
func (s *Server) refuseMandatoryLevel(r *http.Request, a domain.Actor, scope, level string) error {
	requested := level
	if requested != logLevelReset {
		if parsed, err := logging.ParseLevel(level); err == nil {
			requested = logging.LevelName(parsed)
		} else {
			requested = "invalid"
		}
	}
	domain.ForceTraceSampling(r.Context())
	s.logger.ErrorContext(r.Context(), "attempt to lower or disable a mandatory log refused", "component", "security", "event", "log_disable_refused", "scope", scope)
	if err := s.logStore.RecordLogLevelRefused(r.Context(), a, scope, requested); err != nil {
		return err
	}
	return errLogComponentMandatory
}

type logRetentionView struct {
	LogDays       int  `json:"logDays"`
	LogMaxTotalMB int  `json:"logMaxTotalMB"`
	AuditDays     int  `json:"auditDays"`
	SecurityDays  int  `json:"securityDays"`
	FileLogging   bool `json:"fileLogging"`
}

func (s *Server) retentionView(settings domain.LogSettings, audit domain.AuditRetention) logRetentionView {
	return logRetentionView{LogDays: settings.LogDays, LogMaxTotalMB: settings.LogMaxTotalMB, AuditDays: audit.AuditDays, SecurityDays: audit.SecurityDays, FileLogging: s.logControl.HasFile()}
}

func (s *Server) logRetention(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if s.logStore == nil || s.logControl == nil {
		return nil, 0, errLoggingUnwired
	}
	settings, audit, err := s.logStore.AdminLogSettings(r.Context(), a)
	if err != nil {
		return nil, 0, err
	}
	return s.retentionView(settings, audit), http.StatusOK, nil
}

func (s *Server) setLogRetention(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if s.logStore == nil || s.logControl == nil {
		return nil, 0, errLoggingUnwired
	}
	var input struct {
		LogDays       *int `json:"logDays"`
		LogMaxTotalMB *int `json:"logMaxTotalMB"`
		AuditDays     *int `json:"auditDays"`
		SecurityDays  *int `json:"securityDays"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if input.LogDays == nil || input.LogMaxTotalMB == nil || input.AuditDays == nil || input.SecurityDays == nil ||
		!domain.ValidLogRetention(*input.LogDays, *input.LogMaxTotalMB) ||
		*input.AuditDays < domain.AuditRetentionMinDays || *input.AuditDays > domain.AuditRetentionMaxDays ||
		*input.SecurityDays < domain.AuditRetentionMinDays || *input.SecurityDays > domain.AuditRetentionMaxDays {
		return nil, 0, domain.ErrInvalid
	}
	settings, audit, err := s.logStore.SetLogRetention(r.Context(), a, domain.LogRetention{LogDays: *input.LogDays, LogMaxTotalMB: *input.LogMaxTotalMB, AuditDays: *input.AuditDays, SecurityDays: *input.SecurityDays})
	if err != nil {
		return nil, 0, err
	}
	s.logControl.ApplySettings(settings)
	return s.retentionView(settings, audit), http.StatusOK, nil
}
