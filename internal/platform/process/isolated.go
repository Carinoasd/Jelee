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

// IsolatedRunner exposes fixed sandboxed ffprobe operations: the metadata
// probe, the embedded cover reads or the chapter image reads. It cannot be constructed from
// an arbitrary executable, operation registry, or argv.
type IsolatedRunner struct {
	runner     *Runner
	operations map[string]bool
	// direct runs the tool itself (Windows): no helper exit codes exist.
	direct bool
}

// SidecarOperation names the sealed external track probe (B4), registered
// next to the metadata probe on the same runner.
const SidecarOperation = "sidecar"

// CoverOperation names the sealed cover read of one video-relative stream
// index; an index outside the sandbox range yields "".
func CoverOperation(stream int) string {
	if stream < 0 || stream > sandbox.CoverMaxVideoIndex {
		return ""
	}
	return "cover-" + strconv.Itoa(stream)
}

// ChapterOperation names the sealed chapter image read of one video-relative
// stream index; an index outside the sandbox range yields "".
func ChapterOperation(stream int) string {
	if stream < 0 || stream > sandbox.CoverMaxVideoIndex {
		return ""
	}
	return "chapters-" + strconv.Itoa(stream)
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
	if launcher.Direct() {
		return newDirectFFprobe(config, launcher.Executable(), launcher.Confinement(), map[string][]string{
			"metadata": launcher.DirectArguments(), SidecarOperation: launcher.DirectSidecarArguments()})
	}
	arguments, sidecar := launcher.HelperArguments(), launcher.SidecarHelperArguments()
	maximum := base64.RawURLEncoding.EncodedLen(sandbox.MaxDescriptor)
	for _, value := range [][]string{arguments, sidecar} {
		if len(value) != 2 || value[0] != sandbox.HelperCommand || len(value[1]) == 0 || len(value[1]) > maximum {
			return nil, ErrInvalid
		}
	}
	// Launcher fields are private to sandbox and populated only by its verified
	// constructor. This exception cannot change the ordinary New restrictions.
	// Both operations share this runner and so its process admission.
	runner, err := newRunnerWithArgumentLimits(config, []Tool{{ID: "ffprobe", Path: launcher.Executable(), Operations: map[string][]string{"metadata": arguments, SidecarOperation: sidecar}}}, true, maximum, maximum+len(sandbox.HelperCommand)+2)
	if err != nil {
		return nil, err
	}
	return &IsolatedRunner{runner: runner, operations: map[string]bool{"metadata": true, SidecarOperation: true}}, nil
}

// NewIsolatedFFprobeCover registers only the embedded cover reads (G40.4),
// one sealed operation per video-relative stream index, all derived from the
// same verified launcher. It has no metadata operation and no argv input.
func NewIsolatedFFprobeCover(config Config, launcher *sandbox.Launcher) (*IsolatedRunner, error) {
	if launcher == nil {
		return nil, ErrInvalid
	}
	if launcher.Direct() {
		operations := make(map[string][]string, sandbox.CoverMaxVideoIndex+1)
		for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
			operations[CoverOperation(stream)] = launcher.DirectCoverArguments(stream)
		}
		return newDirectFFprobe(config, launcher.Executable(), launcher.Confinement(), operations)
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

// NewIsolatedFFprobeChapters registers only the chapter image reads (G40.1),
// one sealed operation per video-relative stream index, all derived from the
// same verified launcher. It has no metadata or cover operation and no argv
// input.
func NewIsolatedFFprobeChapters(config Config, launcher *sandbox.Launcher) (*IsolatedRunner, error) {
	if launcher == nil {
		return nil, ErrInvalid
	}
	maximum := base64.RawURLEncoding.EncodedLen(sandbox.MaxDescriptor)
	operations := make(map[string][]string, sandbox.CoverMaxVideoIndex+1)
	allowed := make(map[string]bool, sandbox.CoverMaxVideoIndex+1)
	for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
		arguments := launcher.ChapterHelperArguments(stream)
		if len(arguments) != 2 || arguments[0] != sandbox.HelperCommand || len(arguments[1]) == 0 || len(arguments[1]) > maximum {
			return nil, ErrInvalid
		}
		operations[ChapterOperation(stream)] = arguments
		allowed[ChapterOperation(stream)] = true
	}
	runner, err := newRunnerWithArgumentLimits(config, []Tool{{ID: "ffprobe", Path: launcher.Executable(), Operations: operations}}, true, maximum, maximum+len(sandbox.HelperCommand)+2)
	if err != nil {
		return nil, err
	}
	return &IsolatedRunner{runner: runner, operations: allowed}, nil
}

// newDirectFFprobe registers a direct (Windows) launcher: the verified
// ffprobe.exe itself with its fixed argv, every child started under the
// launcher's confinement. There is no helper, so exit codes 64 and 78 are
// ffprobe's own and map as for any unexpected tool exit.
func newDirectFFprobe(config Config, executable string, confinement sandbox.Confinement, operations map[string][]string) (*IsolatedRunner, error) {
	if !confinement.Valid() || len(operations) == 0 {
		return nil, ErrInvalid
	}
	allowed := make(map[string]bool, len(operations))
	for operation, arguments := range operations {
		if len(arguments) == 0 {
			return nil, ErrInvalid
		}
		allowed[operation] = true
	}
	runner, err := newRunner(config, []Tool{{ID: "ffprobe", Path: executable, Operations: operations}}, true)
	if err != nil {
		return nil, err
	}
	runner.confinement = &confinement
	return &IsolatedRunner{runner: runner, operations: allowed, direct: true}, nil
}

func (r *IsolatedRunner) Run(ctx context.Context, request Request) (Result, error) {
	if r == nil || r.runner == nil || request.Tool != "ffprobe" || !r.operations[request.Operation] || request.Stdin == nil {
		return Result{}, ErrInvalid
	}
	if r.direct {
		return r.runner.run(ctx, request, directExitError)
	}
	return r.runner.run(ctx, request, isolatedExitError)
}

// directExitError classifies a confined tool's own exit: 1 is ffprobe's
// normal rejection; anything else, including a job limit termination, is
// an unexpected tool failure.
func directExitError(code int) error {
	if code == 1 {
		return ErrExit
	}
	return ErrUnexpectedExit
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
