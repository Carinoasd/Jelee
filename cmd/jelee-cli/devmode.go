package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/devmode"
)

// Developer mode CLI (G45.1, G45.7). Enabling needs this process's
// JELEE_DEV_MODE=true and dev.enabled outside JELEE_ENV=production, plus a
// one-time token that a developer capable server hands out on its loopback
// entry (POST /api/v1/dev/token). Disabling always works.

type devmodeCLIDependencies struct {
	load  func() (config.Config, error)
	open  func(context.Context, config.Config) (devmode.Store, func(), error)
	clock devmode.Clock
}

func runDevmodeCLI(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runDevmodeCLIWith(ctx, argv, stdout, stderr, devmodeCLIDependencies{
		load: config.Load,
		open: func(ctx context.Context, cfg config.Config) (devmode.Store, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
	})
}

type devmodeCLIStatus struct {
	Active    bool       `json:"active"`
	EnabledAt *time.Time `json:"enabledAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Source    string     `json:"source,omitempty"`
	Toggles   []string   `json:"toggles"`
}

func devmodeStatusOf(rec devmode.Record, now time.Time) devmodeCLIStatus {
	st := devmodeCLIStatus{Toggles: []string{}}
	if rec.Active && now.Before(rec.ExpiresAt) {
		enabled, expires := rec.EnabledAt.UTC(), rec.ExpiresAt.UTC()
		st.Active, st.EnabledAt, st.ExpiresAt, st.Source = true, &enabled, &expires, rec.Source
		for _, t := range rec.Toggles {
			st.Toggles = append(st.Toggles, string(t))
		}
	}
	return st
}

// devmodeConfigDiff summarises the developer settings that differ from a
// production deployment, for the enable audit record (G45.2).
func devmodeConfigDiff(cfg config.Config, ttl time.Duration) string {
	env := cfg.Dev.Environment
	if len(env) > 32 {
		env = env[:32]
	}
	return "JELEE_DEV_MODE=" + strconv.FormatBool(cfg.Dev.EnvFlag) + "; dev.enabled=" + strconv.FormatBool(cfg.Dev.Enabled) +
		"; JELEE_ENV=" + strconv.Quote(env) + "; ttl=" + ttl.String() + "; dev.persistAcrossRestart=" + strconv.FormatBool(cfg.Dev.PersistAcrossRestart)
}

func runDevmodeCLIWith(ctx context.Context, argv []string, stdout, stderr io.Writer, deps devmodeCLIDependencies) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli devmode enable --token TOKEN [--ttl 12h] | disable | status")
		return 2
	}
	if len(argv) == 0 {
		return usage()
	}
	command := argv[0]
	flags := flag.NewFlagSet("devmode "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var token string
	var ttl time.Duration
	switch command {
	case "enable":
		flags.StringVar(&token, "token", "", "one-time token from POST /api/v1/dev/token on the server's loopback address")
		flags.DurationVar(&ttl, "ttl", 0, "session lifetime, at most 24h; default dev.ttlMinutes")
	case "disable", "status":
	default:
		return usage()
	}
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 || command == "enable" && token == "" || ttl < 0 || ttl > devmode.MaxTTL {
		return usage()
	}
	cfg, err := deps.load()
	if err != nil {
		fmt.Fprintln(stderr, "devmode_configuration_invalid")
		return 1
	}
	if command == "enable" {
		// Refuse before touching the database when this process already
		// fails its own thresholds.
		if cfg.Dev.Production() {
			fmt.Fprintln(stderr, "devmode_production_denied: JELEE_ENV=production ignores every developer setting")
			return 1
		}
		var missing []string
		if !cfg.Dev.EnvFlag {
			missing = append(missing, string(devmode.RequireEnvFlag))
		}
		if !cfg.Dev.Enabled {
			missing = append(missing, string(devmode.RequireConfigEnabled))
		}
		if len(missing) > 0 {
			fmt.Fprintln(stderr, "devmode_denied: missing "+strings.Join(missing, ","))
			return 1
		}
	}
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		// Never echo the connection string or driver detail.
		fmt.Fprintln(stderr, "devmode_database_unavailable")
		return 1
	}
	defer closeStore()
	controller, err := devmode.NewController(devmode.ControllerOptions{Store: store, Clock: deps.clock, Local: cfg.Dev.Inputs(), TTL: cfg.Dev.TTL()})
	if err != nil {
		fmt.Fprintln(stderr, "devmode_configuration_invalid")
		return 1
	}
	now := time.Now
	if deps.clock != nil {
		now = deps.clock.Now
	}
	var rec devmode.Record
	switch command {
	case "enable":
		effective := ttl
		if effective == 0 {
			effective = cfg.Dev.TTL()
		}
		if _, err = controller.Enable(ctx, devmode.Actor{}, token, "cli", devmodeConfigDiff(cfg, effective), ttl); err != nil {
			fmt.Fprintln(stderr, devmodeCLIError(err))
			return 1
		}
		fmt.Fprintln(stderr, "WARNING: developer mode is enabled; never use it in production. It ends at expiresAt or with `jelee-cli devmode disable`.")
	case "disable":
		if err = controller.Disable(ctx, devmode.Actor{}, "cli", "disabled with jelee-cli"); err != nil && !errors.Is(err, devmode.ErrInactive) {
			fmt.Fprintln(stderr, devmodeCLIError(err))
			return 1
		}
	}
	if rec, err = store.LoadDevSession(ctx); err != nil {
		fmt.Fprintln(stderr, devmodeCLIError(err))
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(devmodeStatusOf(rec, now())); err != nil {
		fmt.Fprintln(stderr, "devmode_output_failed")
		return 1
	}
	return 0
}

func devmodeCLIError(err error) string {
	switch {
	case errors.Is(err, devmode.ErrProduction):
		return "devmode_production_denied: JELEE_ENV=production ignores every developer setting"
	case errors.Is(err, devmode.ErrDenied):
		return "devmode_denied: the token is invalid, expired or already used; request a new one with POST /api/v1/dev/token on the server's loopback address"
	case errors.Is(err, devmode.ErrAlreadyActive):
		return "devmode_already_active"
	case errors.Is(err, devmode.ErrInvalidTTL):
		return "devmode_invalid_ttl"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "devmode_cancelled"
	}
	return "devmode_failed"
}
