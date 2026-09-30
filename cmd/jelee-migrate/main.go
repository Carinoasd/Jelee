package main

import (
	"context"
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"os"
	"os/signal"
	"time"
)

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: jelee-migrate up|status|down --i-understand")
		return 2
	}
	action := os.Args[1]
	if action == "down" && (len(os.Args) != 3 || os.Args[2] != "--i-understand") {
		fmt.Fprintln(os.Stderr, "down removes the latest schema and its data; add --i-understand after taking a backup")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	version, dirty, err := postgres.Migrate(ctx, cfg.DatabaseURL, action)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("Jelee schema version=%d dirty=%t\n", version, dirty)
	if dirty {
		return 1
	}
	return 0
}
