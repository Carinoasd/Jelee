package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Workers are already bounded. Queue overflow delays this worker rather than
// creating another goroutine or recording a media failure. The job monitor
// continues renewing ownership and enforcing cancellation/window/runtime.
// Acquire before starting a per-file deadline so congestion is not a corrupt
// file or parser timeout. Callers must never nest shared resource permits.
func (r *Runner) acquireWork(ctx context.Context, class app.WorkClass) (func(), error) {
	if r.options.Budget == nil {
		return func() {}, ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := r.options.Budget.Acquire(ctx, class)
		if !errors.Is(err, domain.ErrResourceBusy) {
			return release, err
		}
		if !r.wait(ctx, r.options.PollInterval) {
			return nil, ctx.Err()
		}
	}
}

func (r *Runner) scanDirectory(ctx context.Context, directory domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer release()
	return r.scanner.ScanDirectory(ctx, directory, emit)
}

func (r *Runner) scanIgnoreDirectory(ctx context.Context, directory domain.ScanDirectory, intent domain.IgnoreIntent, emit func(domain.IgnoreScanBatch) error) error {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer release()
	return r.options.Ignore.Scanner.ScanIgnoreDirectory(ctx, directory, intent, emit)
}

func (r *Runner) scanFamilyIgnoreDirectory(ctx context.Context, directory domain.ScanDirectory, intent domain.IgnoreIntent, emit func(domain.FamilyIgnoreScanBatch) error) error {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return err
	}
	defer release()
	return r.options.FamilyIgnore.Scanner.ScanFamilyIgnoreDirectory(ctx, directory, intent, emit)
}
