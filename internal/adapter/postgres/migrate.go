package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgdriver "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Migrate(ctx context.Context, dsn, action string) (uint, bool, error) {
	if action != "up" && action != "down" && action != "status" {
		return 0, false, errors.New("migration action must be up, down or status")
	}
	connection, err := pgx.ParseConfig(dsn)
	if err != nil {
		return 0, false, errors.New("invalid database configuration")
	}
	connection.ConnectTimeout = 5 * time.Second
	// The migration driver uses background contexts during lock acquisition and
	// schema setup. Bound these operations at PostgreSQL itself as well.
	connection.RuntimeParams["lock_timeout"] = "5000"
	connection.RuntimeParams["statement_timeout"] = "30000"
	db := stdlib.OpenDB(*connection)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		return 0, false, errors.New("PostgreSQL is unavailable")
	}
	reserved, err := db.Conn(ctx)
	if err != nil {
		return 0, false, errors.New("cannot reserve migration connection")
	}
	defer reserved.Close()
	// An explicit reserved connection is closed even if driver setup fails.
	// It is still backed by pgx; the migrate SQL adapter does not own the pool.
	driver, err := pgdriver.WithConnection(ctx, reserved, &pgdriver.Config{StatementTimeout: 30 * time.Second})
	if err != nil {
		return 0, false, errors.New("cannot initialize migration lock")
	}
	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return 0, false, fmt.Errorf("read embedded migrations: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return 0, false, fmt.Errorf("prepare migrations: %w", err)
	}
	defer m.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case m.GracefulStop <- true:
			case <-done:
			}
		case <-done:
		}
	}()
	switch action {
	case "up":
		err = m.Up()
	case "down":
		err = m.Steps(-1)
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, false, errors.New("migration failed; inspect database state before retrying")
	}
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errors.New("cannot read migration status")
	}
	return version, dirty, nil
}
