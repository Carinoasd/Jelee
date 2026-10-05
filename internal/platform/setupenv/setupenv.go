// Package setupenv implements the live checks of the G18 setup wizard
// (app.SetupEnvironment) against the local machine: media directories,
// listen addresses, the database and the media toolchain.
package setupenv

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/netaddr"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
)

// MinServerVersion is the oldest PostgreSQL release the wizard accepts
// (server_version_num). Jelee is developed and verified against 16.
const MinServerVersion = 160000

// DatabaseFacts are the raw database facts the database step judges.
type DatabaseFacts struct {
	ServerVersion int
	SchemaVersion int
	Dirty         bool
}

type Options struct {
	// Database reads the current facts; required.
	Database func(context.Context) (DatabaseFacts, error)
	// RequiredSchema is the clean migration version this binary serves.
	RequiredSchema int
	// Listen is this server's own configured listen address. Re-confirming
	// it must succeed even while the server itself holds the port.
	Listen string
	// TMDBConfigured reports whether TMDB_API_KEY(_FILE) is configured.
	TMDBConfigured bool
	// DetectTools overrides the toolchain probe (tests); nil uses the
	// shipped isolated ffprobe runtime health check.
	DetectTools func(context.Context) (app.SetupToolReport, error)
}

// Environment implements app.SetupEnvironment.
type Environment struct {
	netaddr.Setup
	options Options
}

func New(options Options) (*Environment, error) {
	if options.Database == nil || options.RequiredSchema < 1 {
		return nil, errors.New("setup environment needs a database probe and schema version")
	}
	if options.DetectTools == nil {
		options.DetectTools = DetectShippedTools
	}
	return &Environment{options: options}, nil
}

func (e *Environment) DatabaseStatus(ctx context.Context) (app.SetupDatabaseStatus, error) {
	facts, err := e.options.Database(ctx)
	if err != nil {
		return app.SetupDatabaseStatus{}, err
	}
	return app.SetupDatabaseStatus{ServerVersion: facts.ServerVersion, MinServerVersion: MinServerVersion,
		SchemaVersion: facts.SchemaVersion, RequiredSchema: e.options.RequiredSchema, Clean: !facts.Dirty}, nil
}

// InspectDirectory reports existence, kind and readability. Readability is
// proven by listing one entry, which needs both read and traverse rights.
func (e *Environment) InspectDirectory(ctx context.Context, path string) (app.SetupDirectoryStatus, error) {
	if err := ctx.Err(); err != nil {
		return app.SetupDirectoryStatus{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		// Missing and inaccessible parents look alike to the operator: both
		// mean the service account cannot reach the directory.
		return app.SetupDirectoryStatus{}, nil
	}
	status := app.SetupDirectoryStatus{Exists: true, Directory: info.IsDir()}
	if !status.Directory {
		return status, nil
	}
	dir, err := os.Open(path) //nolint:gosec // G304: the operator names the setup directory
	if err != nil {
		return status, nil
	}
	defer dir.Close()
	if _, err = dir.Readdirnames(1); err == nil || errors.Is(err, io.EOF) {
		status.Readable = true
	}
	return status, nil
}

// ListenAvailable binds the address briefly. The server's own address is
// always available because the server itself may be holding it.
func (e *Environment) ListenAvailable(ctx context.Context, address string) (bool, error) {
	if address == e.options.Listen {
		return true, nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return true, listener.Close()
}

func (e *Environment) DetectTools(ctx context.Context) (app.SetupToolReport, error) {
	return e.options.DetectTools(ctx)
}

func (e *Environment) TMDBCredentialConfigured() bool { return e.options.TMDBConfigured }

// DetectShippedTools reports the shipped isolated ffprobe runtime. It is the
// only media tool Jelee runs today; others are not listed until supported.
func DetectShippedTools(ctx context.Context) (app.SetupToolReport, error) {
	health := proberuntime.Diagnose(ctx)
	if err := ctx.Err(); err != nil {
		return app.SetupToolReport{}, err
	}
	if health.Capability == "available" {
		return app.SetupToolReport{Available: []string{"ffprobe"}}, nil
	}
	return app.SetupToolReport{Missing: []string{"ffprobe"}}, nil
}

// NewPostgresSetup builds the G18 wizard over PostgreSQL and the local
// machine. The server and `jelee-cli setup` share it.
func NewPostgresSetup(c config.Config, store *postgres.Store, hasher app.SetupPasswordHasher) (*app.Setup, error) {
	if store == nil {
		return nil, errors.New("setup needs a store")
	}
	environment, err := New(Options{
		Database: func(ctx context.Context) (DatabaseFacts, error) {
			facts, err := store.SetupDatabaseFacts(ctx)
			return DatabaseFacts{ServerVersion: facts.ServerVersion, SchemaVersion: facts.SchemaVersion, Dirty: facts.Dirty}, err
		},
		RequiredSchema: postgres.SchemaVersion,
		Listen:         c.Listen,
		TMDBConfigured: c.TMDBAPIKey != "",
	})
	if err != nil {
		return nil, err
	}
	return app.NewSetup(store, environment, hasher, time.Now)
}
