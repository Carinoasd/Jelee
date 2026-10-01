package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/MoYuanCN/Jelee/internal/platform/toolidentity"
)

// This early CLI path deliberately bypasses service configuration and the DB.
func runMediaTools(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if len(argv) != 0 {
		fmt.Fprintln(stderr, "usage: jelee-cli doctor tools")
		return 2
	}
	project, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "tool_project_unavailable")
		return 1
	}
	return writeMediaDiagnostic(ctx, toolidentity.Diagnose(ctx, project), stdout, stderr)
}

func writeMediaDiagnostic(ctx context.Context, result toolidentity.Diagnostic, stdout, stderr io.Writer) int {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "tool_output_failed")
		if ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return 124
			}
			return 130
		}
		return 1
	}
	switch result.State {
	case "verified":
		return 0
	case "timed_out":
		return 124
	case "cancelled":
		return 130
	default:
		return 1
	}
}

func runMediaToolsWithOutputCancellation(ctx context.Context, argv []string, stdout io.WriteCloser, stderr io.Writer) int {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = stdout.Close() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	return runMediaTools(ctx, argv, stdout, stderr)
}
