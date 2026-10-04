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
	"github.com/MoYuanCN/Jelee/internal/platform/cache"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/MoYuanCN/Jelee/internal/platform/setupenv"
	"github.com/MoYuanCN/Jelee/internal/platform/telemetry"
	"go.uber.org/fx"
)

func New(cfg config.Config, logger *slog.Logger) *fx.App {
	sweepStartupTemporaries(context.Background(), cfg, logger)
	return newWithLifetime(cfg, logger, newLifetime(logger))
}

func newWithLifetime(cfg config.Config, logger *slog.Logger, lifetime *lifetime) *fx.App {
	if err := cfg.Resources.Validate(); err != nil {
		return build(lifetime, fx.NopLogger, fx.Error(err))
	}
	if cfg.Dev.Capable() {
		lifetime.queryLog = postgres.NewQueryLog(logger)
	}
	budget, err := resources.New(resources.Limits{CPU: cfg.Resources.CPULimit(), IO: cfg.Resources.IO, Total: cfg.Resources.Total, Queue: cfg.Resources.Queue})
	if err != nil {
		return build(lifetime, fx.NopLogger, fx.Error(err))
	}
	metadataService, err := prepareMetadataWithBudget(cfg.TMDBAPIKey, lifetime, budget)
	if err != nil {
		return build(lifetime, fx.NopLogger, fx.Error(err))
	}
	return build(lifetime, fx.NopLogger, fx.Supply(cfg, logger, budget), fx.Provide(
		func(store *postgres.Store) (*app.Metadata, error) {
			return bindMetadata(metadataService, store)
		},
		func(c config.Config) (*postgres.Store, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Only a developer capable instance attaches the SQL log tracer
			// (G45.5); production pays nothing for it.
			store, err := postgres.OpenWithQueryLog(ctx, c.DatabaseURL, c.MaxConnections, lifetime.queryLog)
			if err != nil {
				return nil, err
			}
			lifetime.closeStore = store.Pool.Close
			return store, nil
		},
		func(c config.Config, store *postgres.Store, l *slog.Logger) (*app.Catalog, error) {
			catalog, err := app.NewCatalog(store).WithPlayback(store)
			if err != nil {
				return nil, err
			}
			if catalog, err = catalog.WithBrowse(store); err != nil {
				return nil, err
			}
			if catalog, err = catalog.WithDetails(store); err != nil || !c.EnableCatalog {
				return catalog, err
			}
			if err = c.Playback.Validate(); err != nil {
				return nil, err
			}
			if err = c.Stats.Validate(); err != nil {
				return nil, err
			}
			location, err := c.Stats.Location()
			if err != nil {
				return nil, err
			}
			stats, err := app.NewWatchStats(store, app.WatchStatsOptions{Location: location, SundayWeeks: c.Stats.SundayWeeks(), Interval: c.Stats.AggregateInterval(),
				ExportMaxRows: c.Stats.ExportRows(), Retention: c.Stats.Retention(), Rules: c.Stats.Rules(), Logger: l})
			if err != nil {
				return nil, err
			}
			p := c.Playback
			progress, err := app.NewProgress(store, store, app.ProgressOptions{FlushInterval: p.FlushInterval(), SessionTimeout: p.SessionTimeout(),
				ReportInterval: p.ReportInterval(), SampleInterval: p.SampleInterval(), Retention: p.Retention(), MaxBatch: p.Batch(), MaxSessions: p.Sessions(),
				Rules: c.Stats.Rules(), Logger: l, OnEnded: stats.Wake})
			if err != nil {
				return nil, err
			}
			lifetime.progress, lifetime.stats = progress, stats
			if catalog, err = catalog.WithProgress(progress); err != nil {
				return nil, err
			}
			return catalog.WithWatchStats(stats)
		},
		func(c config.Config, store *postgres.Store, budget *resources.Budget) (*imageadapter.Processor, error) {
			if !c.EnableImages {
				return nil, nil
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			p := c.Images
			options := imageadapter.Options{
				Budget:   budget,
				TempRoot: p.TempRoot, MaxConcurrent: p.MaxConcurrent,
				MaxImageBytes: p.MaxImageBytes, MaxSourceBytes: p.MaxSourceBytes,
				MaxOutputBytes: p.MaxOutputBytes, MaxOutputDimension: p.MaxOutputDimension,
				CacheBytes: p.CacheBytes, CacheEntries: p.CacheEntries,
				Timeout:  time.Duration(p.TimeoutSeconds) * time.Second,
				CacheTTL: time.Duration(p.CacheTTLSeconds) * time.Second, DefaultQuality: p.DefaultQuality,
			}
			pictures, err := openImageStore(lifetime.ctx, p, store)
			if err != nil {
				return nil, err
			}
			if pictures != nil {
				options.Store, options.Index = pictures, store
			}
			processor, err := imageadapter.New(lifetime.ctx, options)
			if err != nil {
				if pictures != nil {
					_ = pictures.Close(context.Background())
				}
				return nil, err
			}
			// The store closes after the processor: held responses and the
			// eviction pass may still use it until Shutdown returns.
			lifetime.closeImages = func(ctx context.Context) error {
				if err := processor.Shutdown(ctx); err != nil {
					return err
				}
				if pictures != nil {
					return pictures.Close(ctx)
				}
				return nil
			}
			lifetime.imageStats = processor.Stats
			return processor, nil
		},
		func(c config.Config, store *postgres.Store, processor *imageadapter.Processor) (*app.Images, error) {
			if !c.EnableImages {
				return nil, nil
			}
			images, err := app.NewImages(store, processor)
			if err != nil {
				return nil, err
			}
			if images, err = images.WithAssets(store, processor); err != nil {
				return nil, err
			}
			return images.WithSummaries(store)
		},
		func(c config.Config, store *postgres.Store, budget *resources.Budget, processor *imageadapter.Processor) (*telemetry.Metrics, error) {
			if !c.EnableMetrics {
				return nil, nil
			}
			// A nil processor means images are disabled; avoid a typed-nil source.
			var pictures telemetry.ImageStatsSource
			if processor != nil {
				pictures = processor
			}
			metrics, err := telemetry.NewWithImages(store, store, budget, pictures)
			if err != nil {
				return nil, err
			}
			if err := metrics.RegisterCaches(cache.Default()); err != nil {
				return nil, errors.Join(err, metrics.Shutdown(context.Background()))
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
			opts := jobworker.Options{Budget: budget, Workers: p.Workers, PollInterval: time.Duration(p.PollMilliseconds) * time.Millisecond, LeaseDuration: time.Duration(p.LeaseSeconds) * time.Second, DBOperationTimeout: time.Duration(p.DatabaseTimeoutSeconds) * time.Second, MaxJobRuntime: time.Duration(p.MaxRuntimeSeconds) * time.Second, ScanConcurrency: p.ScanConcurrency}
			window, err := calendar.ParseDailyWindow(p.WindowStart, p.WindowEnd, p.WindowTimezone)
			if err != nil {
				return nil, err
			}
			if window != nil {
				opts.Window = window
			}
			opts.CatalogImport = &jobworker.CatalogImportOptions{Repository: store, Verifier: scan.New()}
			opts.CatalogSync = &jobworker.CatalogSyncOptions{Repository: store, Sidecars: store, Inspector: scan.SidecarInspector{}}
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
			if c.EnableNFOWrite {
				writer, err := nfo.NewWriterWithBudget(budget)
				if err != nil {
					return nil, err
				}
				opts.NFOWrite = &jobworker.NFOWriteOptions{Repository: store, Committer: writer}
			}
			if probing.Available() {
				opts.Probe = &jobworker.ProbeOptions{Repository: store, Prober: probing, LeaseDuration: domain.DefaultProbeCachePolicy().LeaseDuration, MaxConcurrent: 2, Available: probing.Available, OnRuntimeUnavailable: probing.Disable}
			}
			runner, err := jobworker.New(store, scan.New(), opts, l)
			if err != nil {
				return nil, err
			}
			watchOptions := scan.DefaultWatchOptions()
			watchOptions.Budget = budget
			observer, err := scan.NewDirectoryWatcher(watchOptions)
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
		func(c config.Config, store *postgres.Store, l *slog.Logger) (*devmode.Controller, error) {
			return newDevController(c, store, l, lifetime)
		},
		// The controller comes first so the webhook client can follow
		// relax_ssrf_strict.
		func(c config.Config, store *postgres.Store, budget *resources.Budget, l *slog.Logger, _ *devmode.Controller) (*app.Webhooks, error) {
			return newWebhooks(c, store, budget, l, lifetime)
		},
		func(c config.Config, store *postgres.Store, catalog *app.Catalog, jobs *app.Jobs, metadata *app.Metadata, metrics *telemetry.Metrics, pictures *app.Images, webhooks *app.Webhooks, budget *resources.Budget, l *slog.Logger, dev *devmode.Controller) (http.Handler, error) {
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
			var options []httpapi.Option
			if webhooks != nil {
				options = append(options, httpapi.WithWebhooks(webhooks))
			}
			// Client control (G47) gates every authenticated and login request
			// with the rules in storage; its background writer is closed with
			// the pool after HTTP has drained.
			clientService, err := app.NewClientControl(store, httpapi.ValidateClientRule)
			if err != nil {
				return nil, err
			}
			startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			clients, err := httpapi.NewClientControl(startup, store, clientService, l, httpapi.ClientControlOptions{})
			cancel()
			if err != nil {
				return nil, err
			}
			lifetime.closeClientControl = clients.Close
			options = append(options, httpapi.WithClientControl(clients))
			// G18: the wizard and its gate. An incomplete setup issues the
			// one-time setup token now; a complete one never reopens.
			setup, err := setupenv.NewPostgresSetup(c, store, hasher)
			if err != nil {
				return nil, err
			}
			startup, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			token, err := prepareSetup(startup, c, setup, l)
			cancel()
			if err != nil {
				return nil, err
			}
			options = append(options, httpapi.WithSetup(setup, token), httpapi.WithDevMode(dev))
			return httpapi.NewWithResources(c, store, catalog, store, l, accounts, jobs, metadata, metricsHandler, pictures, budget, options...)
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
	// closeClientControl writes buffered client control hits and activity.
	closeClientControl func(context.Context) error
	// progress buffers playback reports; it runs with the workers and is
	// flushed once more after HTTP has drained (G23.2).
	progress     *app.Progress
	progressDone chan struct{}
	// stats rolls ended sessions up into the daily statistics (G23.5).
	stats     *app.WatchStats
	statsDone chan struct{}
	// webhooks delivers outbox events (G12.3). It stops claiming at
	// cancellation and joins its in-flight attempts before the pool closes.
	webhooks           *app.WebhookDispatcher
	webhooksDone       chan struct{}
	closeWebhookClient func()
	// dev is the developer mode controller (G45); devDone joins its
	// refresh loop, queryLog is the SQL log of a capable instance and
	// levels the log router verbose logging raises.
	dev       *devmode.Controller
	devDone   chan struct{}
	queryLog  *postgres.QueryLog
	levels    logLevels
	baseLevel slog.Level
	closeOnce sync.Once
	stopOnce  sync.Once
	exited    chan struct{}
	stopped   chan struct{}
	stopErr   error
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
		if l.closeClientControl != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := l.closeClientControl(ctx); err != nil {
				l.logger.Warn("client control records not written at shutdown", "component", "client_control")
			}
			cancel()
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
			l.closePool() //nolint:contextcheck // the pool is closed even when the start context failed
			return err
		}
	}
	listener, err := l.listen(ctx, "tcp", l.server.Addr)
	if err != nil {
		l.cancel()
		l.closePool() //nolint:contextcheck // the pool is closed even when the start context failed
		return errors.New("cannot bind Jelee listener")
	}
	started := false
	defer func() { //nolint:contextcheck // a failed start must release workers and the pool regardless of the start context
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
		if err := l.worker.Start(l.ctx); err != nil { //nolint:contextcheck // workers live for the process lifetime, not for the OnStart hook
			return errors.New("cannot start inventory workers")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.progress != nil {
		l.progressDone = make(chan struct{})
		go func() {
			defer close(l.progressDone)
			l.progress.Run(l.ctx)
		}()
	}
	if l.stats != nil {
		l.statsDone = make(chan struct{})
		go func() {
			defer close(l.statsDone)
			l.stats.Run(l.ctx)
		}()
	}
	if l.dev != nil {
		l.devDone = make(chan struct{})
		go func() {
			defer close(l.devDone)
			l.dev.Run(l.ctx)
		}()
	}
	if l.webhooks != nil {
		l.webhooksDone = make(chan struct{})
		go func() {
			defer close(l.webhooksDone)
			l.webhooks.Run(l.ctx)
		}()
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
	go func() { //nolint:gosec,contextcheck // G118: worker shutdown must join every goroutine even after the stop deadline
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
	l.flushProgress() //nolint:contextcheck // final progress flush runs after the stop deadline by design
	if l.statsDone != nil {
		<-l.statsDone
	}
	l.joinWebhooks()
	if l.devDone != nil {
		<-l.devDone
	}
	if err := <-workerDone; err != nil {
		l.stopErr = errors.Join(l.stopErr, errors.New("inventory workers did not stop"))
		return // Do not close a store that workers may still be using.
	}
	l.closePool() //nolint:contextcheck // shutdown closes the pool after the stop deadline by design
}

// joinWebhooks waits for the deliverer, which was cancelled with the
// lifetime context and finishes or abandons its in-flight attempts within
// its stop grace. Abandoned attempts are re-sent after their lease expires.
func (l *lifetime) joinWebhooks() {
	if l.webhooksDone != nil {
		<-l.webhooksDone
	}
	if l.closeWebhookClient != nil {
		l.closeWebhookClient()
	}
}

// flushProgress writes the reports buffered since the last flush once no
// request can add more. Sessions stay active in storage: clients that keep
// reporting rejoin them after a restart, the others time out.
func (l *lifetime) flushProgress() {
	if l.progress == nil {
		return
	}
	if l.progressDone != nil {
		<-l.progressDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := l.progress.Flush(ctx); err != nil {
		l.stopErr = errors.Join(l.stopErr, errors.New("playback progress flush failed"))
		l.logger.Error("playback progress flush failed at shutdown", "component", "playback")
	}
}

func (l *lifetime) NotifyJobCancellation(id string) {
	if notifier, ok := l.worker.(app.JobCancellationNotifier); ok {
		notifier.NotifyJobCancellation(id)
	}
}

// openImageStore opens the persistent original/variant store only when a
// root is configured. Library roots known at startup are checked for overlap
// here; roots added later are checked on every request that reads them.
func openImageStore(lifetime context.Context, c config.ImagesConfig, catalog *postgres.Store) (*imageadapter.Store, error) {
	if c.StoreRoot == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(lifetime, 30*time.Second)
	defer cancel()
	roots, err := catalog.ListLibraryRootPaths(ctx, 1024)
	if err != nil {
		return nil, errors.New("cannot list library roots for the image store")
	}
	store, err := imageadapter.OpenStore(ctx, imageadapter.StoreOptions{Root: c.StoreRoot, MediaRoots: roots,
		OriginalBytes: c.StoreOriginalBytes, VariantBytes: c.StoreVariantBytes, MaxEntries: c.StoreEntries})
	if err != nil {
		return nil, errors.New("cannot open image store")
	}
	return store, nil
}
