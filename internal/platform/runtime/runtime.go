package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"go.uber.org/fx"
)

func New(cfg config.Config, logger *slog.Logger) *fx.App {
	return fx.New(fx.NopLogger, fx.Supply(cfg, logger), fx.Provide(
		func(lc fx.Lifecycle, c config.Config) (*postgres.Store, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store, err := postgres.Open(ctx, c.DatabaseURL, c.MaxConnections)
			if err != nil {
				return nil, err
			}
			lc.Append(fx.Hook{OnStop: func(context.Context) error { store.Pool.Close(); return nil }})
			return store, nil
		},
		func(store *postgres.Store) *app.Catalog { return app.NewCatalog(store) },
		func(c config.Config, store *postgres.Store, catalog *app.Catalog, l *slog.Logger) (http.Handler, error) {
			return httpapi.New(c, store, catalog, store, l)
		},
	), fx.Invoke(serve))
}

func serve(lc fx.Lifecycle, cfg config.Config, handler http.Handler, logger *slog.Logger, shutdown fx.Shutdowner) {
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	exited := make(chan struct{})
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Listen)
		if err != nil {
			return errors.New("cannot bind Jelee listener")
		}
		go func() {
			defer close(exited)
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("HTTP listener failed", "component", "http")
				if shutdown.Shutdown(fx.ExitCode(1)) != nil {
					logger.Error("shutdown request failed", "component", "http")
				}
			}
		}()
		logger.Info("Jelee started", "component", "http")
		return nil
	}, OnStop: func(ctx context.Context) error {
		if err := server.Shutdown(ctx); err != nil {
			if closeErr := server.Close(); closeErr != nil {
				return errors.New("cannot close HTTP listener")
			}
			return errors.New("HTTP drain deadline exceeded")
		}
		select {
		case <-exited:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
}
