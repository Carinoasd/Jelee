// Package process runs registered programs with bounded concurrency and output.
// It manages process lifetime; it is not a filesystem or network sandbox.
package process

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("process_invalid")
	ErrBusy        = errors.New("process_busy")
	ErrCancelled   = errors.New("process_cancelled")
	ErrTimeout     = errors.New("process_timeout")
	ErrOutputLimit = errors.New("process_output_limit")
	ErrStart       = errors.New("process_start_failed")
	ErrExit        = errors.New("process_exit_failed")
	ErrCleanup     = errors.New("process_cleanup_failed")
	ErrUnsupported = errors.New("process_platform_unsupported")
)

type Config struct {
	MaxConcurrent  int
	Timeout        time.Duration
	MaxStdoutBytes int64
	MaxStderrBytes int64
	// TempRoot is an existing, private, absolute project-owned directory.
	TempRoot string
}

// Tool is supplied only by trusted registration code after binary verification.
// Operations and their argv are copied at construction and cannot be changed by
// requests. Arguments are not passed to a shell or expanded as templates.
type Tool struct {
	ID         string
	Path       string
	Operations map[string][]string
}

type Request struct {
	Tool      string
	Operation string
	// Stdin is borrowed, read-only, regular and seekable. The caller retains
	// ownership and must not close or concurrently use it until Run returns.
	Stdin *os.File
}

type Result struct {
	// Stdout is untrusted internal parser input, never a public response/log.
	// No raw output is returned after any error; stderr is never retained.
	Stdout      []byte `json:"-"`
	ExitCode    int    `json:"-"`
	StdoutBytes int64  `json:"-"`
	StderrBytes int64  `json:"-"`
}

func (Result) String() string   { return "process result (output redacted)" }
func (Result) GoString() string { return "process result (output redacted)" }

type Runner struct {
	config Config
	tools  map[string]Tool
	slots  chan struct{}
}

func validName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func New(config Config, tools []Tool) (*Runner, error) {
	return newRunner(config, tools, false)
}

// allowTestProgram is reachable only by package tests; production registration
// accepts ffprobe alone. Its version/checksum identity is the registrar's duty.
func newRunner(config Config, tools []Tool, allowTestProgram bool) (*Runner, error) {
	if config.MaxConcurrent < 1 || config.MaxConcurrent > 8 || config.Timeout < time.Millisecond || config.Timeout > 10*time.Minute || config.MaxStdoutBytes < 1 || config.MaxStdoutBytes > 16<<20 || config.MaxStderrBytes < 1 || config.MaxStderrBytes > 1<<20 || !filepath.IsAbs(config.TempRoot) || len(tools) < 1 || len(tools) > 8 {
		return nil, ErrInvalid
	}
	rootInfo, err := os.Lstat(config.TempRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	r := &Runner{config: config, tools: make(map[string]Tool), slots: make(chan struct{}, config.MaxConcurrent)}
	for _, tool := range tools {
		if !allowTestProgram && (tool.ID != "ffprobe" || !allowedProductionName(tool.Path)) {
			return nil, ErrInvalid
		}
		if !validName(tool.ID) || !filepath.IsAbs(tool.Path) || strings.ContainsRune(tool.Path, 0) || len(tool.Operations) < 1 || len(tool.Operations) > 16 {
			return nil, ErrInvalid
		}
		if _, exists := r.tools[tool.ID]; exists {
			return nil, ErrInvalid
		}
		info, err := os.Lstat(tool.Path)
		if err != nil || !info.Mode().IsRegular() || !executableAllowed(tool.Path, info) {
			return nil, ErrInvalid
		}
		copyTool := Tool{ID: tool.ID, Path: filepath.Clean(tool.Path), Operations: make(map[string][]string)}
		for operation, args := range tool.Operations {
			if !validName(operation) || len(args) > 64 {
				return nil, ErrInvalid
			}
			size := 0
			for _, argument := range args {
				if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) || len(argument) > 4096 {
					return nil, ErrInvalid
				}
				size += len(argument) + 1
			}
			if size > 24<<10 {
				return nil, ErrInvalid
			}
			copyTool.Operations[operation] = append([]string(nil), args...)
		}
		r.tools[tool.ID] = copyTool
	}
	return r, nil
}

type child interface {
	// ready waits for exit without releasing the process identity; reap is
	// called only after kill has also terminated remaining descendants.
	ready() error
	kill() error
	reap() (int, error)
	close() error
}

type capture struct {
	data   []byte
	count  int64
	failed bool
}

