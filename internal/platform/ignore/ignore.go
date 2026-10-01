// Package ignore compiles bounded, immutable .jeleeignore rule snapshots.
// It performs no I/O and does not discover files or grant filesystem access.
package ignore

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"
)

const ProgramVersion = "jeleeignore-v1"

const (
	MaxSources            = 128
	MaxSourceBytes        = 256 << 10
	MaxTotalSourceBytes   = 4 << 20
	MaxDecodedSourceBytes = 384 << 10
	MaxTotalDecodedBytes  = 4 << 20
	MaxSourceLines        = 4096
	MaxTotalLines         = 16384
	MaxLineBytes          = 4096
	MaxRules              = 16384
	MaxTokens             = 131072
	MaxPatternTokens      = 4096
	MaxClasses            = 4096
	MaxPathBytes          = 1024
	MaxPathComponents     = 128
	MaxCompileWork        = 16777216
	MaxEvaluateWork       = 8388608
	MaxDiagnostics        = 64
)

var (
	ErrInvalid   = errors.New("ignore_invalid_input")
	ErrLimit     = errors.New("ignore_limit")
	ErrWorkLimit = errors.New("ignore_work_limit")
)

type CaseMode uint8

const (
	CaseSensitive CaseMode = iota
	CaseASCIIInsensitive
)

type Options struct{ Case CaseMode }
type Source struct {
	Path string
	Text []byte `json:"-"`
}

func (Source) String() string   { return "ignore source (data redacted)" }
func (Source) GoString() string { return "ignore source (data redacted)" }

type Kind uint8

const (
	File Kind = iota + 1
	Directory
)

type Outcome uint8

const (
	Unmatched Outcome = iota
	Include
	Exclude
)

type Match struct {
	Outcome       Outcome
	Source        string
	Line          int
	MatchedPath   string
	ParentBlocked bool
}
type Diagnostic struct {
	Source string
	Line   int
	Code   string
}
type Diagnostics struct {
	Total     int
	Truncated bool
	Items     []Diagnostic
}

type compiledSource struct {
	path, base        string
	depth, start, end int
}
type rule struct {
	source, line, start, count             int
	basename, directory, negative, invalid bool
}
type tokenKind uint8

const (
	literal tokenKind = iota
	oneByte
	byteClass
	star
	globstar
	componentStar
)

type matchToken struct {
	kind  tokenKind
	value byte
	class uint16
}
type bitmap [32]byte

func (b *bitmap) add(c byte)          { b[c/8] |= 1 << (c % 8) }
func (b bitmap) contains(c byte) bool { return b[c/8]&(1<<(c%8)) != 0 }

// Every field is private. Evaluation scratch and work meters belong to calls,
// never the Program. The zero value is deliberately not an empty valid program.
type Program struct {
	valid       bool
	mode        CaseMode
	sources     []compiledSource
	byBase      map[string]int
	rules       []rule
	tokens      []matchToken
	classes     []bitmap
	maxPattern  int
	diagnostics Diagnostics
}

func (Program) String() string   { return "ignore program (data redacted)" }
func (Program) GoString() string { return "ignore program (data redacted)" }
func (p *Program) Diagnostics() Diagnostics {
	if p == nil {
		return Diagnostics{}
	}
	v := p.diagnostics
	v.Items = append([]Diagnostic(nil), v.Items...)
	return v
}

// spend checks cancellation at most 1,024 charged units apart. Large bounded
// operations must call it before each chunk, not once after an unbounded loop.
type workMeter struct {
	ctx                   context.Context
	remaining, untilCheck int
}

func newWorkMeter(ctx context.Context, limit int) *workMeter {
	return &workMeter{ctx: ctx, remaining: limit}
}
func (m *workMeter) check() error { return m.ctx.Err() }
func (m *workMeter) spend(n int) error {
	if n < 0 {
		return ErrInvalid
	}
	if n >= m.untilCheck {
		if err := m.check(); err != nil {
			return err
		}
		m.untilCheck = 1024
	}
	if n > m.remaining {
		return ErrWorkLimit
	}
	m.remaining -= n
	m.untilCheck -= n
	return nil
}

// Paths are already canonical values; cleaning would turn rejected input into
// a different rule source or candidate. Case folding is never applied here.
func validatePath(path string, m *workMeter) (int, error) {
	if path == "" || len(path) > MaxPathBytes {
		if len(path) > MaxPathBytes {
			return 0, ErrLimit
		}
		return 0, ErrInvalid
	}
	components, start := 1, 0
	for i := 0; i < len(path); {
		r, size := utf8.DecodeRuneInString(path[i:])
		if err := m.spend(size); err != nil {
			return 0, err
		}
		if r == utf8.RuneError && size == 1 || unicode.IsControl(r) || r == '\\' || r == ':' {
			return 0, ErrInvalid
		}
		if r == '/' {
			if i == start || path[start:i] == "." || path[start:i] == ".." {
				return 0, ErrInvalid
			}
			components++
			if components > MaxPathComponents {
				return 0, ErrLimit
			}
			start = i + 1
		}
		i += size
	}
	if start == len(path) || path[start:] == "." || path[start:] == ".." {
		return 0, ErrInvalid
	}
	return components, nil
}

func fold(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
