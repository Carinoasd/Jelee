package jobs

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogSyncOptions enables claiming catalog_sync jobs. Every batch is one
// fenced transaction that also stores its checkpoint, so a replacement lease
// continues where the last committed batch stopped.
type CatalogSyncOptions struct {
	Repository app.CatalogSyncExecutionRepository
}

func (r *Runner) executeCatalogSync(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.CatalogSync
	if options == nil {
		return domain.ErrScanUnavailable, false
	}
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var done bool
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			done, err = options.Repository.AdvanceCatalogSync(c, lease)
			return err
		})
		if err != nil {
			return err, true
		}
		if done {
			return nil, false
		}
	}
}
