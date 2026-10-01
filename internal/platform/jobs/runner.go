// Package jobs owns bounded, service-lifetime inventory workers. PostgreSQL
// owns durable scheduling, authorization, checkpoints and lease fencing.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Clock controls worker timers. Database lease expiry always uses the database
// clock; this interface cannot extend a lease or bypass its fencing checks.
type Clock interface{ NewTimer(time.Duration) Timer }
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realClock struct{}
type realTimer struct{ *time.Timer }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
func (t realTimer) C() <-chan time.Time          { return t.Timer.C }

type Options struct {
	Ignore             *IgnoreOptions
	FamilyIgnore       *FamilyIgnoreOptions
	Workers            int
	PollInterval       time.Duration
	LeaseDuration      time.Duration
	DBOperationTimeout time.Duration
	MaxJobRuntime      time.Duration
	Owner              string
	Clock              Clock
	Probe              *ProbeOptions
	NFO                *NFOOptions
}

func DefaultOptions() Options {
	return Options{Workers: 2, PollInterval: 250 * time.Millisecond, LeaseDuration: 30 * time.Second, DBOperationTimeout: 2 * time.Second, MaxJobRuntime: time.Hour}
}

var (
	errCancelRequested = errors.New("persisted job cancellation")
	errHeartbeatFailed = errors.New("job heartbeat failed")
)

type Runner struct {
	cancellationMu       sync.Mutex
	runningCancellations map[string]runningCancellation
	repository           app.JobExecutionRepository
	scanner              app.InventoryScanner
	options              Options
	logger               *slog.Logger
	mu                   sync.Mutex
	started              bool
	cancel               context.CancelFunc
	done                 chan struct{}
	probeRepository      app.ProbeExecutionRepository
	probeGate            chan struct{}
	probeIdentity        string
	probeUnavailable     atomic.Bool
	nfoRepository        app.NFOExecutionRepository
	nfoGate              chan struct{}
	nfoIdentity          domain.NFOIdentity
	nfoUnavailable       atomic.Bool
}

func New(repository app.JobExecutionRepository, scanner app.InventoryScanner, opts Options, logger *slog.Logger) (*Runner, error) {
	if repository == nil || scanner == nil || logger == nil || opts.Workers < 1 || opts.Workers > 8 ||
		opts.PollInterval < 100*time.Millisecond || opts.PollInterval > time.Minute ||
		opts.LeaseDuration < 10*time.Second || opts.LeaseDuration > 5*time.Minute ||
		opts.DBOperationTimeout <= 0 || opts.DBOperationTimeout >= opts.LeaseDuration/3 ||
		opts.MaxJobRuntime < time.Minute || opts.MaxJobRuntime > 24*time.Hour || opts.Owner != "" && !domain.ValidID(opts.Owner) {
		return nil, domain.ErrInvalid
	}
	if opts.Owner == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, errors.New("cannot generate job worker identity")
		}
		id[6] = id[6]&0x0f | 0x40
		id[8] = id[8]&0x3f | 0x80
		raw := hex.EncodeToString(id[:])
		opts.Owner = raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Ignore != nil {
		if _, ok := repository.(app.IgnoreExecutionRepository); !ok {
			return nil, domain.ErrInvalid
		}
		i := *opts.Ignore
		if i.Repository == nil || i.Scanner == nil || i.Observer == nil {
			return nil, domain.ErrInvalid
		}
		opts.Ignore = &i
	}
	if opts.FamilyIgnore != nil {
		if _, ok := repository.(app.IgnoreExecutionRepository); !ok {
			return nil, domain.ErrInvalid
		}
		if _, ok := repository.(app.FamilyIgnoreExecutionRepository); !ok {
			return nil, domain.ErrInvalid
		}
		i := *opts.FamilyIgnore
		if i.Repository == nil || i.Scanner == nil {
			return nil, domain.ErrInvalid
		}
		opts.FamilyIgnore = &i
	}
	r := &Runner{repository: repository, scanner: scanner, options: opts, logger: logger}
	if err := r.configureProbe(); err != nil {
		return nil, err
	}
	if err := r.configureNFO(); err != nil {
		return nil, err
	}
	return r, nil
}

// Start must receive a service-lifetime context, not an HTTP request or fx's
// short-lived OnStart context. Stop owns cancellation when ctx is Background.
// A Runner may be started once; repeated Stop calls are safe.
func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return errors.New("job runner already started")
	}
	life, cancel := context.WithCancel(ctx)
	r.started, r.cancel, r.done = true, cancel, make(chan struct{})
	var remaining atomic.Int32
	remaining.Store(int32(r.options.Workers))
	for i := 0; i < r.options.Workers; i++ {
		go func() {
			defer func() {
				if remaining.Add(-1) == 0 {
					close(r.done)
				}
			}()
			r.work(life)
		}()
	}
	return nil
}

