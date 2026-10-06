package logging

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// New returns a synchronous JSON logger with the default whitelist: IPs and
// paths redacted. It is kept for tests and tools; servers use Open.
func New(output io.Writer) *slog.Logger {
	return slog.New(&levelHandler{next: newJSONHandler(output, defaultRedactor), levels: newLevels(slog.LevelInfo)})
}

func newJSONHandler(output io.Writer, red *redactor) slog.Handler {
	// The level gate lives in levelHandler; the formatter accepts everything
	// it is given so component thresholds below the global level work.
	return slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.Level(-1 << 20), ReplaceAttr: red.replace})
}

func SafeMethod(method string) string {
	switch strings.ToUpper(method) {
	case "GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS":
		return strings.ToUpper(method)
	}
	return "OTHER"
}

// Format selects the stdout rendering.
type Format string

const (
	FormatJSON    Format = "json"
	FormatConsole Format = "console"
)

// Output selects the sinks. Files are always written as JSON.
type Output string

const (
	OutputStdout Output = "stdout"
	OutputFile   Output = "file"
	OutputBoth   Output = "both"
)

// DefaultBufferEntries bounds each sink queue.
const DefaultBufferEntries = 4096

// Options configures Open. Zero values select production defaults: INFO
// level (DEBUG stays off unless requested), JSON on stdout, redacted IPs and
// paths.
type Options struct {
	Level         slog.Level
	Components    map[string]slog.Level
	Format        Format
	Output        Output
	File          RotateOptions
	BufferEntries int
	IPMode        IPMode
	PathMode      PathMode
	PathRoots     []string
	Forwarders    []Forwarder
}

// Router owns the logger, its level thresholds and its asynchronous sinks.
type Router struct {
	logger  *slog.Logger
	levels  *levels
	red     *redactor
	writers []*asyncWriter
	// file is the rotating log file; nil in stdout-only mode.
	file *RotatingFile
}

// Open builds the logging pipeline. stdout is never closed by the router.
func Open(opts Options, stdout io.Writer) (*Router, error) {
	if opts.Format == "" {
		opts.Format = FormatJSON
	}
	if opts.Output == "" {
		opts.Output = OutputStdout
	}
	if opts.BufferEntries == 0 {
		opts.BufferEntries = DefaultBufferEntries
	}
	if opts.IPMode == "" {
		opts.IPMode = IPRedact
	}
	if opts.PathMode == "" {
		opts.PathMode = PathRedact
	}
	switch {
	case opts.Format != FormatJSON && opts.Format != FormatConsole:
		return nil, errors.New("invalid log format")
	case opts.Output != OutputStdout && opts.Output != OutputFile && opts.Output != OutputBoth:
		return nil, errors.New("invalid log output")
	case opts.BufferEntries < 1:
		return nil, errors.New("invalid log buffer size")
	case opts.IPMode != IPRedact && opts.IPMode != IPMask:
		return nil, errors.New("invalid log IP mode")
	case opts.PathMode != PathRedact && opts.PathMode != PathRelative:
		return nil, errors.New("invalid log path mode")
	case opts.Output != OutputFile && stdout == nil:
		return nil, errors.New("stdout log output is unavailable")
	}
	r := &Router{levels: newLevels(opts.Level), red: newRedactor(opts.IPMode, opts.PathMode, opts.PathRoots)}
	for component, level := range opts.Components {
		if err := r.levels.set(component, level); err != nil {
			return nil, err
		}
	}
	var handlers []slog.Handler
	if opts.Output != OutputFile {
		w := r.add(writerOnly{stdout}, opts.BufferEntries)
		if opts.Format == FormatConsole {
			handlers = append(handlers, newConsoleHandler(w, r.red))
		} else {
			handlers = append(handlers, newJSONHandler(w, r.red))
		}
	}
	var jsonSinks []io.Writer
	if opts.Output != OutputStdout {
		file, err := OpenRotatingFile(opts.File)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.file = file
		jsonSinks = append(jsonSinks, r.add(file, opts.BufferEntries))
	}
	for _, f := range opts.Forwarders {
		if f == nil {
			continue
		}
		jsonSinks = append(jsonSinks, r.add(f, opts.BufferEntries))
	}
	switch len(jsonSinks) {
	case 0:
	case 1:
		handlers = append(handlers, newJSONHandler(jsonSinks[0], r.red))
	default:
		// io.MultiWriter stops at the first error; asyncWriter never fails.
		handlers = append(handlers, newJSONHandler(io.MultiWriter(jsonSinks...), r.red))
	}
	var next slog.Handler
	if len(handlers) == 1 {
		next = handlers[0]
	} else {
		next = slog.NewMultiHandler(handlers...)
	}
	r.logger = slog.New(&levelHandler{next: next, levels: r.levels})
	return r, nil
}

