package main

import (
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	// The otlp log forwarder kind; only the server links the OTLP modules.
	_ "github.com/MoYuanCN/Jelee/internal/platform/logging/otlplog"
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
	crash, err := logging.OpenCrashArchive(cfg.Logging.CrashOptions())
	if err != nil {
		logs.Close()
		fmt.Fprintln(os.Stderr, "Jelee logging:", err)
		os.Exit(1)
	}
	if code := serve(cfg, logs, crash); code != 0 {
		os.Exit(code)
	}
}

// serve runs the server. A panic of this goroutine is logged, archived and
// raised again (G46.3); the crash output of every other goroutine goes to
// the archive's pending file and is reported by the next start.
func serve(cfg config.Config, logs *logging.Router, crash *logging.CrashArchive) int {
	defer logging.CapturePanic(logs, crash)
	logging.SetCrashArchive(crash)
	crash.Collect(logs.Logger())
	if err := crash.Arm(); err != nil {
		logs.Logger().Warn("crash output cannot be archived", "component", "system", "code", "crash_archive_unavailable")
	}
	defer crash.Disarm()
	a := runtime.NewWithLogs(cfg, logs)
	if a.Err() != nil {
		logs.Close()
		fmt.Fprintln(os.Stderr, "Jelee cannot initialize: verify PostgreSQL connectivity and run jelee-migrate up; a failed startup self-check is named in the log.")
		return 1
	}
	a.Run()
	_ = logs.Close()
	return 0
}
