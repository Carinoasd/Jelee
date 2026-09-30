package main

import (
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/runtime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperCommand {
		os.Exit(proberuntime.Helper(os.Args[2:]))
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Jelee configuration:", err)
		os.Exit(1)
	}
	a := runtime.New(cfg, logging.New(os.Stdout))
	if a.Err() != nil {
		fmt.Fprintln(os.Stderr, "Jelee cannot initialize: verify PostgreSQL connectivity and run jelee-migrate up.")
		os.Exit(1)
	}
	a.Run()
}
