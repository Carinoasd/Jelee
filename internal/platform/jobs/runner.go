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
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
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

type WorkWindow interface{ Allows(time.Time) bool }

type Options struct {
	Budget             app.WorkBudget
	Window             WorkWindow
	Now                func() time.Time
	CatalogImport      *CatalogImportOptions
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
	NFOWrite           *NFOWriteOptions
	CatalogSync        *CatalogSyncOptions
	Consistency        *ConsistencyOptions
	// ScanConcurrency bounds directories scanned at once by one inventory job.
	// Zero or one keeps the sequential loop.
	ScanConcurrency int
}

func DefaultOptions() Options {
	return Options{ScanConcurrency: DefaultScanConcurrency, Workers: 2, PollInterval: 250 * time.Millisecond, LeaseDuration: 30 * time.Second, DBOperationTimeout: 2 * time.Second, MaxJobRuntime: time.Hour}
}

// DefaultScanConcurrency is deliberately small: directory reads compete with
// streaming for disk and the shared I/O budget still bounds every slot.
const DefaultScanConcurrency = 2

// MaxScanConcurrency bounds slots per job; each holds one directory claim.
const MaxScanConcurrency = 16

var (
	errCancelRequested = errors.New("persisted job cancellation")
	errHeartbeatFailed = errors.New("job heartbeat failed")
	errWindowClosed    = errors.New("job work window closed")
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
		opts.MaxJobRuntime < time.Minute || opts.MaxJobRuntime > 24*time.Hour || opts.ScanConcurrency < 0 || opts.ScanConcurrency > MaxScanConcurrency || opts.Owner != "" && !domain.ValidID(opts.Owner) {
		return nil, domain.ErrInvalid
	}
	if opts.Window != nil {
		if _, ok := repository.(app.JobPauseRepository); !ok {
			return nil, domain.ErrInvalid
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
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
	if opts.CatalogImport != nil {
		if _, ok := repository.(stagesClaimer); !ok || opts.CatalogImport.Repository == nil || opts.CatalogImport.Verifier == nil {
			return nil, domain.ErrInvalid
		}
		value := *opts.CatalogImport
		opts.CatalogImport = &value
	}
	if opts.CatalogSync != nil {
		if _, ok := repository.(stagesClaimer); !ok || opts.CatalogSync.Repository == nil || (opts.CatalogSync.Sidecars == nil) != (opts.CatalogSync.Inspector == nil) {
			return nil, domain.ErrInvalid
		}
		value := *opts.CatalogSync
		opts.CatalogSync = &value
	}
	if opts.Consistency != nil {
		if _, ok := repository.(stagesClaimer); !ok || opts.Consistency.Checker == nil || opts.Consistency.Repository == nil {
			return nil, domain.ErrInvalid
		}
		value := *opts.Consistency
		opts.Consistency = &value
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
	if err := r.configureNFOWrite(); err != nil {
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
	if r.options.NFOWrite != nil {
		remaining.Add(1)
		go func() {
			defer func() {
				if remaining.Add(-1) == 0 {
					close(r.done)
				}
			}()
			r.recoverNFOWrites(life)
		}()
	}
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
		if r.options.Window != nil && !r.options.Window.Allows(r.options.Now()) {
			if !r.wait(ctx, r.options.PollInterval) {
				return
			}
			continue
		}
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		var lease domain.JobLease
		var err error
		if r.nfoRepository != nil {
			lease, err = r.nfoRepository.ClaimJobWithCapabilities(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, domain.ScanCapabilities{CatalogImport: r.options.CatalogImport != nil, CatalogSync: r.options.CatalogSync != nil, ConsistencyCheck: r.options.Consistency != nil, Probe: r.probeAvailable(), NFO: r.nfoAvailable(), Ignore: r.options.Ignore != nil, FamilyIgnore: r.familyIgnoreAvailable(), NFOWrite: r.options.NFOWrite != nil})
		} else if capable, ok := r.repository.(stagesClaimer); ok {
			lease, err = capable.ClaimJobWithCapabilities(dbCtx, r.options.Owner, turn%4 == 3, r.options.LeaseDuration, domain.ScanCapabilities{CatalogImport: r.options.CatalogImport != nil, CatalogSync: r.options.CatalogSync != nil, ConsistencyCheck: r.options.Consistency != nil, Probe: r.probeAvailable(), Ignore: r.options.Ignore != nil, FamilyIgnore: r.familyIgnoreAvailable(), NFOWrite: r.options.NFOWrite != nil})
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
	var window Timer
	var windowReady <-chan time.Time
	if r.options.Window != nil {
		window = r.options.Clock.NewTimer(time.Second)
		windowReady = window.C()
	}
	defer close(done)
	defer func() {
		heartbeat.Stop()
		runtime.Stop()
		if cancellation != nil {
			cancellation.Stop()
		}
		if window != nil {
			window.Stop()
		}
	}()
	if r.options.Window != nil && !r.options.Window.Allows(r.options.Now()) {
		cancelJob(errWindowClosed)
		close(started)
		return
	}
	close(started)
	for {
		select {
		case <-ctx.Done():
			return
		case <-windowReady:
			if !r.options.Window.Allows(r.options.Now()) {
				cancelJob(errWindowClosed)
				return
			}
			window.Stop()
			window = r.options.Clock.NewTimer(time.Second)
			windowReady = window.C()
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

const inventoryScanKind = "inventory_scan"

// jobComponent maps a job kind onto its G46.2 log scope.
func jobComponent(kind string) string {
	switch kind {
	case "", inventoryScanKind, domain.JobCatalogImport, domain.JobCatalogSync:
		return "scan"
	case domain.JobNFOWrite:
		return "nfo"
	}
	return "jobs"
}

func (r *Runner) run(serviceCtx context.Context, lease domain.JobLease) {
	// G46.6: a claimed job continues the trace of the request that submitted
	// it when this process remembers it, and starts a linked root otherwise.
	// Every record below and every context derived from serviceCtx carries
	// the job span, including stage spans and cleanup after cancellation.
	kind := lease.Job.Kind
	if kind == "" {
		kind = inventoryScanKind
	}
	serviceCtx, span := tracing.Default().StartLinked(serviceCtx, "job."+kind, jobComponent(kind), tracing.LinkJob, lease.Job.ID)
	outcome := "released"
	defer func() { span.End(outcome) }()
	r.logger.InfoContext(serviceCtx, "job started", "component", "jobs", "taskId", lease.Job.ID)
	ctx, cancelJob := context.WithCancelCause(serviceCtx)
	defer cancelJob(nil)
	defer r.registerCancellation(lease, cancelJob)()
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	started, monitored := make(chan struct{}), make(chan struct{})
	go r.monitor(hbCtx, lease, cancelJob, started, monitored)
	<-started
	err, repositoryError := r.execute(ctx, lease)
	if err == nil && lease.Job.Kind == "inventory_scan" {
		if prepareErr := r.prepareInventoryPublication(ctx, lease); prepareErr != nil {
			err, repositoryError = prepareErr, true
		}
	}

	stopHeartbeat()
	<-monitored
	cause := context.Cause(ctx)
	if errors.Is(err, domain.ErrJobLeaseLost) || errors.Is(err, domain.ErrProbeLeaseLost) || errors.Is(cause, domain.ErrJobLeaseLost) || errors.Is(cause, errHeartbeatFailed) {
		outcome = "lease_lost"
		r.logger.WarnContext(serviceCtx, "job ownership could not be retained", "component", "jobs", "taskId", lease.Job.ID, "code", "job_lease_lost")
		return
	}
	// A planned closure refunds the current claim only after work and monitor
	// have joined. The repository rechecks persisted cancellation under its lock.
	if errors.Is(cause, errWindowClosed) && (err == nil || errors.Is(err, context.Canceled)) {
		dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		defer cancelDB()
		if err := r.repository.(app.JobPauseRepository).PauseJob(dbCtx, lease); err != nil {
			r.logPersistenceFailure(serviceCtx, lease)
		}
		return
	}
	// Cleanup survives service cancellation but always has its own short bound.
	if serviceCtx.Err() != nil && !errors.Is(cause, errCancelRequested) {
		dbCtx, cancelDB := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		defer cancelDB()
		if err := r.repository.ReleaseJob(dbCtx, lease); err != nil {
			r.logPersistenceFailure(serviceCtx, lease)
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
			r.logPersistenceFailure(serviceCtx, lease)
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
			r.logPersistenceFailure(serviceCtx, lease)
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
	if lease.Job.Kind == domain.JobCatalogImport && state == domain.JobFailed && code != "job_timeout" {
		code = "catalog_import_failed"
	}
	if lease.Job.Kind == domain.JobNFOWrite && state == domain.JobFailed && code != "job_timeout" {
		code = "nfo_write_failed"
	}
	if lease.Job.Kind == domain.JobCatalogSync && state == domain.JobFailed && code != "job_timeout" {
		code = "catalog_sync_failed"
	}
	if lease.Job.Kind == domain.JobConsistencyCheck && state == domain.JobFailed && code != "job_timeout" {
		code = "consistency_check_failed"
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
			r.logPersistenceFailure(serviceCtx, lease)
			return
		}
		state = domain.JobCancelled
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(serviceCtx), r.options.DBOperationTimeout)
		finishErr = r.repository.FinishJob(finishCtx, lease, state, "")
		cancelFinish()
	}
	if finishErr != nil {
		r.logPersistenceFailure(serviceCtx, lease)
		return
	}
	outcome = state
	r.logger.InfoContext(serviceCtx, "inventory job completed", "component", "jobs", "taskId", lease.Job.ID, "state", state, "code", code)
}

func (r *Runner) logPersistenceFailure(ctx context.Context, lease domain.JobLease) {
	r.logger.WarnContext(ctx, "job state could not be persisted", "component", "jobs", "taskId", lease.Job.ID, "code", "scan_unavailable")
}

func (r *Runner) executeInventory(ctx context.Context, lease domain.JobLease) (result error, repositoryError bool) {
	ctx, span := tracing.Default().Start(ctx, "scan.inventory", "scan")
	defer func() { span.End(spanOutcome(result)) }()
	// Scanner bugs cannot strand the monitor or expose panic payloads in logs.
	defer func() {
		if recover() != nil {
			result, repositoryError = domain.ErrScanIO, false
		}
	}()
	if claimer, ok := r.repository.(app.ScanDirectoryClaimer); ok && r.options.ScanConcurrency > 1 {
		return r.executeInventoryConcurrent(ctx, lease, claimer)
	}
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
		if err, repository := r.scanInventoryDirectory(ctx, lease, directory); err != nil {
			return err, repository
		}
	}
}

// executeInventoryConcurrent scans up to ScanConcurrency claimed directories
// at once under one lease. The monitor goroutine still owns heartbeat,
// cancellation and runtime limits; every slot stops when the job context ends
// and the first failure cancels the others before the job reports it. Slots
// write the same per-directory checkpoints as the sequential loop, so the
// committed inventory does not depend on the order directories complete in.
func (r *Runner) executeInventoryConcurrent(ctx context.Context, lease domain.JobLease, claimer app.ScanDirectoryClaimer) (error, bool) {
	scanCtx, stop := context.WithCancel(ctx)
	defer stop()
	type outcome struct {
		err        error
		repository bool
	}
	done := make(chan outcome, r.options.ScanConcurrency)
	active := 0
	var first *outcome
	fail := func(o outcome) {
		if first == nil {
			first = &o
			stop()
		}
	}
	for {
		if first == nil && active < r.options.ScanConcurrency && scanCtx.Err() == nil {
			dbCtx, cancel := context.WithTimeout(scanCtx, r.options.DBOperationTimeout)
			directory, err := claimer.ClaimScanDirectory(dbCtx, lease)
			cancel()
			switch {
			case err == nil:
				active++
				go func() {
					o := outcome{domain.ErrScanIO, false}
					defer func() {
						if recover() != nil {
							o = outcome{domain.ErrScanIO, false}
						}
						done <- o
					}()
					o.err, o.repository = r.scanInventoryDirectory(scanCtx, lease, directory)
				}()
				continue
			case errors.Is(err, domain.ErrNotFound):
				if active == 0 {
					return nil, false
				}
			case ctx.Err() != nil:
				fail(outcome{ctx.Err(), false})
			default:
				fail(outcome{err, true})
			}
		}
		if active == 0 {
			break
		}
		o := <-done
		active--
		if o.err != nil {
			fail(o)
		}
	}
	if first != nil {
		return first.err, first.repository
	}
	if err := ctx.Err(); err != nil {
		return err, false
	}
	return nil, false
}

// scanInventoryDirectory streams one directory to its checkpoint. A directory
// is complete only after a Done batch commits.
func (r *Runner) scanInventoryDirectory(ctx context.Context, lease domain.JobLease, directory domain.ScanDirectory) (error, bool) {
	var callbackError error
	callbackFailed, completed := false, false
	err := r.scanDirectory(ctx, directory, func(batch domain.ScanBatch) error {
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
	return nil, false
}

func (r *Runner) prepareInventoryPublication(ctx context.Context, l domain.JobLease) error {
	preparer, ok := r.repository.(app.InventoryPublicationPreparer)
	if !ok {
		return nil
	}
	for {
		call, stop := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		ready, err := preparer.PrepareInventoryPublication(call, l)
		stop()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
	}
}

// spanOutcome is the span record outcome of a stage result.
func spanOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return "failed"
}
