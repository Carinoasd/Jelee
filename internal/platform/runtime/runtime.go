package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"go.uber.org/fx"
)

func New(cfg config.Config, logger *slog.Logger) *fx.App {
	lifetime := newLifetime(logger)
	return build(lifetime, fx.NopLogger, fx.Supply(cfg, logger), fx.Provide(
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
		func(c config.Config, store *postgres.Store, l *slog.Logger) (*app.Jobs, error) {
			if !c.EnableJobs {
				return nil, nil
			}
			if err := c.Validate(); err != nil {
				return nil, err
			}
			startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
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
				Probes: store, ProbeIdentity: probing.identity, ProbeCapability: probing.Capability,
				NFOAdmin: store, NFOQueries: store, Images: store, NFOIdentity: validation.identity, NFOAvailable: validation.Available,
			})
			if err != nil {
				return nil, err
			}
			p := c.Jobs
			opts := jobworker.Options{Workers: p.Workers, PollInterval: time.Duration(p.PollMilliseconds) * time.Millisecond, LeaseDuration: time.Duration(p.LeaseSeconds) * time.Second, DBOperationTimeout: time.Duration(p.DatabaseTimeoutSeconds) * time.Second, MaxJobRuntime: time.Duration(p.MaxRuntimeSeconds) * time.Second}
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
			lifetime.worker = &probeWorker{worker: runner, probe: probing, nfo: validation}
			return service, nil
		},
		func(c config.Config, store *postgres.Store, catalog *app.Catalog, jobs *app.Jobs, l *slog.Logger) (http.Handler, error) {
			if !c.EnableAccounts {
				return httpapi.New(c, store, catalog, store, l)
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
			return httpapi.NewWithJobs(c, store, catalog, store, l, accounts, jobs)
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
	ctx             context.Context
	cancel          context.CancelFunc
	worker          serviceWorker
	server          *http.Server
	logger          *slog.Logger
	listen          func(context.Context, string, string) (net.Listener, error)
	requestShutdown func() error
	closeStore      func()
	closeProbe      func() error
	closeOnce       sync.Once
	stopOnce        sync.Once
	exited          chan struct{}
	stopped         chan struct{}
	stopErr         error
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
		if l.closeProbe != nil {
			if err := l.closeProbe(); err != nil {
				l.stopErr = errors.Join(l.stopErr, errors.New("probe temporary cleanup failed"))
				l.logger.Error("probe temporary cleanup failed", "component", "probe", "code", "probe_runtime_unavailable")
			}
		}
		if l.closeStore != nil {
			l.closeStore()
		}
	})
}

func (l *lifetime) start(ctx context.Context) error {
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
