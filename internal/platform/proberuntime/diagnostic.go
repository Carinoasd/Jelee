package proberuntime

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/tools"
)

// Diagnostic contains fixed safe classifications. Available means this shipped
// helper can enforce its policy; it does not enable scan/catalog operations.
type Diagnostic struct {
	Platform        string `json:"platform"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	Capability      string `json:"capability"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
}

type diagnosticRunner interface {
	Run(context.Context, process.Request) (process.Result, error)
}

func Diagnose(ctx context.Context) Diagnostic {
	return diagnose(ctx, runtime.GOOS+"-"+runtime.GOARCH, func(ctx context.Context, directory string) (diagnosticRunner, error) {
		return New(ctx, directory)
	})
}

// The internal seam tests health classification without granting callers a way
// to replace the production policy, executable or platform selection.
func diagnose(ctx context.Context, platform string, factory func(context.Context, string) (diagnosticRunner, error)) (result Diagnostic) {
	result = Diagnostic{Platform: platform, State: "unavailable", Reason: "runtime_unavailable", Capability: "disabled"}
	if ctx == nil {
		result.Reason = "invalid_context"
		return
	}
	if platform != "linux-amd64" {
		result.Reason = "platform_unsupported"
		return
	}
	if spec, err := tools.FFprobeSpec("linux-amd64"); err == nil {
		result.ExpectedVersion = spec.VendorVersion
	}
	if err := ctx.Err(); err != nil {
		diagnosticContext(&result, err)
		return
	}
	dir, err := os.MkdirTemp("", "jelee-probe-check-")
	if err != nil {
		result.Reason = "temporary_unavailable"
		return
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			result.State = "unavailable"
			result.Reason = "cleanup_failed"
			result.Capability = "disabled"
		}
	}()
	runner, err := factory(ctx, dir)
	if err != nil {
		if ctx.Err() != nil {
			diagnosticContext(&result, ctx.Err())
		}
		return
	}
	file, err := os.CreateTemp(dir, "health-")
	if err != nil {
		result.Reason = "temporary_unavailable"
		return
	}
	name := file.Name()
	_, writeErr := file.WriteString("Jelee sandbox health check; deliberately invalid media\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		result.Reason = "temporary_unavailable"
		return
	}
	input, err := os.Open(name)
	if err != nil {
		result.Reason = "temporary_unavailable"
		return
	}
	_, err = runner.Run(ctx, process.Request{Tool: "ffprobe", Operation: "metadata", Stdin: input})
	closeErr = input.Close()
	if ctx.Err() != nil {
		diagnosticContext(&result, ctx.Err())
		return
	}
	// All helper setup failures exit 64/78. Exit1 here proves the fixed verified
	// ffprobe was exec'd after policy application and rejected the health bytes.
	if errors.Is(err, process.ErrExit) && closeErr == nil {
		result.State, result.Reason, result.Capability = "available", "isolated_helper_verified", "available"
	}
	return
}

func diagnosticContext(result *Diagnostic, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		result.State, result.Reason = "timed_out", "deadline_exceeded"
	} else {
		result.State, result.Reason = "cancelled", "operation_cancelled"
	}
	result.Capability = "disabled"
}
