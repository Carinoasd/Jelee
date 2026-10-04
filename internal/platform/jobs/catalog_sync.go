package jobs

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogSyncOptions enables claiming catalog_sync jobs. Every batch is one
// fenced transaction that also stores its checkpoint, so a replacement lease
// continues where the last committed batch stopped.
//
// With Sidecars and Inspector set, the job ends with a pass over the
// library's external tracks that still lack a fingerprint: each file gets a
// bounded read (edge fingerprint, text subtitle charset) outside any
// transaction, and the results are stored in fenced pages.
type CatalogSyncOptions struct {
	Repository app.CatalogSyncExecutionRepository
	Sidecars   app.SidecarInspectionRepository
	Inspector  app.SidecarInspector
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
			return r.inspectSidecars(ctx, lease)
		}
	}
}

// inspectSidecars walks the uninspected tracks once, by ID. A file that
// cannot be read now (gone, replaced, changed) is skipped and stays
// uninspected; the next scan replaces or removes its row.
func (r *Runner) inspectSidecars(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.CatalogSync
	if options.Sidecars == nil || options.Inspector == nil {
		return nil, false
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var page []domain.SidecarInspectionTarget
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			page, err = options.Sidecars.NextSidecarInspections(c, lease, after, domain.SidecarInspectionBatch)
			return err
		})
		if err != nil {
			return err, true
		}
		if len(page) == 0 {
			return nil, false
		}
		results := make([]domain.SidecarInspection, 0, len(page))
		for _, target := range page {
			value, err := options.Inspector.InspectSidecar(ctx, target)
			if err := ctx.Err(); err != nil {
				return err, false
			}
			if err == nil {
				results = append(results, value)
			}
		}
		if len(results) > 0 {
			err = r.ignoreDB(ctx, func(c context.Context) error {
				_, err := options.Sidecars.RecordSidecarInspections(c, lease, results)
				return err
			})
			if err != nil {
				return err, true
			}
		}
		if len(page) < domain.SidecarInspectionBatch {
			return nil, false
		}
		after = page[len(page)-1].ID
	}
}
