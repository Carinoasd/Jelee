package process

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

var (
	ErrSandboxUnavailable = errors.New("process_sandbox_unavailable")
	ErrSandboxInvalid     = errors.New("process_sandbox_invalid")
	ErrUnexpectedExit     = errors.New("process_unexpected_exit")
)

// IsolatedRunner exposes fixed sandboxed ffprobe operations: either the
// metadata probe or the embedded cover reads. It cannot be constructed from
// an arbitrary executable, operation registry, or argv.
type IsolatedRunner struct {
	runner     *Runner
	operations map[string]bool
}

// CoverOperation names the sealed cover read of one video-relative stream
// index; an index outside the sandbox range yields "".
func CoverOperation(stream int) string {
	if stream < 0 || stream > sandbox.CoverMaxVideoIndex {
		return ""
	}
	return "cover-" + strconv.Itoa(stream)
}

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
	return &IsolatedRunner{runner: runner, operations: map[string]bool{"metadata": true}}, nil
}

// NewIsolatedFFprobeCover registers only the embedded cover reads (G40.4),
// one sealed operation per video-relative stream index, all derived from the
// same verified launcher. It has no metadata operation and no argv input.
func NewIsolatedFFprobeCover(config Config, launcher *sandbox.Launcher) (*IsolatedRunner, error) {
	if launcher == nil {
		return nil, ErrInvalid
	}
	maximum := base64.RawURLEncoding.EncodedLen(sandbox.MaxDescriptor)
	operations := make(map[string][]string, sandbox.CoverMaxVideoIndex+1)
	allowed := make(map[string]bool, sandbox.CoverMaxVideoIndex+1)
	for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
		arguments := launcher.CoverHelperArguments(stream)
		if len(arguments) != 2 || arguments[0] != sandbox.HelperCommand || len(arguments[1]) == 0 || len(arguments[1]) > maximum {
			return nil, ErrInvalid
		}
		operations[CoverOperation(stream)] = arguments
		allowed[CoverOperation(stream)] = true
	}
	runner, err := newRunnerWithArgumentLimits(config, []Tool{{ID: "ffprobe", Path: launcher.Executable(), Operations: operations}}, true, maximum, maximum+len(sandbox.HelperCommand)+2)
	if err != nil {
		return nil, err
	}
	return &IsolatedRunner{runner: runner, operations: allowed}, nil
}

func (r *IsolatedRunner) Run(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.runner == nil || request.Tool != "ffprobe" || !r.operations[request.Operation] || request.Stdin == nil {
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
