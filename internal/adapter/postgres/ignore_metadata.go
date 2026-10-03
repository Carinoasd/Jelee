package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Metadata may consume enabled inventory only after comparison has frozen and
// classified the complete scan. This does not authorize baseline publication.
func requireMetadataInventory(ctx context.Context, tx pgx.Tx, id string) error {
	request, err := loadExecutionIgnoreRequest(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if request == nil {
		return nil
	}
	var complete bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_comparisons WHERE job_id=$1::uuid AND completed)`, id).Scan(&complete); err != nil {
		return storageError(err)
	}
	if !complete {
		return domain.ErrIgnoreUnavailable
	}
	epoch, live, err := inventoryEpoch(ctx, tx, domain.JobLease{Job: domain.Job{ID: id}})
	if err != nil {
		return err
	}
	if epoch == nil || *epoch != live {
		return domain.ErrInventoryInvalidated
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_comparisons c JOIN job_ignore_manifests m ON m.job_id=c.job_id JOIN jobs j ON j.id=c.job_id JOIN libraries b ON b.id=j.library_id WHERE c.job_id=$1::uuid AND c.completed AND c.inventory_generation=$2 AND m.inventory_generation=$2 AND NOT m.invalidated AND c.baseline_revision=b.inventory_baseline_revision AND (NOT $3 OR EXISTS(SELECT 1 FROM job_ignore_legacy_manifests n WHERE n.job_id=c.job_id AND n.inventory_generation=$2 AND NOT n.invalidated)))`, id, live, request.Intent.Mode == domain.IgnoreModeFamily).Scan(&valid)
	if err != nil {
		return storageError(err)
	}
	if !valid {
		return domain.ErrInventoryInvalidated
	}
	return nil
}

func requireMetadataNFOFinished(ctx context.Context, tx pgx.Tx, id string) error {
	if err := requireMetadataInventory(ctx, tx, id); err != nil {
		return err
	}
	return requireNFOPhaseFinished(ctx, tx, id)
}
