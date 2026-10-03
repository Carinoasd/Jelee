package runtime

import (
	"context"
	"log/slog"
	"os"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

// sweepStartupTemporaries removes owner-tagged temporary objects left by
// crashed Jelee processes before services create new ones. It is best effort:
// failures are logged as counts and fixed codes, never paths, and never stop
// startup. Each root has its own entry, removal and time limits.
func sweepStartupTemporaries(ctx context.Context, cfg config.Config, logger *slog.Logger) {
	sweep := func(area, root string, kinds ...scratch.Kind) {
		report, err := scratch.Sweep(ctx, root, kinds, scratch.DefaultLimits())
		if err == nil && report.Removed == 0 && report.Failed == 0 && !report.Incomplete {
			return
		}
		attributes := []any{"area", area, "removed", report.Removed, "failed", report.Failed, "kept_alive", report.Alive, "kept_unknown", report.Unknown, "incomplete", report.Incomplete}
		if err != nil {
			logger.Warn("startup temporary sweep skipped", append(attributes, "reason", err.Error())...)
			return
		}
		if report.Failed > 0 || report.Incomplete {
			logger.Warn("startup temporary sweep incomplete", attributes...)
			return
		}
		logger.Info("startup temporary sweep removed leftovers", attributes...)
	}
	sweep("process", os.TempDir(), scratch.ServiceProbe, scratch.ServiceIgnore, scratch.ProbeCheck)
	if cfg.EnableImages && cfg.Images.TempRoot != "" {
		sweep("images", cfg.Images.TempRoot, scratch.ImageStage)
	}
}
