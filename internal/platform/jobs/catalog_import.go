package jobs

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type CatalogImportOptions struct {
	Repository app.CatalogImportExecutionRepository
	Verifier   app.InventoryImportVerifier
}

func (r *Runner) executeCatalogImport(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.CatalogImport
	if options == nil {
		return domain.ErrScanUnavailable, false
	}
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var task domain.CatalogImportTask
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			task, err = options.Repository.NextCatalogImport(c, lease)
			return err
		})
		if errors.Is(err, domain.ErrNotFound) {
			return nil, false
		}
		if err != nil {
			return err, true
		}
		if err = options.Verifier.VerifyInventoryImport(ctx, task.Source); err != nil {
			return err, false
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return options.Repository.CommitCatalogImport(c, lease, task) }); err != nil {
			return err, true
		}
	}
}
