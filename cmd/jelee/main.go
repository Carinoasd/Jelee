package main

import (
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/runtime"
	"os"
)

func main() {
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
