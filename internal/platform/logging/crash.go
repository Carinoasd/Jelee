package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Crash archive (G46.3). Process level panics and fatal errors used to
// reach only stderr. The archive keeps one masked report per crash in its
// own directory (default: crash/ next to the log file):
//
//   - a panic of the main goroutine is recovered by CapturePanic, logged,
//     archived and then re-raised, so the process still ends as before;
//   - a panic the HTTP boundary recovers is archived next to its record
//     (ArchivePanic);
//   - any other goroutine's unrecovered panic or a fatal runtime error is
//     written by the Go runtime to this process's pending file
//     (debug.SetCrashOutput, besides stderr). The next start converts the
//     pending file of a crashed process into a masked report and logs it
//     (Collect).
//
// Reports are masked like the panic records of the log (MaskStack): no
// panic value, argument words or absolute directories. Files are 0600 in a
// 0700 directory and only the newest Keep reports are kept.

// DefaultCrashKeep and MaxCrashKeep bound the number of kept reports.
const (
	DefaultCrashKeep = 20
	MaxCrashKeep     = 1000
)

// Report sources.
const (
	crashSourceProcess = "process_panic"
	crashSourceRequest = "request_panic"
	crashSourceRuntime = "crash_output"
)

// crashPendingMaxAge is the age after which an empty pending file of
// another process is taken as left behind by a killed process.
const crashPendingMaxAge = 24 * time.Hour

// crashRawMax bounds how much of a pending file is read.
const crashRawMax = 1 << 20

// crashArchiveInterval bounds archived request panics to one per interval,
// so a panicking route cannot turn requests into disk writes.
const crashArchiveInterval = time.Second

const crashTimeLayout = "20060102T150405.000000000Z"

var (
	crashNamePattern   = regexp.MustCompile(`^crash-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z(-[0-9]{1,3})?\.log$`)
	crashPendingPrefix = "pending-"
	crashPendingSuffix = ".raw"
)

// validCrashName accepts the base name of a crash report.
func validCrashName(name string) bool { return crashNamePattern.MatchString(name) }

// CrashOptions configure the crash archive. An empty Dir disables it.
type CrashOptions struct {
	Dir  string
	Keep int
}

// CrashArchive writes masked crash reports into one directory. A nil
// archive does nothing. It is safe for concurrent use.
type CrashArchive struct {
	dir  string
	keep int
	now  func() time.Time
	mu   sync.Mutex
	// pending is this process's crash output file while armed.
	pending string
	// last is the time of the last archived request panic.
	last time.Time
}

// OpenCrashArchive creates the directory (0700) when needed. It returns a
// nil archive for an empty Dir.
func OpenCrashArchive(opts CrashOptions) (*CrashArchive, error) {
	if opts.Dir == "" {
		return nil, nil
	}
	if !filepath.IsAbs(opts.Dir) || strings.ContainsAny(opts.Dir, "\x00\r\n") {
		return nil, errors.New("crash archive directory must be absolute")
	}
	keep := opts.Keep
	if keep == 0 {
		keep = DefaultCrashKeep
	}
	if keep < 1 || keep > MaxCrashKeep {
		return nil, errors.New("crash archive keep count is outside supported range")
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, errors.New("crash archive directory cannot be created")
	}
	return &CrashArchive{dir: filepath.Clean(opts.Dir), keep: keep, now: time.Now}, nil
}

// Dir is the archive directory; empty for a nil archive.
func (a *CrashArchive) Dir() string {
	if a == nil {
		return ""
	}
	return a.dir
}

