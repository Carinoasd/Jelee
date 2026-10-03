package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/telemetry"
	"go.uber.org/fx"
)

func New(cfg config.Config, logger *slog.Logger) *fx.App {
	return newWithLifetime(cfg, logger, newLifetime(logger))
}

func newWithLifetime(cfg config.Config, logger *slog.Logger, lifetime *lifetime) *fx.App {
	metadataService, err := prepareMetadata(cfg.TMDBAPIKey, lifetime)
	if err != nil {
		return build(lifetime, fx.NopLogger, fx.Error(err))
	}
	return build(lifetime, fx.NopLogger, fx.Supply(cfg, logger), fx.Provide(
		func(c config.Config) (*resources.Budget, error) {
			if err := c.Resources.Validate(); err != nil {
				return nil, err
			}
			return resources.New(resources.Limits{CPU: c.Resources.CPULimit(), IO: c.Resources.IO, Total: c.Resources.Total, Queue: c.Resources.Queue})
		},
		func(store *postgres.Store) (*app.Metadata, error) {
			return bindMetadata(metadataService, store)
		},
		func(c config.Config) (*postgres.Store, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store, err := postgres.Open(ctx, c.DatabaseURL, c.MaxConnections)
			if err != nil {
				return nil, err
			}
			lifetime.closeStore = store.Pool.Close
			return store, nil
		},
		func(store *postgres.Store) *app.Catalog { return app.NewCatalog(store) },
		func(c config.Config, budget *resources.Budget) (*imageadapter.Processor, error) {
			if !c.EnableImages {
				return nil, nil
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			p := c.Images
			processor, err := imageadapter.New(lifetime.ctx, imageadapter.Options{
				Budget:   budget,
				TempRoot: p.TempRoot, MaxConcurrent: p.MaxConcurrent,
				MaxImageBytes: p.MaxImageBytes, MaxSourceBytes: p.MaxSourceBytes,
				MaxOutputBytes: p.MaxOutputBytes, MaxOutputDimension: p.MaxOutputDimension,
				CacheBytes: p.CacheBytes, CacheEntries: p.CacheEntries,
				Timeout:  time.Duration(p.TimeoutSeconds) * time.Second,
				CacheTTL: time.Duration(p.CacheTTLSeconds) * time.Second, DefaultQuality: p.DefaultQuality,
			})
			if err != nil {
				return nil, err
			}
			lifetime.closeImages = processor.Shutdown
			lifetime.imageStats = processor.Stats
			return processor, nil
		},
		func(c config.Config, store *postgres.Store, processor *imageadapter.Processor) (*app.Images, error) {
			if !c.EnableImages {
				return nil, nil
			}
			return app.NewImages(store, processor)
		},
		func(c config.Config, store *postgres.Store) (*telemetry.Metrics, error) {
			if !c.EnableMetrics {
				return nil, nil
			}
			metrics, err := telemetry.NewWithJobs(store, store)
			if err != nil {
				return nil, err
			}
			lifetime.closeTelemetry = metrics.Shutdown
			return metrics, nil
		},
		func(c config.Config, store *postgres.Store, budget *resources.Budget, l *slog.Logger) (*app.Jobs, error) {
			if !c.EnableJobs {
				return nil, nil
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ignoring, err := newFamilyIgnoreService(startup, c.EnableFamilyIgnore, prepareProductionFamilyIgnore)
			if err != nil {
				return nil, err
			}
			lifetime.ignoreService = ignoring
			lifetime.closeIgnore = ignoring.Close
			probing, err := newProbeService(startup, c.EnableProbe, store, prepareProductionProbe)
			if err != nil {
				return nil, err
			}
			lifetime.closeProbe = probing.Close
			probing.logger = l
			reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
			if err != nil {
				return nil, err
			}
			validation, err := newNFOService(startup, store, reader)
			if err != nil {
				return nil, err
			}
			validation.logger = l
			service, err := app.NewJobsWithScanStages(store, c.Jobs.Policy(), store, app.ScanServices{
				CancellationNotifier:  lifetime,
				FamilyIgnoreAvailable: ignoring.Available,
				IgnoreAvailable:       func() bool { return goruntime.GOOS == "linux" || goruntime.GOOS == "windows" },
				Probes:                store, ProbeIdentity: probing.identity, ProbeCapability: probing.Capability,
				NFOAdmin: store, NFOQueries: store, Images: store, NFOIdentity: validation.identity, NFOAvailable: validation.Available,
			})
			if err != nil {
				return nil, err
			}
			service, err = app.NewJobsWithInventoryImport(service, store, scan.New())
			if err != nil {
				return nil, err
			}
			service, err = app.NewJobsWithSchedules(service, store, calendar.Calendar{})
			if err != nil {
				return nil, err
			}
			service, err = app.NewJobsWithWatch(service, store)
			if err != nil {
				return nil, err
			}
			p := c.Jobs
			opts := jobworker.Options{Budget: budget, Workers: p.Workers, PollInterval: time.Duration(p.PollMilliseconds) * time.Millisecond, LeaseDuration: time.Duration(p.LeaseSeconds) * time.Second, DBOperationTimeout: time.Duration(p.DatabaseTimeoutSeconds) * time.Second, MaxJobRuntime: time.Duration(p.MaxRuntimeSeconds) * time.Second}
			window, err := calendar.ParseDailyWindow(p.WindowStart, p.WindowEnd, p.WindowTimezone)
			if err != nil {
				return nil, err
			}
			if window != nil {
				opts.Window = window
			}
			opts.CatalogImport = &jobworker.CatalogImportOptions{Repository: store, Verifier: scan.New()}
			if goruntime.GOOS == "linux" || goruntime.GOOS == "windows" {
				ignoreScanner := scan.NewIgnoreScanner()
				opts.Ignore = &jobworker.IgnoreOptions{Repository: store, Scanner: ignoreScanner, Observer: ignoreScanner}
			}
			if ignoring.Available() {
				opts.FamilyIgnore = &jobworker.FamilyIgnoreOptions{Repository: store, Scanner: scan.NewFamilyIgnoreScanner(ignoring), Available: ignoring.Available}
			}
			if validation.Available() {
				opts.NFO = &jobworker.NFOOptions{Repository: store, Reader: validation, MaxConcurrent: 2, Available: validation.Available, OnRuntimeUnavailable: validation.Disable}
			}
			if probing.Available() {
				opts.Probe = &jobworker.ProbeOptions{Repository: store, Prober: probing, LeaseDuration: domain.DefaultProbeCachePolicy().LeaseDuration, MaxConcurrent: 2, Available: probing.Available, OnRuntimeUnavailable: probing.Disable}
			}
			runner, err := jobworker.New(store, scan.New(), opts, l)
			if err != nil {
				return nil, err
			}
			observer, err := scan.NewDirectoryWatcher(scan.DefaultWatchOptions())
			if err != nil {
				return nil, err
			}
			watchRunner, err := jobworker.NewWatchRunnerWithWindow(store, observer, service, l, opts.Window)
			if err != nil {
				return nil, err
			}
			lifetime.worker = &watchGroup{worker: &scheduledWorker{worker: &probeWorker{worker: runner, probe: probing, nfo: validation}, dispatch: service, logger: l}, watch: watchRunner}
			return service, nil
		},
		func(c config.Config, store *postgres.Store, catalog *app.Catalog, jobs *app.Jobs, metadata *app.Metadata, metrics *telemetry.Metrics, pictures *app.Images, budget *resources.Budget, l *slog.Logger) (http.Handler, error) {
			if !c.EnableAccounts {
				return httpapi.NewWithResources(c, store, catalog, store, l, nil, nil, nil, nil, nil, budget)
			}
			if err := c.Accounts.Validate(); err != nil {
				return nil, err
			}
			p := c.Accounts
			hasher, err := password.New(password.Config{MemoryKiB: uint32(p.PasswordMemoryKiB), Iterations: uint32(p.PasswordIterations), Parallelism: uint8(p.PasswordParallelism), MaxConcurrent: p.PasswordConcurrency})
			if err != nil {
				return nil, err
			}
			accounts, err := app.NewAccounts(store, hasher, app.AccountOptions{SessionTTL: time.Duration(p.SessionHours) * time.Hour, MaxSessions: p.MaxSessions, LockAfter: p.LockAfter, LockFor: time.Duration(p.LockSeconds) * time.Second})
			if err != nil {
				return nil, err
			}
			var metricsHandler http.Handler
			if metrics != nil {
				metricsHandler = metrics.Handler()
			}
			return httpapi.NewWithResources(c, store, catalog, store, l, accounts, jobs, metadata, metricsHandler, pictures, budget)
		},
	), fx.Invoke(func(lc fx.Lifecycle, cfg config.Config, handler http.Handler, shutdown fx.Shutdowner) {
		lifetime.server = &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		lifetime.requestShutdown = func() error { return shutdown.Shutdown(fx.ExitCode(1)) }
		lc.Append(fx.Hook{OnStart: lifetime.start, OnStop: lifetime.stop})
	}))
}

type serviceWorker interface {
	Start(context.Context) error
	Stop(context.Context) error
}

// One Fx hook owns all resources. Separate hooks would allow an HTTP drain
// deadline to make Fx skip worker cancellation and pool cleanup entirely.
type lifetime struct {
	prepareTMDB     func(context.Context) error
	closeTMDB       func()
	ignoreService   *familyIgnoreService
	ctx             context.Context
	cancel          context.CancelFunc
	worker          serviceWorker
	server          *http.Server
	logger          *slog.Logger
	listen          func(context.Context, string, string) (net.Listener, error)
	requestShutdown func() error
	closeStore      func()
	closeProbe      func() error
	closeIgnore     func() error
	closeTelemetry  func(context.Context) error
	closeImages     func(context.Context) error
	closeOnce       sync.Once
	stopOnce        sync.Once
	exited          chan struct{}
	stopped         chan struct{}
	stopErr         error
	// Observe the same processor used by HTTP without replacing its dependencies.
	imageStats func() imageadapter.Stats
}

func newLifetime(logger *slog.Logger) *lifetime {
	ctx, cancel := context.WithCancel(context.Background())
	return &lifetime{ctx: ctx, cancel: cancel, logger: logger, listen: (&net.ListenConfig{}).Listen, exited: make(chan struct{}), stopped: make(chan struct{})}
}

func build(l *lifetime, options ...fx.Option) *fx.App {
	a := fx.New(options...)
	if a.Err() != nil {
		// Fx does not start or stop lifecycle hooks when graph construction fails.
		l.cancel()
		l.closePool()
	}
	return a
}

func (l *lifetime) closePool() {
	l.closeOnce.Do(func() {
		if l.closeTMDB != nil {
			l.closeTMDB()
		}
		if l.closeIgnore != nil {
			if err := l.closeIgnore(); err != nil {
				l.stopErr = errors.Join(l.stopErr, errors.New("ignore helper temporary cleanup failed"))
				l.logger.Error("ignore helper temporary cleanup failed", "component", "ignore", "code", "ignore_unavailable")
			}
		}
		if l.closeProbe != nil {
			if err := l.closeProbe(); err != nil {
				l.stopErr = errors.Join(l.stopErr, errors.New("probe temporary cleanup failed"))
				l.logger.Error("probe temporary cleanup failed", "component", "probe", "code", "probe_runtime_unavailable")
			}
		}
		// Metrics scrapes may prefetch shared database snapshots. Stop admitted
		// scrapes before releasing the pool, including Fx startup failures.
		if l.closeTelemetry != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := l.closeTelemetry(ctx)
			cancel()
			if err != nil {
				// Like worker cleanup, collection ownership outlives a caller's
				// deadline. Keep the pool until its last snapshot has joined.
				l.logger.Warn("waiting for metrics cleanup", "component", "metrics")
				if err := l.closeTelemetry(context.Background()); err != nil {
					l.stopErr = errors.Join(l.stopErr, errors.New("metrics shutdown failed"))
					l.logger.Error("metrics shutdown failed; pool retained", "component", "metrics")
					return
				}
			}
		}
		// Image requests recheck catalog access after decoding. Keep the pool
		// alive until cancelled decoding and held response bodies have joined.
		if l.closeImages != nil {
			if err := l.closeImages(context.Background()); err != nil {
				l.stopErr = errors.Join(l.stopErr, errors.New("image shutdown failed"))
				return
			}
		}
		if l.closeStore != nil {
			l.closeStore()
		}
	})
}

func (l *lifetime) start(ctx context.Context) error {
	if l.prepareTMDB != nil {
		if err := l.prepareTMDB(ctx); err != nil {
			l.cancel()
			l.closePool()
			return err
		}
	}
	listener, err := l.listen(ctx, "tcp", l.server.Addr)
	if err != nil {
		l.cancel()
		l.closePool()
		return errors.New("cannot bind Jelee listener")
	}
	started := false
	defer func() {
		if !started {
			// A failing OnStart hook is not included in Fx's rollback hooks.
			l.cancel()
			_ = listener.Close()
			if l.worker != nil {
				_ = l.worker.Stop(context.Background())
			}
			l.closePool()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.worker != nil {
		if err := l.worker.Start(l.ctx); err != nil {
			return errors.New("cannot start inventory workers")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	go func() {
		defer close(l.exited)
		if err := l.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.cancel()
			l.logger.Error("HTTP listener failed", "component", "http")
			if l.requestShutdown != nil && l.requestShutdown() != nil {
				l.logger.Error("shutdown request failed", "component", "http")
			}
		}
	}()
	started = true
	l.logger.Info("Jelee started", "component", "http")
	return nil
}

func (l *lifetime) stop(ctx context.Context) error {
	// Cancellation happens before waiting for either HTTP clients or workers.
	l.cancel()
	l.stopOnce.Do(func() { go l.shutdown(ctx) })
	select {
	case <-l.stopped:
		return l.stopErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *lifetime) shutdown(ctx context.Context) {
	defer close(l.stopped)
	workerDone := make(chan error, 1)
	go func() {
		if l.worker == nil {
			workerDone <- nil
			return
		}
		// Stop's caller may time out, but pool ownership remains here until all
		// scanner, heartbeat and checkpoint cleanup goroutines have joined.
		workerDone <- l.worker.Stop(context.Background())
	}()
	if err := l.server.Shutdown(ctx); err != nil {
		l.stopErr = errors.New("HTTP drain deadline exceeded")
		if err := l.server.Close(); err != nil {
			l.stopErr = errors.New("cannot close HTTP listener")
		}
	}
	<-l.exited
	if err := <-workerDone; err != nil {
		l.stopErr = errors.Join(l.stopErr, errors.New("inventory workers did not stop"))
		return // Do not close a store that workers may still be using.
	}
	l.closePool()
}

func (l *lifetime) NotifyJobCancellation(id string) {
	if notifier, ok := l.worker.(app.JobCancellationNotifier); ok {
		notifier.NotifyJobCancellation(id)
	}
}