// Stop cancels claims and scans, then waits for heartbeat/worker shutdown and
// bounded checkpoint release. A deadline error means joining is incomplete;
// callers must not report successful shutdown or close the store prematurely.
func (r *Runner) Stop(ctx context.Context) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) work(ctx context.Context) {
	turn := 0
	for ctx.Err() == nil {
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		var lease domain.JobLease
		var err error
		if r.nfoRepository != nil {
			lease, err = r.nfoRepository.ClaimJobWithCapabilities(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, domain.ScanCapabilities{Probe: r.probeAvailable(), NFO: r.nfoAvailable(), Ignore: r.options.Ignore != nil, FamilyIgnore: r.familyIgnoreAvailable()})
		} else if capable, ok := r.repository.(stagesClaimer); ok {
			lease, err = capable.ClaimJobWithCapabilities(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, domain.ScanCapabilities{Probe: r.probeAvailable(), Ignore: r.options.Ignore != nil, FamilyIgnore: r.familyIgnoreAvailable()})
		} else if r.probeRepository != nil {
			lease, err = r.probeRepository.ClaimJobWithProbe(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, r.probeAvailable())
		} else if capable, ok := r.repository.(probeClaimer); ok {
			lease, err = capable.ClaimJobWithProbe(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, false)
		} else {
			lease, err = r.repository.ClaimJob(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration)
		}
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, domain.ErrNotFound) {
				r.logger.Warn("job claim unavailable", "component", "jobs", "code", "scan_unavailable")
			}
			if !r.wait(ctx, r.options.PollInterval) {
				return
			}
			continue
		}
		turn = (turn + 1) % 4
		r.run(ctx, lease)
	}
}

func (r *Runner) wait(ctx context.Context, d time.Duration) bool {
	timer := r.options.Clock.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C():
		return ctx.Err() == nil
	}
}

// monitor is the sole per-job helper goroutine. Its timers are stopped and it
// is joined before any terminal/release transaction is attempted.
func (r *Runner) monitor(ctx context.Context, lease domain.JobLease, cancelJob context.CancelCauseFunc, started, done chan struct{}) {
	heartbeat := r.options.Clock.NewTimer(r.heartbeatInterval())
	runtime := r.options.Clock.NewTimer(r.options.MaxJobRuntime)
	reader, _ := r.repository.(app.JobCancellationReader)
	var cancellation Timer
	var cancellationReady <-chan time.Time
	if reader != nil {
		cancellation = r.options.Clock.NewTimer(time.Second)
		cancellationReady = cancellation.C()
	}
	close(started)
	defer close(done)
	defer func() {
		heartbeat.Stop()
		runtime.Stop()
		if cancellation != nil {
			cancellation.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-runtime.C():
			cancelJob(context.DeadlineExceeded)
			return
		case <-cancellationReady:
			// Poll committed flags without increasing heartbeat writes. The
			// existing monitor owns this timer; no extra goroutine is started.
			dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
			requested, err := reader.ReadJobCancellation(dbCtx, lease)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if errors.Is(err, domain.ErrJobLeaseLost) {
					cancelJob(domain.ErrJobLeaseLost)
				} else {
					cancelJob(errHeartbeatFailed)
				}
				return
			}
			if requested {
				cancelJob(errCancelRequested)
				return
			}
			cancellation.Stop()
			cancellation = r.options.Clock.NewTimer(time.Second)
			cancellationReady = cancellation.C()
		case <-heartbeat.C():
			dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
			requested, err := r.repository.HeartbeatJob(dbCtx, lease, r.options.LeaseDuration)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if errors.Is(err, domain.ErrJobLeaseLost) {
					cancelJob(domain.ErrJobLeaseLost)
				} else {
					cancelJob(errHeartbeatFailed)
				}
				return
			}
			if requested {
				cancelJob(errCancelRequested)
				return
			}
			heartbeat.Stop()
			heartbeat = r.options.Clock.NewTimer(r.heartbeatInterval())
		}
	}
}

