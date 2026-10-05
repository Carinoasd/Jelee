package diag

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/tools"
)

// Database is the read-only view doctor and diag export need. The CLI adapts
// the PostgreSQL diagnostics pool to it; tests substitute fakes.
type Database interface {
	Ping(ctx context.Context) error
	Migration(ctx context.Context) (version int64, dirty, present bool, err error)
	LibraryRoots(ctx context.Context, limit int) ([]Root, error)
	TableStats(ctx context.Context, limit int) ([]TableStat, error)
	JobSummary(ctx context.Context, since time.Time, limit int) ([]JobGroup, error)
	Close()
}

// Root is a registered media root. Path is used for local checks and as a
// sensitive-value needle; it is never serialised.
type Root struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Path      string `json:"-"`
}

type TableStat struct {
	Name          string `json:"name"`
	EstimatedRows int64  `json:"estimatedRows"`
	TotalBytes    int64  `json:"totalBytes"`
}

type JobGroup struct {
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	ErrorCode string    `json:"errorCode,omitempty"`
	Count     int64     `json:"count"`
	Latest    time.Time `json:"latest"`
}

// Open errors are classified by these sentinels; anything else is treated as
// an unreachable database.
var (
	ErrDBConfig = errors.New("database configuration invalid")
	ErrDBAuth   = errors.New("database credentials rejected")
)

// Opener connects to the configured database.
type Opener func(ctx context.Context, dsn string) (Database, error)

// ExternalOutcome classifies the optional TMDB connectivity probe.
type ExternalOutcome int

const (
	ExternalOK ExternalOutcome = iota
	ExternalCredentials
	ExternalRateLimited
	ExternalUnreachable
)

// ToolCandidate is one location where the pinned ffprobe may be installed.
// Label is printed; Path is not.
type ToolCandidate struct {
	Label string
	Path  string
}

// Environment carries every input of a doctor run. Zero fields select the
// production behaviour; tests replace them to inject faults.
type Environment struct {
	Lookup    func(string) (string, bool)
	Config    config.Config
	ConfigErr error
	// SchemaVersion is the clean migration version this binary requires.
	SchemaVersion int64
	OpenDB        Opener
	// Project is the working directory used for project-local tools.
	Project string
	TempDir string
	// Tools overrides the ffprobe candidates; ToolSpec overrides the
	// embedded manifest lookup.
	Tools    []ToolCandidate
	ToolSpec func() (tools.FFprobeSpecification, error)
	// MatroskaSpec overrides the embedded mkvtoolnix/MediaInfo lookup.
	MatroskaSpec func(name string) (tools.MatroskaToolSpecification, error)
	// OCRSpec overrides the embedded Tesseract lookup (G15.6).
	OCRSpec  func() (tools.OCRToolSpecification, error)
	Statfs   func(path string) (DiskUsage, error)
	Disk     DiskThresholds
	MaxRoots int
	// External enables the TMDB probe; TMDB performs it.
	External bool
	TMDB     func(ctx context.Context) ExternalOutcome
	Now      func() time.Time
}

const (
	DefaultMaxRoots = 64
	MaxRootsLimit   = 1024
)

// Session owns a lazily opened database shared by all checks and the
// exporter.
type Session struct {
	env   Environment
	once  sync.Once
	db    Database
	dbErr error
}

func NewSession(env Environment) *Session {
	if env.Lookup == nil {
		env.Lookup = os.LookupEnv
	}
	if env.Now == nil {
		env.Now = time.Now
	}
	if env.TempDir == "" {
		env.TempDir = os.TempDir()
	}
	if env.MaxRoots <= 0 {
		env.MaxRoots = DefaultMaxRoots
	}
	if env.MaxRoots > MaxRootsLimit {
		env.MaxRoots = MaxRootsLimit
	}
	if env.Statfs == nil {
		env.Statfs = statfs
	}
	if env.Disk == (DiskThresholds{}) {
		env.Disk = DefaultDiskThresholds()
	}
	return &Session{env: env}
}

// errDBNotConfigured marks a session without a database URL.
var errDBNotConfigured = errors.New("database not configured")

func (s *Session) database(ctx context.Context) (Database, error) {
	s.once.Do(func() {
		if s.env.Config.DatabaseURL == "" {
			s.dbErr = errDBNotConfigured
			return
		}
		if s.env.OpenDB == nil {
			s.dbErr = ErrDBConfig
			return
		}
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s.db, s.dbErr = s.env.OpenDB(openCtx, s.env.Config.DatabaseURL)
		if s.dbErr == nil && s.db == nil {
			s.dbErr = ErrDBConfig
		}
	})
	return s.db, s.dbErr
}

func (s *Session) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// Checks returns the doctor checks in report order.
func (s *Session) Checks() []Check {
	checks := []Check{
		CheckFunc{"config", s.checkConfig},
		CheckFunc{"database", s.checkDatabase},
		CheckFunc{"migrations", s.checkMigrations},
		CheckFunc{"library_roots", s.checkRoots},
		CheckFunc{"tools", s.checkTools},
		CheckFunc{"matroska_tools", s.checkMatroskaTools},
		CheckFunc{"subtitle_ocr", s.checkSubtitleOCR},
		CheckFunc{"disk", s.checkDisk},
		CheckFunc{"network", s.checkNetwork},
		CheckFunc{"directories", s.checkDirectories},
		CheckFunc{"privacy", s.checkPrivacy},
		CheckFunc{"devmode", s.checkDevMode},
	}
	if s.env.External {
		checks = append(checks, CheckFunc{"external", s.checkExternal})
	}
	return checks
}

// Doctor runs every check.
func (s *Session) Doctor(ctx context.Context) Report {
	return Run(ctx, s.Checks(), s.env.Now)
}

// DoctorSelected runs only the named checks; unknown names are returned
// without running anything.
func (s *Session) DoctorSelected(ctx context.Context, names []string) (Report, []string) {
	checks, unknown := SelectChecks(s.Checks(), names)
	if len(unknown) > 0 {
		return Report{}, unknown
	}
	return Run(ctx, checks, s.env.Now), nil
}
