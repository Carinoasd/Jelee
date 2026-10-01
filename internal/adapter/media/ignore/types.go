// Package ignoresource observes reachable .jeleeignore files through strict
// native handles. Its cache saves compilation, never filesystem observation.
package ignoresource

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

const (
	MaxConcurrent        = 2
	MaxDuration          = 30 * time.Second
	MaxRootBytes         = 4096
	MaxCompileInputBytes = 32 << 20
	MaxCacheEntries      = 64
	MaxCacheWeight       = 16 << 20
)

var (
	ErrInvalid     = errors.New("ignore_source_invalid")
	ErrRead        = errors.New("ignore_source_read")
	ErrUnsafe      = errors.New("ignore_source_unsafe")
	ErrUnavailable = errors.New("ignore_source_unavailable")
	ErrChanged     = errors.New("ignore_source_changed")
	ErrLimit       = errors.New("ignore_source_limit")
	ErrWorkLimit   = errors.New("ignore_source_work_limit")
	ErrBusy        = errors.New("ignore_source_busy")
	// Only a missing fixed leaf in a successfully opened parent yields absence.
	errAbsent = errors.New("ignore_source_absent")
)

// Observation describes this candidate's reachable ancestor chain only. The
// token identifies an observation, not a persistent lease or scan generation.
type Observation struct {
	Match       ignore.Match
	Diagnostics ignore.Diagnostics
	token       [32]byte
	chain       []directoryObservation
}

func (o Observation) Token() [32]byte { return o.token }
func (Observation) String() string    { return "ignore observation (data redacted)" }
func (Observation) GoString() string  { return "ignore observation (data redacted)" }

type Resolver struct {
	slots chan struct{}
	mu    sync.Mutex
	cache programCache
}

func NewResolver() *Resolver { return &Resolver{slots: make(chan struct{}, MaxConcurrent)} }

// These private, per-invocation ports support deterministic fault and ownership
// tests. Exported callers cannot replace them or supply their own filesystem.
type sourceAccess struct {
	openRoot func(string) (directory, error)
	compile  func(context.Context, []ignore.Source, ignore.Options) (*ignore.Program, error)
}
type directory interface {
	Stat() (fileState, error)
	OpenDirectory(string) (directory, error)
	OpenRule() (sourceFile, error)
	Close() error
}
type sourceFile interface {
	io.Reader
	Stat() (fileState, error)
	Close() error
}

// Native implementations encode the volume and full file identifier here.
// Metadata comes from the held handle, never a second path lookup.
type fileIdentity [32]byte
type nodeKind uint8

const (
	nodeRegular nodeKind = iota + 1
	nodeDirectory
	nodeOther
)

type fileState struct {
	identity         fileIdentity
	size             int64
	modifiedUnixNano int64
	kind             nodeKind
}
