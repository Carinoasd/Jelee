package runtime

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"go.uber.org/fx"
)

// logLevels is the part of the log router verbose developer logging uses.
type logLevels interface {
	SetLevel(component string, level slog.Level) error
	Level(component string) slog.Level
}

// NewWithLogs is New with the log router, so the developer mode
// debug_verbose_logging toggle can raise and restore the global level.
func NewWithLogs(cfg config.Config, logs *logging.Router) *fx.App {
	logger := logs.Logger()
	sweepStartupTemporaries(context.Background(), cfg, logger)
	life := newLifetime(logger)
	life.levels = logs
	return newWithLifetime(cfg, logger, life)
}

// devAvailable lists the toggles this process wires into its subsystems
// (docs/developer-mode.md lists the rest and why they are not wired yet).
func devAvailable(c config.Config, verbose bool) []devmode.Toggle {
	available := []devmode.Toggle{
		devmode.RelaxLoginRateLimit, devmode.RelaxAPIRateLimit, devmode.RelaxPlaybackConcurrency, devmode.RelaxBandwidthLimit,
		devmode.RelaxPermissionStrict, devmode.RelaxHostStrict, devmode.RelaxClientUABlock,
		devmode.DebugSQLLogging, devmode.DebugBodyLogging, devmode.DebugPprof,
	}
	if c.EnableWebhooks {
		available = append(available, devmode.RelaxSSRFStrict)
	}
	if verbose {
		available = append(available, devmode.DebugVerboseLogging)
	}
	return available
}

// newDevController builds the controller every instance runs, capable or
// not: an incapable instance never applies a session and switches off one
// it finds in shared storage (G45.1, G45.2). Startup reconciles the stored
// session before the instance serves (G45.7).
func newDevController(c config.Config, store *postgres.Store, l *slog.Logger, lifetime *lifetime) (*devmode.Controller, error) {
	if lifetime.levels != nil {
		lifetime.baseLevel = lifetime.levels.Level(logging.GlobalComponent)
	}
	controller, err := devmode.NewController(devmode.ControllerOptions{Store: store, Local: c.Dev.Inputs(), TTL: c.Dev.TTL(), Persist: c.Dev.PersistAcrossRestart,
		Available: devAvailable(c, lifetime.levels != nil), Logger: l, OnChange: lifetime.devChanged})
	if err != nil {
		return nil, err
	}
	if lifetime.queryLog != nil {
		lifetime.queryLog.SetEnabled(func() bool { return controller.Effective(devmode.DebugSQLLogging) })
	}
	lifetime.dev = controller
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = controller.Startup(ctx); err != nil {
		return nil, err
	}
	return controller, nil
}

// devChanged applies the effective session to process-wide settings: the
// global log level follows debug_verbose_logging and returns to the
// configured level when the toggle or the session ends.
func (l *lifetime) devChanged(st devmode.Status) {
	if l.levels == nil {
		return
	}
	level := l.baseLevel
	if st.Active && slices.Contains(st.Toggles, devmode.DebugVerboseLogging) {
		level = slog.LevelDebug
	}
	if l.levels.Level(logging.GlobalComponent) != level {
		_ = l.levels.SetLevel(logging.GlobalComponent, level)
		l.logger.Warn("developer mode changed the global log level", "component", "devmode", "level", level.String())
	}
}