func (r *Router) add(sink io.Writer, capacity int) *asyncWriter {
	w := newAsyncWriter(sink, capacity)
	r.writers = append(r.writers, w)
	return w
}

// writerOnly hides Close and Sync so the router never closes the process
// stdout.
type writerOnly struct{ io.Writer }

func (r *Router) Logger() *slog.Logger { return r.logger }

// SetLevel changes a threshold immediately for every derived logger. An
// empty component (GlobalComponent) changes the global level; aliases such
// as "ignore" change their scope.
func (r *Router) SetLevel(component string, level slog.Level) error {
	return r.levels.set(component, level)
}

// ResetLevel makes a component follow the global level again.
func (r *Router) ResetLevel(component string) error { return r.levels.reset(component) }

// Level returns the effective threshold for a component.
func (r *Router) Level(component string) slog.Level { return r.levels.threshold(component) }

// SetOverrides replaces the administrator level overrides (G46.2): each
// sets the level of one scope, or of the global level for GlobalComponent,
// until it expires. Expired entries are ignored. An unknown scope or a
// mandatory one (audit, security) refuses the whole list.
func (r *Router) SetOverrides(overrides []LevelOverride) error {
	return r.levels.setOverrides(overrides)
}

// SetDeveloperVerbose applies the developer mode debug_verbose_logging
// switch: while on, the global level is DEBUG unless an administrator
// override sets it. It reports whether the switch changed.
func (r *Router) SetDeveloperVerbose(on bool) bool { return r.levels.setDeveloperVerbose(on) }

// LevelReport lists the global level followed by every scope with its
// configured, overridden and effective level.
func (r *Router) LevelReport() []ScopeLevel { return r.levels.report() }

// SetFileRetention changes the age and total size limits of the log file
// backups (G46.9). Zero disables a limit. Without a log file it does
// nothing.
func (r *Router) SetFileRetention(maxAge time.Duration, maxTotalBytes int64) {
	if r.file != nil {
		r.file.SetRetention(maxAge, maxTotalBytes)
	}
}

// PruneFiles applies the backup limits now; rotations apply them too.
func (r *Router) PruneFiles() error {
	if r.file == nil {
		return nil
	}
	return r.file.Prune()
}

// HasFile reports whether the router writes a log file.
func (r *Router) HasFile() bool { return r.file != nil }

// SetPathRoots replaces the roots used by the relative path mode.
func (r *Router) SetPathRoots(roots []string) { r.red.setRoots(roots) }

// SetDeveloperLogging connects the developer mode switches: while sql
// (debug_sql_logging) or body (debug_body_logging) reports true, the fields
// of that log pass the whitelist with secrets masked. A nil switch keeps its
// fields redacted, which is also the state of a router never connected.
func (r *Router) SetDeveloperLogging(sql, body func() bool) {
	r.red.dev.Store(&devSwitches{sql: sql, body: body})
}

// Dropped counts records discarded because a sink queue was full or closed.
func (r *Router) Dropped() uint64 {
	var n uint64
	for _, w := range r.writers {
		n += w.Dropped()
	}
	return n
}

// WriteFailures counts records a sink rejected.
func (r *Router) WriteFailures() uint64 {
	var n uint64
	for _, w := range r.writers {
		n += w.Failed()
	}
	return n
}

// Flush waits until every record logged before the call reached its sinks.
func (r *Router) Flush() error {
	var errs error
	for _, w := range r.writers {
		errs = errors.Join(errs, w.Flush())
	}
	return errs
}

// Close drains every queue and closes owned sinks.
func (r *Router) Close() error {
	r.levels.stop()
	var errs error
	for _, w := range r.writers {
		errs = errors.Join(errs, w.Close())
	}
	return errs
}

// ApplySettings applies the stored logging settings (G46.2, G46.9): the
// administrator level overrides and the log file retention. Entries that
// name an unknown or mandatory scope or an unknown level are skipped, so a
// row written by a newer version never disables the rest.
func (r *Router) ApplySettings(s domain.LogSettings) {
	overrides := make([]LevelOverride, 0, len(s.Overrides))
	for _, o := range s.Overrides {
		level, err := ParseLevel(o.Level)
		if err != nil {
			continue
		}
		if o.Component != GlobalComponent {
			if _, ok := ScopeFor(o.Component); !ok || Mandatory(o.Component) {
				continue
			}
		}
		overrides = append(overrides, LevelOverride{Component: o.Component, Level: level, ExpiresAt: o.ExpiresAt})
	}
	// Every entry was checked above, so the list is accepted as a whole.
	_ = r.SetOverrides(overrides)
	if domain.ValidLogRetention(s.LogDays, s.LogMaxTotalMB) {
		r.SetFileRetention(time.Duration(s.LogDays)*24*time.Hour, int64(s.LogMaxTotalMB)<<20)
	}
}
