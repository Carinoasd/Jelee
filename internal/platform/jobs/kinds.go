package jobs

import (
	"context"
	"errors"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// KindWorker runs one claimed job under the job context, so the monitor's
// heartbeat, cancellation and runtime limit stop it.
type KindWorker interface {
	Run(ctx context.Context, lease domain.JobLease) error
}

// KindOptions claims one further job kind (maintenance jobs, G13.6, G14.7).
// Finish makes the job terminal under its lease fence; FailureCode is the
// kind's error code for every failure but a timeout.
type KindOptions struct {
	Worker      KindWorker
	Finish      func(ctx context.Context, lease domain.JobLease, state, code string) error
	FailureCode string
}

var kindFailureCodes = map[string]string{
	domain.JobSubtitleCacheCleanup:    "subtitle_cache_cleanup_failed",
	domain.JobBackupReminder:          "backup_reminder_failed",
	domain.JobExternalMetadataRemoval: "external_metadata_removal_failed",
}

func validKindOptions(kind string, value KindOptions) bool {
	return kindFailureCodes[kind] != "" && kindFailureCodes[kind] == value.FailureCode && value.Worker != nil && value.Finish != nil
}

// kindNames lists the claimable further kinds in a stable order.
func (r *Runner) kindNames() []string {
	names := make([]string, 0, len(r.options.Kinds))
	for kind := range r.options.Kinds {
		names = append(names, kind)
	}
	slices.Sort(names)
	return names
}

// executeKind runs a further kind's worker. Errors other than cancellation,
// timeouts and a lost lease are persisted as the kind's failure.
func (r *Runner) executeKind(ctx context.Context, lease domain.JobLease, kind KindOptions) (storage bool, err error) {
	err = kind.Worker.Run(ctx, lease)
	switch {
	case err == nil:
		return false, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, domain.ErrJobLeaseLost), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false, err
	}
	return true, err
}
