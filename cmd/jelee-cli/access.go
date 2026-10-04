package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// accessCLIStore is the emergency recovery of client control (G47.7).
type accessCLIStore interface {
	ResetClientPolicies(context.Context) (domain.ClientPolicyReset, error)
}

type accessCLIDependencies struct {
	load func() (config.Config, error)
	open func(context.Context, config.Config) (accessCLIStore, func(), error)
}

func runAccessCLI(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runAccessCLIWith(ctx, argv, stdout, stderr, accessCLIDependencies{
		load: config.Load,
		open: func(ctx context.Context, cfg config.Config) (accessCLIStore, func(), error) {
			store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.MaxConnections)
			if err != nil {
				return nil, nil, err
			}
			return store, store.Pool.Close, nil
		},
	})
}

// runAccessCLIWith implements "jelee-cli access reset-policies": it disables
// every client control rule and restores the default policy (unknown
// clients allowed, administrators and loopback exempt) in one audited
// transaction, so a misconfigured rule set that locks everyone out can be
// undone from the server's shell. Running servers pick the change up on
// their next request. Known clients, their trust and hit records are kept.
func runAccessCLIWith(ctx context.Context, argv []string, stdout, stderr io.Writer, deps accessCLIDependencies) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli access reset-policies")
		return 2
	}
	if len(argv) == 0 || argv[0] != "reset-policies" {
		return usage()
	}
	flags := flag.NewFlagSet("access reset-policies", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if err := flags.Parse(argv[1:]); err != nil || flags.NArg() != 0 {
		return usage()
	}
	cfg, err := deps.load()
	if err != nil {
		fmt.Fprintln(stderr, "access_configuration_invalid")
		return 1
	}
	store, closeStore, err := deps.open(ctx, cfg)
	if err != nil {
		// Never echo the connection string or driver detail.
		fmt.Fprintln(stderr, "access_database_unavailable")
		return 1
	}
	defer closeStore()
	result, err := store.ResetClientPolicies(ctx)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "access_cancelled")
		} else {
			fmt.Fprintln(stderr, "access_reset_failed")
		}
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "access_output_failed")
		return 1
	}
	return 0
}
