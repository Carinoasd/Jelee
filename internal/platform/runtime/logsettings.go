package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Shared logging settings (G46.2, G46.9). An administrator changes level
// overrides and log retention through /api/v1/admin/logging on one
// instance, which applies them at once; every other instance picks the
// stored row up within logSettingsInterval. Overrides expire on each
// instance's own clock, so a temporary DEBUG ends on time even while the
// database is unreachable.

const (
	logSettingsInterval = 15 * time.Second
	// logPruneInterval applies the age and size limits between rotations.
	logPruneInterval = time.Hour
	// logSettingsReadTimeout bounds one read of the settings row.
	logSettingsReadTimeout = 5 * time.Second
)

// logSettingsSource is the storage side: one read of the singleton row.
type logSettingsSource interface {
	LogSettings(ctx context.Context) (domain.LogSettings, error)
}

// logSettingsTarget is the log router side.
type logSettingsTarget interface {
	ApplySettings(domain.LogSettings)
	PruneFiles() error
}

type logSettingsSync struct {
	source   logSettingsSource
	target   logSettingsTarget
	logger   *slog.Logger
	interval time.Duration
	prune    time.Duration
	failing  bool
}

func newLogSettingsSync(source logSettingsSource, target logSettingsTarget, logger *slog.Logger) *logSettingsSync {
	return &logSettingsSync{source: source, target: target, logger: logger, interval: logSettingsInterval, prune: logPruneInterval}
}

// refresh reads the row and applies it. A failure keeps the settings in
// force and is logged once until a read succeeds again.
func (s *logSettingsSync) refresh(ctx context.Context) bool {
	readCtx, cancel := context.WithTimeout(ctx, logSettingsReadTimeout)
	settings, err := s.source.LogSettings(readCtx)
	cancel()
	if err != nil {
		if ctx.Err() == nil && !s.failing {
			s.logger.Warn("log settings could not be read; keeping the settings in force", "component", "system", "code", "log_settings_unavailable")
		}
		s.failing = true
		return false
	}
	s.failing = false
	s.target.ApplySettings(settings)
	return true
}

func (s *logSettingsSync) pruneFiles() {
	if err := s.target.PruneFiles(); err != nil {
		s.logger.Warn("expired log files could not be removed", "component", "system", "code", "log_prune_failed")
	}
}

// Run refreshes on the interval and prunes log files hourly until ctx ends.
func (s *logSettingsSync) Run(ctx context.Context) {
	refresh := time.NewTicker(s.interval)
	defer refresh.Stop()
	prune := time.NewTicker(s.prune)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			s.refresh(ctx)
		case <-prune.C:
			s.pruneFiles()
		}
	}
}
