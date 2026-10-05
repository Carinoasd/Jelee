package main

import (
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/ocrruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/runtime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == legacyignorehelper.Command {
		os.Exit(legacyignorehelper.Main())
	}
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperCommand {
		os.Exit(proberuntime.Helper(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == sandbox.ToolHelperCommand {
		// Matroska, MediaInfo and OCR modes share one tool helper entry.
		os.Exit(ocrruntime.ToolHelper(os.Args[2:]))
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Jelee configuration:", err)
		os.Exit(1)
	}
	logs, err := logging.Open(cfg.Logging.Options(), os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Jelee logging:", err)
		os.Exit(1)
	}
	a := runtime.NewWithLogs(cfg, logs)
	if a.Err() != nil {
		logs.Close()
		fmt.Fprintln(os.Stderr, "Jelee cannot initialize: verify PostgreSQL connectivity and run jelee-migrate up; a failed startup self-check is named in the log.")
		os.Exit(1)
	}
	a.Run()
	logs.Close()
}
