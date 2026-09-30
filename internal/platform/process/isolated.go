package process

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

var (
	ErrSandboxUnavailable = errors.New("process_sandbox_unavailable")
	ErrSandboxInvalid     = errors.New("process_sandbox_invalid")
	ErrUnexpectedExit     = errors.New("process_unexpected_exit")
)

// IsolatedRunner exposes one fixed ffprobe metadata operation. It cannot be
// constructed from an arbitrary executable, operation registry, or argv.
type IsolatedRunner struct{ runner *Runner }

func (r *IsolatedRunner) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return r.runner.Stats()
}

func NewIsolatedFFprobe(config Config, launcher *sandbox.Launcher) (*IsolatedRunner, error) {
	if launcher == nil {
		return nil, ErrInvalid
	}
	arguments := launcher.HelperArguments()
	maximum := base64.RawURLEncoding.EncodedLen(sandbox.MaxDescriptor)
	if len(arguments) != 2 || arguments[0] != sandbox.HelperCommand || len(arguments[1]) == 0 || len(arguments[1]) > maximum {
		return nil, ErrInvalid
	}
	// Launcher fields are private to sandbox and populated only by its verified
	// constructor. This exception cannot change the ordinary New restrictions.
	runner, err := newRunnerWithArgumentLimits(config, []Tool{{ID: "ffprobe", Path: launcher.Executable(), Operations: map[string][]string{"metadata": arguments}}}, true, maximum, maximum+len(sandbox.HelperCommand)+2)
	if err != nil {
		return nil, err
	}
	return &IsolatedRunner{runner: runner}, nil
}

func (r *IsolatedRunner) Run(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.runner == nil || request.Tool != "ffprobe" || request.Operation != "metadata" || request.Stdin == nil {
		return Result{}, ErrInvalid
	}
	return r.runner.run(ctx, request, isolatedExitError)
}

func isolatedExitError(code int) error {
	switch code {
	case sandbox.ExitUnavailable:
		return ErrSandboxUnavailable
	case sandbox.ExitInvalid:
		return ErrSandboxInvalid
	case 1:
		return ErrExit
	default:
		return ErrUnexpectedExit
	}
}
