package runtime

import (
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

var _ jobworker.BackgroundLocker = (*postgres.Store)(nil)

// backgroundJobs returns the job executor's background lane (G41.1),
// building it on first use: webhook delivery, the watch statistics roll-up,
// the playback progress flush and subtitle OCR run there with the store's
// cluster-wide locks and the shared budget, instead of goroutines of their
// own. The lane
// starts with the workers; its metrics are exported with the job metrics.
func (l *lifetime) backgroundJobs(store *postgres.Store, budget *resources.Budget) (*jobworker.Background, error) {
	if l.background != nil {
		return l.background, nil
	}
	var work app.WorkBudget
	if budget != nil {
		work = budget
	}
	var locker jobworker.BackgroundLocker
	if store != nil {
		locker = store
	}
	lane, err := jobworker.NewBackground(jobworker.BackgroundOptions{Budget: work, Locker: locker, Logger: l.logger})
	if err != nil {
		return nil, err
	}
	l.background = lane
	return lane, nil
}

// registerBackground adds tasks to the lane.
func (l *lifetime) registerBackground(store *postgres.Store, budget *resources.Budget, tasks ...app.BackgroundTask) error {
	lane, err := l.backgroundJobs(store, budget)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := lane.Register(task); err != nil {
			return err
		}
	}
	return nil
}

// joinProgressFlush waits for the progress flush job, which stops with the
// lifetime context, before the final flush.
func (l *lifetime) joinProgressFlush() {
	if l.background != nil {
		l.background.Wait(domain.JobPlaybackProgressFlush)
	}
}

// joinBackground waits for every background job. The webhook delivery job
// finishes or abandons its in-flight attempts within its stop grace; the
// abandoned ones are sent again after their lease expires.
func (l *lifetime) joinBackground() {
	if l.background != nil {
		l.background.Wait()
	}
}
