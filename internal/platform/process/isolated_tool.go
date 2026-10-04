package process

import (
	"context"
	"encoding/base64"
	"os"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// IsolatedToolRunner runs one sandboxed Matroska, MediaInfo or OCR mode. It
// cannot be constructed from an arbitrary executable, operation or argv:
// the only argv is the helper descriptor its verified launcher encodes.
type IsolatedToolRunner struct {
	runner   *Runner
	launcher *sandbox.ToolLauncher
	id       string
}

// ToolRequest is one run. Stdin is borrowed like Request.Stdin. Extraction
// is empty except for the extraction mode (IDs) and the OCR mode (languages). Collect, required for extraction
// and refused otherwise, reads the private directory after a successful exit
// and before its removal; it must not retain the path.
type ToolRequest struct {
	Stdin      *os.File
	Extraction sandbox.Extraction
	Collect    func(directory string) error
}

// Stats returns the runner's aggregate process counts.
func (r *IsolatedToolRunner) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return r.runner.Stats()
}

// NewIsolatedTool registers the launcher's one mode with bounded config.
func NewIsolatedTool(config Config, launcher *sandbox.ToolLauncher) (*IsolatedToolRunner, error) {
	if launcher == nil {
		return nil, ErrInvalid
	}
	id := string(launcher.Mode())
	probe, err := launcher.HelperArguments(probeExtraction(launcher))
	maximum := base64.RawURLEncoding.EncodedLen(sandbox.MaxDescriptor)
	if err != nil || len(probe) != 2 || probe[0] != sandbox.ToolHelperCommand || !validName(id) {
		return nil, ErrInvalid
	}
	// The registered operation only proves the launcher's argv shape; each
	// run builds its own descriptor through the same verified launcher.
	runner, err := newRunnerWithArgumentLimits(config, []Tool{{ID: id, Path: launcher.Executable(), Operations: map[string][]string{"run": probe}}}, true, maximum, maximum+len(sandbox.ToolHelperCommand)+2)
	if err != nil {
		return nil, err
	}
	return &IsolatedToolRunner{runner: runner, launcher: launcher, id: id}, nil
}

func probeExtraction(launcher *sandbox.ToolLauncher) sandbox.Extraction {
	switch launcher.Mode() {
	case sandbox.ToolExtract:
		return sandbox.Extraction{Tracks: []int{0}}
	case sandbox.ToolOCR:
		if languages := launcher.Languages(); len(languages) > 0 {
			return sandbox.Extraction{Languages: languages[:1]}
		}
	}
	return sandbox.Extraction{}
}

// Run executes one sandboxed run of the registered mode.
func (r *IsolatedToolRunner) Run(ctx context.Context, request ToolRequest) (Result, error) {
	if r == nil || r.runner == nil || request.Stdin == nil {
		return Result{}, ErrInvalid
	}
	if (r.launcher.Mode() == sandbox.ToolExtract) != (request.Collect != nil) {
		return Result{}, ErrInvalid
	}
	arguments, err := r.launcher.HelperArguments(request.Extraction)
	if err != nil {
		return Result{}, ErrInvalid
	}
	exitError := isolatedExitError
	if mode := r.launcher.Mode(); mode == sandbox.ToolIdentify || mode == sandbox.ToolExtract {
		// mkvtoolnix exits 1 after warnings with complete output, 2 on errors.
		exitError = func(code int) error {
			switch code {
			case 1:
				return nil
			case 2:
				return ErrExit
			}
			return isolatedExitError(code)
		}
	}
	return r.runner.runWith(ctx, Request{Tool: r.id, Operation: "run", Stdin: request.Stdin}, arguments, exitError, request.Collect)
}