func (r *Runner) run(serviceCtx context.Context, lease domain.JobLease) {
	ctx, cancelJob := context.WithCancelCause(serviceCtx)
	defer cancelJob(nil)
	defer r.registerCancellation(lease, cancelJob)()
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	started, monitored := make(chan struct{}), make(chan struct{})
	go r.monitor(hbCtx, lease, cancelJob, started, monitored)
	<-started
	err, repositoryError := r.execute(ctx, lease)
	stopHeartbeat()
	<-monitored
	cause := context.Cause(ctx)
	if errors.Is(err, domain.ErrJobLeaseLost) || errors.Is(err, domain.ErrProbeLeaseLost) || errors.Is(cause, domain.ErrJobLeaseLost) || errors.Is(cause, errHeartbeatFailed) {
		r.logger.Warn("job ownership could not be retained", "component", "jobs", "taskId", lease.Job.ID, "code", "job_lease_lost")
		return
	}
	// Cleanup survives service cancellation but always has its own short bound.
	if serviceCtx.Err() != nil && !errors.Is(cause, errCancelRequested) {
		dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		defer cancelDB()
		if err := r.repository.ReleaseJob(dbCtx, lease); err != nil {
			r.logPersistenceFailure(lease)
		}
		return
	}
	state, code := domain.JobSucceeded, ""
	var aborted *probeAbort
	var nfoAborted *nfoAbort
	if errors.As(err, &aborted) && cause == nil && aborted.persist {
		abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		abortErr := r.probeRepository.AbortProbeRequest(abortCtx, lease, aborted.code)
		cancelAbort()
		if errors.Is(abortErr, context.Canceled) {
			cause = errCancelRequested
		} else if abortErr != nil {
			r.logPersistenceFailure(lease)
			return
		}
	}
	if errors.As(err, &nfoAborted) && cause == nil && nfoAborted.persist {
		abortCtx, cancelAbort := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		abortErr := r.nfoRepository.AbortNFORequest(abortCtx, lease, nfoAborted.code)
		cancelAbort()
		if errors.Is(abortErr, context.Canceled) {
			cause = errCancelRequested
		} else if abortErr != nil {
			r.logPersistenceFailure(lease)
			return
		}
	}
	switch {
	case errors.Is(cause, errCancelRequested):
		state = domain.JobCancelled
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded) && !repositoryError:
		state, code = domain.JobFailed, "job_timeout"
	case errors.Is(err, context.Canceled):
		state = domain.JobCancelled
	case err != nil:
		state = domain.JobFailed
		switch {
		case errors.Is(err, domain.ErrScanLimit):
			code = "scan_limit"
		case errors.Is(err, domain.ErrScanUnavailable):
			code = "scan_unavailable"
		case errors.Is(err, domain.ErrScanIO):
			code = "scan_io"
		case aborted != nil || nfoAborted != nil:
			code = "scan_unavailable"
		case repositoryError:
			code = "scan_unavailable"
		default:
			code = "scan_io"
		}
	}
	dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
	defer cancelDB()
	finishErr := r.finishJob(dbCtx, lease, state, code)
	if state == domain.JobSucceeded && (errors.Is(finishErr, domain.ErrInventoryInvalidated) || errors.Is(finishErr, domain.ErrNFOInvalidated) || errors.Is(finishErr, domain.ErrNFOIdentityMismatch)) {
		// The success transaction was rolled back. Persist the terminal safe
		// failure without replacing the accepted inventory/image baseline.
		cancelDB()
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		state, code = domain.JobFailed, "scan_unavailable"
		finishErr = r.repository.FinishJob(finishCtx, lease, state, code)
		cancelFinish()
	}
	if state == domain.JobSucceeded && errors.Is(finishErr, domain.ErrConflict) {
		// Cancellation can commit after the last heartbeat but before Finish.
		// Confirm the persistent flag under the same fence before changing the
		// result. Other conflicts or lost ownership must await lease recovery.
		cancelDB()
		checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		requested, err := r.repository.HeartbeatJob(checkCtx, lease, r.options.LeaseDuration)
		cancelCheck()
		if err != nil || !requested {
			r.logPersistenceFailure(lease)
			return
		}
		state = domain.JobCancelled
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		finishErr = r.repository.FinishJob(finishCtx, lease, state, "")
		cancelFinish()
	}
	if finishErr != nil {
		r.logPersistenceFailure(lease)
		return
	}
	r.logger.Info("inventory job completed", "component", "jobs", "taskId", lease.Job.ID, "state", state, "code", code)
}

func (r *Runner) logPersistenceFailure(lease domain.JobLease) {
	r.logger.Warn("job state could not be persisted", "component", "jobs", "taskId", lease.Job.ID, "code", "scan_unavailable")
}

func (r *Runner) executeInventory(ctx context.Context, lease domain.JobLease) (result error, repositoryError bool) {
	// Scanner bugs cannot strand the monitor or expose panic payloads in logs.
	defer func() {
		if recover() != nil {
			result, repositoryError = domain.ErrScanIO, false
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		directory, err := r.repository.NextScanDirectory(dbCtx, lease)
		cancel()
		if errors.Is(err, domain.ErrNotFound) {
			return nil, false
		}
		if err != nil {
			return err, true
		}
		var callbackError error
		callbackFailed, completed := false, false
		err = r.scanner.ScanDirectory(ctx, directory, func(batch domain.ScanBatch) error {
			if callbackError != nil {
				return callbackError
			}
			if err := ctx.Err(); err != nil {
				callbackError = err
				return err
			}
			if completed {
				callbackError = domain.ErrScanIO
				return callbackError
			}
			if len(batch.Entries)+len(batch.Directories) > domain.ScanBatchMaxEntries || batch.Skipped < 0 {
				callbackError = domain.ErrScanLimit
				return callbackError
			}
			dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
			defer cancel()
			err := r.repository.SaveScanBatch(dbCtx, lease, directory, batch)
			callbackError = err
			callbackFailed = err != nil
			completed = err == nil && batch.Done
			return err
		})
		if callbackError != nil {
			return callbackError, callbackFailed
		}
		if err != nil {
			return err, callbackFailed
		}
		if !completed {
			return domain.ErrScanIO, false
		}
	}
}
