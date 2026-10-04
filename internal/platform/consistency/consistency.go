// Package consistency wires the data consistency checker (G50.3) from the
// service configuration, for the job worker and the command line alike.
package consistency

import (
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// Repository is everything the checker reads and repairs.
type Repository interface {
	app.ConsistencyRepository
	app.ConsistencyFixer
}

// NewChecker builds a checker over repository. File probes go through
// os.Root at each library root; image variant probes are enabled only when
// the image store is configured; report paths follow the logging path mode.
func NewChecker(cfg config.Config, repository Repository) (*app.ConsistencyChecker, error) {
	var variants app.ConsistencyVariantProber
	if cfg.EnableImages && cfg.Images.StoreRoot != "" {
		prober, err := imageadapter.NewVariantProber(cfg.Images.StoreRoot)
		if err != nil {
			return nil, err
		}
		variants = prober
	}
	paths := logging.NewRedactor(logging.IPMode(cfg.Logging.IPMode), logging.PathMode(cfg.Logging.PathMode), cfg.Logging.PathRoots)
	return app.NewConsistencyChecker(repository, scan.ConsistencyProber{}, variants, paths, repository)
}

// Template returns the run bounds of the configuration. Origin, job,
// library and fix mode are set per run.
func Template(cfg config.Config) app.ConsistencyOptions {
	return app.ConsistencyOptions{
		StatBudget: cfg.Jobs.ConsistencyStatBudget, WatchSample: cfg.Jobs.ConsistencyWatchSample,
		SessionRetention: cfg.Playback.Retention(),
		DailyRetention:   cfg.Stats.Retention(),
		CallTimeout:      CallTimeout,
	}
}

// CallTimeout bounds every page query of a run; a page reads at most a few
// thousand rows through an index.
const CallTimeout = 15 * time.Second
