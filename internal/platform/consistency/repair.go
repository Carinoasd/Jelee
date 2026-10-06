package consistency

import (
	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// NewRepairer builds the self-healing repairer (G50.4) over repository with
// the same probes as the checker: file probes through os.Root at each
// library root, image variant probes only when the image store is
// configured, and report paths in the logging path mode. The server-only
// actions are added with WithServer by the running service.
func NewRepairer(cfg config.Config, repository app.RepairRepository) (*app.Repairer, error) {
	var variants app.ConsistencyVariantProber
	if cfg.EnableImages && cfg.Images.StoreRoot != "" {
		prober, err := imageadapter.NewVariantProber(cfg.Images.StoreRoot)
		if err != nil {
			return nil, err
		}
		variants = prober
	}
	paths := logging.NewRedactor(logging.IPMode(cfg.Logging.IPMode), logging.PathMode(cfg.Logging.PathMode), cfg.Logging.PathRoots)
	return app.NewRepairer(repository, scan.ConsistencyProber{}, variants, paths)
}

// RepairTemplate returns the run bounds of the configuration. Action,
// library, origin, actor and dry run are set per run.
func RepairTemplate(cfg config.Config) app.RepairOptions {
	return app.RepairOptions{
		StatBudget:       cfg.Jobs.ConsistencyStatBudget,
		SessionRetention: cfg.Playback.Retention(),
		Policy:           cfg.Jobs.Policy(),
		CallTimeout:      CallTimeout,
	}
}
