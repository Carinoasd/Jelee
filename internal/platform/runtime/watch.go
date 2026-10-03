package runtime

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/app"
)

type watchGroup struct{ worker, watch serviceWorker }

func (w *watchGroup) Start(ctx context.Context) error {
	if err := w.worker.Start(ctx); err != nil {
		return err
	}
	return w.watch.Start(ctx)
}
func (w *watchGroup) Stop(ctx context.Context) error {
	return errors.Join(w.watch.Stop(ctx), w.worker.Stop(ctx))
}
func (w *watchGroup) NotifyJobCancellation(id string) {
	if notifier, ok := w.worker.(app.JobCancellationNotifier); ok {
		notifier.NotifyJobCancellation(id)
	}
}
