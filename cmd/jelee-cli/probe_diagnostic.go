package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
)

func runProbeDiagnostic(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if len(argv) != 0 {
		fmt.Fprintln(stderr, "usage: jelee-cli doctor probe")
		return 2
	}
	return writeProbeDiagnostic(ctx, proberuntime.Diagnose(ctx), stdout, stderr)
}

func writeProbeDiagnostic(ctx context.Context, diagnostic proberuntime.Diagnostic, stdout, stderr io.Writer) int {
	if err := json.NewEncoder(stdout).Encode(diagnostic); err != nil {
		fmt.Fprintln(stderr, "probe_output_failed")
		if ctx != nil && ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return 124
			}
			return 130
		}
		return 1
	}
	if ctx != nil && ctx.Err() != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 124
		}
		return 130
	}
	switch diagnostic.State {
	case "available":
		return 0
	case "timed_out":
		return 124
	case "cancelled":
		return 130
	default:
		return 1
	}
}

func runProbeDiagnosticWithOutputCancellation(ctx context.Context, argv []string, stdout io.WriteCloser, stderr io.Writer) int {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = stdout.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	return runProbeDiagnostic(ctx, argv, stdout, stderr)
}