func drain(file *os.File, maximum int64, retain bool, exceeded func()) capture {
	var out capture
	buffer := make([]byte, 32<<10)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			remaining := maximum - out.count
			if retain && remaining > 0 {
				keep := int64(n)
				if keep > remaining {
					keep = remaining
				}
				out.data = append(out.data, buffer[:keep]...)
			}
			// Counts are saturated so an adversarial writer cannot overflow them.
			if out.count <= maximum {
				out.count += int64(n)
			}
			if out.count > maximum {
				exceeded()
			}
		}
		if err != nil {
			out.failed = !errors.Is(err, io.EOF)
			return out
		}
	}
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrCancelled
}

func (r *Runner) Run(ctx context.Context, request Request) (result Result, resultErr error) {
	if ctx == nil {
		return Result{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return Result{}, contextError(ctx)
	}
	tool, ok := r.tools[request.Tool]
	if !ok {
		return Result{}, ErrInvalid
	}
	arguments, ok := tool.Operations[request.Operation]
	if !ok {
		return Result{}, ErrInvalid
	}
	if request.Stdin != nil && !validInput(request.Stdin) {
		return Result{}, ErrInvalid
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return Result{}, ErrBusy
	}
	defer func() { <-r.slots }()
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	dir, err := os.MkdirTemp(r.config.TempRoot, "run-")
	if err != nil {
		return Result{}, ErrStart
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			result, resultErr = Result{}, ErrCleanup
		}
	}()
	stdin := request.Stdin
	if stdin == nil {
		stdin, err = os.Open(os.DevNull)
		if err != nil {
			return Result{}, ErrStart
		}
		defer stdin.Close()
	}
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		return Result{}, ErrStart
	}
	defer stdout.Close()
	defer stdoutWrite.Close()
	stderr, stderrWrite, err := os.Pipe()
	if err != nil {
		return Result{}, ErrStart
	}
	defer stderr.Close()
	defer stderrWrite.Close()
	if ctx.Err() != nil {
		return Result{}, contextError(ctx)
	}
	process, err := startChild(tool.Path, arguments, dir, environment(dir), stdin, stdoutWrite, stderrWrite)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return Result{}, ErrUnsupported
		}
		return Result{}, ErrStart
	}
	defer func() {
		if err := process.close(); err != nil && resultErr == nil {
			result, resultErr = Result{}, ErrCleanup
		}
	}()
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	limit := make(chan struct{})
	var once sync.Once
	exceeded := func() { once.Do(func() { close(limit) }) }
	outDone, errDone := make(chan capture, 1), make(chan capture, 1)
	go func() { outDone <- drain(stdout, r.config.MaxStdoutBytes, true, exceeded) }()
	go func() { errDone <- drain(stderr, r.config.MaxStderrBytes, false, exceeded) }()
	exited := make(chan error, 1)
	go func() { exited <- process.ready() }()
	var waitError error
	waited := false
	select {
	case <-ctx.Done():
		resultErr = contextError(ctx)
	case <-limit:
		resultErr = ErrOutputLimit
	case waitError = <-exited:
		waited = true
	}
	// Also terminate descendants when their direct parent exits normally.
	if err := process.kill(); err != nil {
		resultErr = ErrCleanup
	}
	if !waited {
		waitError = <-exited
	}
	code, reapError := process.reap()
	// A child that escaped a Linux process group could retain pipe handles.
	// Bound pipe joining even in this unsupported sandbox-escape scenario.
	drainClosed := make(chan struct{})
	drainTimer := time.AfterFunc(time.Second, func() {
		defer close(drainClosed)
		_ = stdout.Close()
		_ = stderr.Close()
	})
	out, diagnostic := <-outDone, <-errDone
	if !drainTimer.Stop() {
		<-drainClosed
	}
	if resultErr == nil {
		select {
		case <-limit:
			resultErr = ErrOutputLimit
		default:
		}
	}
	if resultErr == nil && ctx.Err() != nil {
		resultErr = contextError(ctx)
	}
	if resultErr == nil && (waitError != nil || reapError != nil || out.failed || diagnostic.failed) {
		resultErr = ErrCleanup
	}
	if resultErr == nil && code != 0 {
		resultErr = ErrExit
	}
	if resultErr != nil {
		return Result{}, resultErr
	}
	return Result{Stdout: out.data, ExitCode: code, StdoutBytes: out.count, StderrBytes: diagnostic.count}, nil
}

func environment(dir string) []string {
	env := []string{"LANG=C", "LC_ALL=C", "TMPDIR=" + dir, "TMP=" + dir, "TEMP=" + dir}
	return platformEnvironment(env)
}
