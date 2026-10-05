package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
)

var (
	ErrIgnoreUnavailable = errors.New("ignore_helper_unavailable")
	ErrIgnoreEvaluation  = errors.New("ignore_helper_evaluation_failed")
)

// IgnoreRunner runs only this executable's fixed legacy helper command. It
// neither accepts arbitrary programs/argv nor changes the ffprobe allowlist.
type IgnoreRunner struct {
	runner *Runner
	slots  chan struct{}
}

func NewIgnoreRunner(tempRoot string, maxConcurrent int, timeout time.Duration) (*IgnoreRunner, error) {
	if maxConcurrent < 1 || maxConcurrent > 2 || timeout < time.Millisecond || timeout > 10*time.Second {
		return nil, ErrInvalid
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, ErrStart
	}
	config := Config{TempRoot: tempRoot, MaxConcurrent: maxConcurrent, Timeout: timeout, MaxStdoutBytes: legacyignore.MaxResultBytes, MaxStderrBytes: 64 << 10}
	runner, err := newRunner(config, []Tool{{ID: "legacy-ignore", Path: executable, Operations: map[string][]string{"batch": {legacyignorehelper.Command}}}}, true)
	if err != nil {
		return nil, err
	}
	return &IgnoreRunner{runner: runner, slots: make(chan struct{}, maxConcurrent)}, nil
}
func (r *IgnoreRunner) Stats() Stats {
	if r == nil || r.runner == nil {
		return Stats{}
	}
	return r.runner.Stats()
}

// Evaluate owns its input file through process join and result validation.
// Errors discard every decision so callers cannot publish partial results.
func (r *IgnoreRunner) Evaluate(ctx context.Context, batch legacyignore.Batch) (result legacyignore.BatchResult, resultErr error) {
	if r == nil || r.runner == nil || ctx == nil {
		return result, ErrInvalid
	}
	if ctx.Err() != nil {
		return result, contextError(ctx)
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return result, ErrBusy
	}
	defer func() { <-r.slots }()
	ctx, cancel := context.WithTimeout(ctx, r.runner.config.Timeout)
	defer cancel()
	frame, err := legacyignore.EncodeBatch(ctx, batch)
	if err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp(r.runner.config.TempRoot, "ignore-input-")
	if err != nil {
		return result, ErrStart
	}
	defer func() {
		if os.RemoveAll(dir) != nil {
			result = legacyignore.BatchResult{}
			resultErr = ErrCleanup
		}
	}()
	path := filepath.Join(dir, "request.bin")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // G304: a file inside the private scratch directory
	if err != nil {
		return result, ErrStart
	}
	_, writeErr := writer.Write(frame)
	closeErr := writer.Close()
	if writeErr != nil || closeErr != nil {
		return result, ErrStart
	}
	input, err := os.Open(path) //nolint:gosec // G304: a file inside the private scratch directory
	if err != nil {
		return result, ErrStart
	}
	defer func() {
		if input.Close() != nil {
			result = legacyignore.BatchResult{}
			resultErr = ErrCleanup
		}
	}()
	raw, err := r.runner.run(ctx, Request{Tool: "legacy-ignore", Operation: "batch", Stdin: input}, ignoreExitError)
	if err != nil {
		return result, err
	}
	result, err = legacyignore.DecodeResult(ctx, batch, raw.Stdout)
	if err != nil {
		return legacyignore.BatchResult{}, err
	}
	return result, nil
}
func ignoreExitError(code int) error {
	switch code {
	case legacyignorehelper.ExitUnavailable:
		return ErrIgnoreUnavailable
	case legacyignorehelper.ExitInvalid:
		return ErrInvalid
	default:
		return ErrIgnoreEvaluation
	}
}