// Record writes one masked report and prunes the oldest beyond Keep. It
// returns the report's base name.
func (a *CrashArchive) Record(source, panicType string, stack []byte) (string, error) {
	if a == nil {
		return "", nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.record(a.now(), source, panicType, MaskStack(string(stack)))
}

func (a *CrashArchive) record(at time.Time, source, panicType, masked string) (string, error) {
	if !safeTypeName(panicType) && !safeLowerIdent(panicType) {
		panicType = "unknown"
	}
	var body bytes.Buffer
	fmt.Fprintf(&body, "Jelee crash report\ntime: %s\nsource: %s\ntype: %s\n\n%s\n", at.UTC().Format(time.RFC3339Nano), source, panicType, masked)
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	base := "crash-" + at.UTC().Format(crashTimeLayout)
	var file *os.File
	var name string
	for n := 0; n < 1000; n++ {
		name = base + ".log"
		if n > 0 {
			name = base + "-" + strconv.Itoa(n) + ".log"
		}
		file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	_, err = file.Write(body.Bytes())
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		_ = root.Remove(name)
		return "", err
	}
	return name, a.prune(root)
}

// prune removes the oldest reports beyond keep. Other files are left alone.
func (a *CrashArchive) prune(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	reports := names[:0]
	for _, name := range names {
		if validCrashName(name) {
			reports = append(reports, name)
		}
	}
	sort.Strings(reports)
	var errs []error
	for len(reports) > a.keep {
		if err := root.Remove(reports[0]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		reports = reports[1:]
	}
	return errors.Join(errs...)
}

// Reports lists the report base names, oldest first.
func (a *CrashArchive) Reports() ([]string, error) {
	if a == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && validCrashName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Arm makes the Go runtime copy the crash output of this process (an
// unrecovered panic in any goroutine, a fatal error) to a pending file in
// the archive directory, besides stderr. Disarm undoes it.
func (a *CrashArchive) Arm() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending != "" {
		return nil
	}
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := crashPendingPrefix + a.now().UTC().Format(crashTimeLayout) + "-" + strconv.Itoa(os.Getpid()) + crashPendingSuffix
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// The runtime keeps its own duplicate of the descriptor.
	err = debug.SetCrashOutput(file, debug.CrashOptions{})
	_ = file.Close()
	if err != nil {
		_ = root.Remove(name)
		return err
	}
	a.pending = name
	return nil
}

// Disarm stops the crash output copy and removes the pending file.
func (a *CrashArchive) Disarm() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == "" {
		return
	}
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	if root, err := os.OpenRoot(a.dir); err == nil {
		_ = root.Remove(a.pending)
		_ = root.Close()
	}
	a.pending = ""
}

// Collect converts the pending files that crashed processes left behind
// into reports and logs one ERROR record per report (component system,
// code process_crashed). Empty pending files of other processes older than
// a day are removed. It returns the number of reports written.
func (a *CrashArchive) Collect(logger *slog.Logger) int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	root, err := os.OpenRoot(a.dir)
	if err != nil {
		return 0
	}
	defer func() { _ = root.Close() }()
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return 0
	}
	collected := 0
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || name == a.pending || !strings.HasPrefix(name, crashPendingPrefix) || !strings.HasSuffix(name, crashPendingSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() == 0 {
			if a.now().Sub(info.ModTime()) > crashPendingMaxAge {
				_ = root.Remove(name)
			}
			continue
		}
		raw, err := readPending(root, name)
		if err != nil {
			continue
		}
		masked := MaskStack(string(raw))
		report, err := a.record(info.ModTime(), crashSourceRuntime, crashKind(raw), masked)
		if err != nil {
			continue
		}
		_ = root.Remove(name)
		collected++
		if logger != nil {
			logger.Error("previous process crashed", "component", "system", "code", "process_crashed", "panicType", crashKind(raw),
				"stack", slog.AnyValue(panicStack{text: masked}), "crashFile", report)
		}
	}
	return collected
}

func readPending(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, crashRawMax))
}

// crashKind names the kind of a runtime crash dump from its first line,
// without its value: panic, fatal_error or crash.
func crashKind(raw []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimLeft(raw, "\n"), []byte("\n"))
	switch {
	case bytes.HasPrefix(line, []byte("panic: ")):
		return "panic"
	case bytes.HasPrefix(line, []byte("fatal error: ")):
		return "fatal_error"
	}
	return "crash"
}

// installed is the archive request panics go to (SetCrashArchive).
var installed atomic.Pointer[CrashArchive]

// SetCrashArchive installs the process's archive for ArchivePanic; nil
// removes it.
func SetCrashArchive(a *CrashArchive) { installed.Store(a) }

// ArchivePanic archives the stack of a panic a recover handler caught (the
// HTTP boundary) in the installed archive, at most one per second. It
// returns the report's base name, or "" when nothing was archived.
func ArchivePanic(recovered any, stack []byte) string {
	a := installed.Load()
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if !a.last.IsZero() && now.Sub(a.last) < crashArchiveInterval {
		return ""
	}
	// A prune failure still leaves the report written.
	name, _ := a.record(now, crashSourceRequest, fmt.Sprintf("%T", recovered), MaskStack(string(stack)))
	if name == "" {
		return ""
	}
	a.last = now
	return name
}

// CapturePanic records a panic of the calling goroutine and re-raises it.
// Defer it directly in main: it logs an ERROR record (component system,
// code process_panic, masked stack), archives the report, flushes and
// closes the router, disarms the crash output (the report is already
// written) and panics again with the same value, so the process ends as it
// did before.
func CapturePanic(r *Router, a *CrashArchive) {
	recovered := recover()
	if recovered == nil {
		return
	}
	stack := debug.Stack()
	panicType := fmt.Sprintf("%T", recovered)
	name, _ := a.Record(crashSourceProcess, panicType, stack)
	if r != nil {
		attrs := []any{"component", "system", "code", "process_panic", "panicType", panicType, "stack", PanicStack(stack)}
		if name != "" {
			attrs = append(attrs, "crashFile", name)
		}
		r.Logger().Error("process panic", attrs...)
		_ = r.Close()
	}
	a.Disarm()
	panic(recovered)
}
