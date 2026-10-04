package diag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/tools"
)

// Values that must never reach doctor output or a bundle.
const (
	testPassword = "Sup3rSecretDbPass"
	testDBHost   = "db.internal.example"
	testDSN      = "postgres://jelee:" + testPassword + "@" + testDBHost + ":5432/jelee?sslmode=disable"
	testTMDBKey  = "0123456789abcdef0123456789abcdef"
)

type fakeDB struct {
	pingErr   error
	version   int64
	dirty     bool
	missing   bool
	migErr    error
	roots     []Root
	rootsErr  error
	tables    []TableStat
	tablesErr error
	jobs      []JobGroup
	jobsErr   error
	closed    bool
}

func (f *fakeDB) Ping(context.Context) error { return f.pingErr }
func (f *fakeDB) Migration(context.Context) (int64, bool, bool, error) {
	return f.version, f.dirty, !f.missing, f.migErr
}
func (f *fakeDB) LibraryRoots(_ context.Context, limit int) ([]Root, error) {
	if len(f.roots) > limit {
		return f.roots[:limit], f.rootsErr
	}
	return f.roots, f.rootsErr
}
func (f *fakeDB) TableStats(context.Context, int) ([]TableStat, error) { return f.tables, f.tablesErr }
func (f *fakeDB) JobSummary(context.Context, time.Time, int) ([]JobGroup, error) {
	return f.jobs, f.jobsErr
}
func (f *fakeDB) Close() { f.closed = true }

const testSchema = 60

var fakeToolBytes = []byte("pinned ffprobe stand-in")

func fakeSpec() (tools.FFprobeSpecification, error) {
	sum := sha256.Sum256(fakeToolBytes)
	return tools.FFprobeSpecification{Platform: "linux-amd64", VendorVersion: "7.1-test", SHA256: hex.EncodeToString(sum[:]), ExecutablePath: ".tools/ffprobe"}, nil
}

func plentyDisk(string) (DiskUsage, error) {
	return DiskUsage{TotalBytes: 100 << 30, FreeBytes: 50 << 30, TotalInodes: 1 << 20, FreeInodes: 1 << 19, InodesKnown: true}, nil
}

func lookupMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// healthyEnv returns an environment in which every check passes, so each
// test injects exactly one fault.
func healthyEnv(t *testing.T) (Environment, *fakeDB) {
	t.Helper()
	db := &fakeDB{version: testSchema}
	tempDir := t.TempDir()
	cfg := config.Config{Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: testDSN, MaxConnections: 8, MaxStreams: 8, RequestTimeoutSeconds: 15, Logging: config.DefaultLoggingConfig()}
	toolPath := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(toolPath, fakeToolBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	env := Environment{
		Lookup: lookupMap(map[string]string{}), Config: cfg, SchemaVersion: testSchema,
		OpenDB:  func(context.Context, string) (Database, error) { return db, nil },
		TempDir: tempDir, Tools: []ToolCandidate{{Label: "runtime", Path: toolPath}}, ToolSpec: fakeSpec,
		Statfs: plentyDisk, Now: func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
	}
	env.MatroskaSpec = fakeMatroska(toolPath)
	env.OCRSpec = fakeOCR(toolPath)
	return env, db
}

// fakeOCR points the OCR executable and every language at one stand-in.
func fakeOCR(path string) func() (tools.OCRToolSpecification, error) {
	sum := sha256.Sum256(fakeToolBytes)
	file := tools.RuntimeFile{Path: ".tools/none/tesseract", ContainerPath: path, SHA256: hex.EncodeToString(sum[:])}
	return func() (tools.OCRToolSpecification, error) {
		languages := map[string]tools.RuntimeFile{}
		for _, code := range tools.OCRLanguages {
			languages[code] = file
		}
		return tools.OCRToolSpecification{Version: "5-test", Executable: file, Languages: languages}, nil
	}
}

func TestSubtitleOCRToolFaults(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		env, _ := healthyEnv(t)
		r := runCheck(t, env, "subtitle_ocr")
		expect(t, r, StatusOK, CodeOCRVerified)
		if len(r.Findings) != 1+len(tools.OCRLanguages) || r.Facts["version"] != "5-test" || r.Facts["enabled"] != "false" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("missing is optional unless OCR is enabled", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.OCRSpec = fakeOCR(filepath.Join(t.TempDir(), "none"))
		expect(t, runCheck(t, env, "subtitle_ocr"), StatusWarn, CodeOCRMissing)
		env.Config.SubtitleOCR = config.DefaultSubtitleOCRConfig()
		env.Config.SubtitleOCR.Enable = true
		env.Config.SubtitleOCR.Languages = []string{"chi_tra", "eng"}
		r := runCheck(t, env, "subtitle_ocr")
		expect(t, r, StatusFail, CodeOCRMissing)
		required := map[string]bool{"tesseract": true, "tessdata/eng": true, "tessdata/chi_tra": true}
		for _, f := range r.Findings {
			if required[f.Subject] != (f.Status == StatusFail) {
				t.Fatalf("finding %+v", f)
			}
		}
		if r.Facts["languages"] != "chi_tra+eng" || r.Facts["concurrency"] != "1" || r.Facts["pictures_per_minute"] != "60" {
			t.Fatalf("facts %+v", r.Facts)
		}
	})
	t.Run("hash mismatch and unreadable", func(t *testing.T) {
		env, _ := healthyEnv(t)
		bad := filepath.Join(t.TempDir(), "tampered")
		if err := os.WriteFile(bad, []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		env.OCRSpec = fakeOCR(bad)
		r := runCheck(t, env, "subtitle_ocr")
		expect(t, r, StatusFail, CodeOCRMismatch)
		if r.Findings[0].Subject != "tesseract:runtime" {
			t.Fatalf("subject = %q", r.Findings[0].Subject)
		}
		env.OCRSpec = fakeOCR(t.TempDir())
		expect(t, runCheck(t, env, "subtitle_ocr"), StatusFail, CodeOCRUnreadable)
	})
	t.Run("unsupported platform and broken manifest", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.OCRSpec = func() (tools.OCRToolSpecification, error) {
			return tools.OCRToolSpecification{}, errors.New("tool_platform_unsupported")
		}
		expect(t, runCheck(t, env, "subtitle_ocr"), StatusWarn, CodeOCRUnsupported)
		env.Config.SubtitleOCR.Enable = true
		expect(t, runCheck(t, env, "subtitle_ocr"), StatusFail, CodeOCRUnsupported)
		env.OCRSpec = func() (tools.OCRToolSpecification, error) {
			return tools.OCRToolSpecification{}, errors.New("tool_manifest_invalid")
		}
		expect(t, runCheck(t, env, "subtitle_ocr"), StatusFail, CodeToolManifest)
	})
	t.Run("embedded manifest and project install", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.OCRSpec = nil
		env.Project = t.TempDir()
		r := runCheck(t, env, "subtitle_ocr")
		if r.Status == StatusFail {
			t.Fatalf("an absent optional runtime failed doctor: %+v", r)
		}
	})
}

// fakeMatroska points every optional tool at one verified stand-in file.
func fakeMatroska(path string) func(string) (tools.MatroskaToolSpecification, error) {
	sum := sha256.Sum256(fakeToolBytes)
	return func(name string) (tools.MatroskaToolSpecification, error) {
		return tools.MatroskaToolSpecification{Name: name, Version: "1-test", Executable: tools.RuntimeFile{Path: ".tools/none/" + name, ContainerPath: path, SHA256: hex.EncodeToString(sum[:])}}, nil
	}
}

func TestMatroskaToolFaults(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		env, _ := healthyEnv(t)
		r := runCheck(t, env, "matroska_tools")
		expect(t, r, StatusOK, CodeMatroskaVerified)
		if len(r.Findings) != 3 || r.Facts["mkvextract.version"] != "1-test" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("missing is optional unless extraction is enabled", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.MatroskaSpec = fakeMatroska(filepath.Join(t.TempDir(), "none"))
		expect(t, runCheck(t, env, "matroska_tools"), StatusWarn, CodeMatroskaMissing)
		env.Config.Matroska.EnableExtraction = true
		r := runCheck(t, env, "matroska_tools")
		expect(t, r, StatusFail, CodeMatroskaMissing)
		for _, f := range r.Findings {
			if (f.Subject == "mediainfo") != (f.Status == StatusWarn) {
				t.Fatalf("finding %+v", f)
			}
		}
	})
	t.Run("hash mismatch and unreadable", func(t *testing.T) {
		env, _ := healthyEnv(t)
		bad := filepath.Join(t.TempDir(), "tampered")
		if err := os.WriteFile(bad, []byte("tampered"), 0o700); err != nil {
			t.Fatal(err)
		}
		env.MatroskaSpec = fakeMatroska(bad)
		r := runCheck(t, env, "matroska_tools")
		expect(t, r, StatusFail, CodeMatroskaMismatch)
		if r.Findings[0].Subject != "mkvmerge:runtime" {
			t.Fatalf("subject = %q", r.Findings[0].Subject)
		}
		env.MatroskaSpec = fakeMatroska(t.TempDir())
		expect(t, runCheck(t, env, "matroska_tools"), StatusFail, CodeMatroskaUnreadable)
	})
	t.Run("unsupported platform and broken manifest", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.MatroskaSpec = func(string) (tools.MatroskaToolSpecification, error) {
			return tools.MatroskaToolSpecification{}, errors.New("tool_platform_unsupported")
		}
		expect(t, runCheck(t, env, "matroska_tools"), StatusWarn, CodeMatroskaUnsupported)
		env.MatroskaSpec = func(string) (tools.MatroskaToolSpecification, error) {
			return tools.MatroskaToolSpecification{}, errors.New("tool_manifest_invalid")
		}
		expect(t, runCheck(t, env, "matroska_tools"), StatusFail, CodeToolManifest)
	})
	t.Run("embedded manifest", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.MatroskaSpec = nil
		env.Project = t.TempDir()
		r := runCheck(t, env, "matroska_tools")
		if r.Status == StatusFail {
			t.Fatalf("an absent optional tool failed doctor: %+v", r)
		}
	})
}

func runCheck(t *testing.T, env Environment, name string) Result {
	t.Helper()
	s := NewSession(env)
	defer s.Close()
	for _, c := range s.Checks() {
		if c.Name() == name {
			return runOne(context.Background(), c)
		}
	}
	t.Fatalf("no check %s", name)
	return Result{}
}

// expect asserts the result's overall status and code, and that non-ok
// results carry a remediation.
func expect(t *testing.T, r Result, status Status, code string) {
	t.Helper()
	if r.Status != status || r.Code != code {
		t.Fatalf("%s: got %s/%s want %s/%s (%+v)", r.Check, r.Status, r.Code, status, code, r.Findings)
	}
	if status != StatusOK && r.Fix == "" {
		t.Fatalf("%s: %s has no fix", r.Check, code)
	}
}

func hasFinding(r Result, status Status, code string) bool {
	for _, f := range r.Findings {
		if f.Status == status && f.Code == code {
			return true
		}
	}
	return false
}

func TestHealthyEnvironmentPasses(t *testing.T) {
	env, _ := healthyEnv(t)
	report := NewSession(env).Doctor(context.Background())
	if report.Status != StatusOK || report.Summary.Fail != 0 || report.Summary.Warn != 0 {
		data, _ := MarshalReport(report)
		t.Fatalf("healthy environment not ok:\n%s", data)
	}
}

func TestConfigFaults(t *testing.T) {
	t.Run("missing database", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.DatabaseURL = ""
		env.ConfigErr = errors.New("JELEE_DATABASE_URL must identify a PostgreSQL database")
		r := runCheck(t, env, "config")
		expect(t, r, StatusFail, CodeConfigDatabaseMissing)
		expect(t, runCheck(t, env, "database"), StatusFail, CodeDBNotConfigured)
	})
	t.Run("invalid setting keeps safe detail only", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.ConfigErr = errors.New("invalid JELEE_MAX_STREAMS")
		r := runCheck(t, env, "config")
		expect(t, r, StatusFail, CodeConfigInvalid)
		if r.Findings[0].Detail != "invalid JELEE_MAX_STREAMS" {
			t.Fatalf("detail = %q", r.Findings[0].Detail)
		}
		env.ConfigErr = errors.New("cannot read /etc/jelee/secret.json")
		if d := runCheck(t, env, "config").Findings[0].Detail; d != "" {
			t.Fatalf("unsafe detail kept: %q", d)
		}
	})
	t.Run("credential file readable by others", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits are not used on Windows")
		}
		env, _ := healthyEnv(t)
		path := filepath.Join(t.TempDir(), "db-url")
		if err := os.WriteFile(path, []byte(testDSN), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		env.Lookup = lookupMap(map[string]string{"JELEE_DATABASE_URL_FILE": path})
		r := runCheck(t, env, "config")
		expect(t, r, StatusWarn, CodeConfigSecretFileMode)
		if r.Findings[1].Subject != "JELEE_DATABASE_URL_FILE" {
			t.Fatalf("subject = %q", r.Findings[1].Subject)
		}
	})
}

func TestDatabaseFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"unreachable", errors.New("dial tcp: refused"), CodeDBUnreachable},
		{"credentials", ErrDBAuth, CodeDBAuthFailed},
		{"bad dsn", ErrDBConfig, CodeDBConfigInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := healthyEnv(t)
			env.OpenDB = func(context.Context, string) (Database, error) { return nil, tc.err }
			expect(t, runCheck(t, env, "database"), StatusFail, tc.code)
			expect(t, runCheck(t, env, "migrations"), StatusWarn, CodeDBUnchecked)
			expect(t, runCheck(t, env, "library_roots"), StatusWarn, CodeRootsUnchecked)
		})
	}
	t.Run("ping lost after open", func(t *testing.T) {
		env, db := healthyEnv(t)
		db.pingErr = errors.New("gone")
		expect(t, runCheck(t, env, "database"), StatusFail, CodeDBUnreachable)
	})
}

func TestMigrationFaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fakeDB)
		status Status
		code   string
	}{
		{"current", func(*fakeDB) {}, StatusOK, CodeSchemaCurrent},
		{"dirty", func(d *fakeDB) { d.dirty = true }, StatusFail, CodeMigrationDirty},
		{"behind", func(d *fakeDB) { d.version = testSchema - 1 }, StatusFail, CodeMigrationBehind},
		{"newer", func(d *fakeDB) { d.version = testSchema + 1 }, StatusFail, CodeSchemaNewer},
		{"never migrated", func(d *fakeDB) { d.missing = true }, StatusFail, CodeSchemaMissing},
		{"query failed", func(d *fakeDB) { d.migErr = errors.New("permission denied") }, StatusFail, CodeDBQueryFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, db := healthyEnv(t)
			tc.mutate(db)
			r := runCheck(t, env, "migrations")
			expect(t, r, tc.status, tc.code)
			if r.Facts["required"] != "60" {
				t.Fatalf("facts = %v", r.Facts)
			}
		})
	}
}

func TestLibraryRootFaults(t *testing.T) {
	good := t.TempDir()
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("missing and not a directory", func(t *testing.T) {
		env, db := healthyEnv(t)
		db.roots = []Root{{ID: "r1", Path: good}, {ID: "r2", Path: filepath.Join(good, "gone")}, {ID: "r3", Path: file}}
		r := runCheck(t, env, "library_roots")
		expect(t, r, StatusFail, CodeRootMissing)
		if !hasFinding(r, StatusOK, CodeRootOK) || !hasFinding(r, StatusFail, CodeRootNotDirectory) {
			t.Fatalf("findings = %+v", r.Findings)
		}
		if r.Findings[1].Subject != "root:r2" {
			t.Fatalf("subject = %q", r.Findings[1].Subject)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs POSIX permissions enforced for a non-root user")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		env, db := healthyEnv(t)
		db.roots = []Root{{ID: "r1", Path: dir}}
		expect(t, runCheck(t, env, "library_roots"), StatusFail, CodeRootUnreadable)
	})
	t.Run("bounded count", func(t *testing.T) {
		env, db := healthyEnv(t)
		env.MaxRoots = 2
		db.roots = []Root{{ID: "a", Path: good}, {ID: "b", Path: good}, {ID: "c", Path: filepath.Join(good, "never-checked")}}
		r := runCheck(t, env, "library_roots")
		expect(t, r, StatusWarn, CodeRootsTruncated)
		if len(r.Findings) != 3 || r.Facts["checked"] != "2" {
			t.Fatalf("findings = %+v facts = %v", r.Findings, r.Facts)
		}
	})
	t.Run("none registered", func(t *testing.T) {
		env, _ := healthyEnv(t)
		expect(t, runCheck(t, env, "library_roots"), StatusOK, CodeRootsNone)
	})
	t.Run("query failure", func(t *testing.T) {
		env, db := healthyEnv(t)
		db.rootsErr = errors.New("boom")
		expect(t, runCheck(t, env, "library_roots"), StatusFail, CodeDBQueryFailed)
	})
}

func TestToolFaults(t *testing.T) {
	t.Run("hash mismatch", func(t *testing.T) {
		env, _ := healthyEnv(t)
		bad := filepath.Join(t.TempDir(), "ffprobe")
		if err := os.WriteFile(bad, []byte("tampered"), 0o700); err != nil {
			t.Fatal(err)
		}
		env.Tools = []ToolCandidate{{Label: "runtime", Path: bad}}
		r := runCheck(t, env, "tools")
		expect(t, r, StatusFail, CodeToolHashMismatch)
		if r.Findings[0].Subject != "ffprobe:runtime" {
			t.Fatalf("subject = %q", r.Findings[0].Subject)
		}
	})
	t.Run("verified", func(t *testing.T) {
		env, _ := healthyEnv(t)
		r := runCheck(t, env, "tools")
		expect(t, r, StatusOK, CodeToolVerified)
		if r.Facts["expectedVersion"] != "7.1-test" {
			t.Fatalf("facts = %v", r.Facts)
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Tools = []ToolCandidate{{Label: "runtime", Path: t.TempDir()}}
		expect(t, runCheck(t, env, "tools"), StatusFail, CodeToolUnreadable)
	})
	t.Run("missing optional and required", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Tools = []ToolCandidate{{Label: "runtime", Path: filepath.Join(t.TempDir(), "none")}}
		expect(t, runCheck(t, env, "tools"), StatusWarn, CodeToolMissing)
		env.Config.EnableProbe = true
		expect(t, runCheck(t, env, "tools"), StatusFail, CodeToolMissing)
	})
	t.Run("embedded covers need a verified ffprobe only", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.EnableEmbeddedCovers = true
		r := runCheck(t, env, "tools")
		if !hasFinding(r, StatusOK, CodeEmbeddedCoversReady) {
			t.Fatalf("ready cover pass not reported: %+v", r.Findings)
		}
		env.Tools = []ToolCandidate{{Label: "runtime", Path: filepath.Join(t.TempDir(), "none")}}
		r = runCheck(t, env, "tools")
		if r.Status != StatusFail || !hasFinding(r, StatusFail, CodeEmbeddedCoversNoTool) {
			t.Fatalf("missing tool not reported for enabled cover pass: %+v", r.Findings)
		}
	})
	t.Run("unsupported platform and broken manifest", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.ToolSpec = func() (tools.FFprobeSpecification, error) {
			return tools.FFprobeSpecification{}, errors.New("tool_platform_unsupported")
		}
		expect(t, runCheck(t, env, "tools"), StatusWarn, CodeToolUnsupported)
		env.ToolSpec = func() (tools.FFprobeSpecification, error) {
			return tools.FFprobeSpecification{}, errors.New("tool_manifest_invalid")
		}
		expect(t, runCheck(t, env, "tools"), StatusFail, CodeToolManifest)
	})
}

func TestDiskFaults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		usage  DiskUsage
		err    error
		status Status
		code   string
	}{
		{"low space", DiskUsage{TotalBytes: 100 << 30, FreeBytes: 1 << 30, TotalInodes: 100, FreeInodes: 90, InodesKnown: true}, nil, StatusWarn, CodeDiskSpaceLow},
		{"no space", DiskUsage{TotalBytes: 100 << 30, FreeBytes: 10 << 20, TotalInodes: 100, FreeInodes: 90, InodesKnown: true}, nil, StatusFail, CodeDiskSpaceCritical},
		{"few inodes", DiskUsage{TotalBytes: 100 << 30, FreeBytes: 50 << 30, TotalInodes: 1000, FreeInodes: 30, InodesKnown: true}, nil, StatusWarn, CodeDiskInodesLow},
		{"no inodes", DiskUsage{TotalBytes: 100 << 30, FreeBytes: 50 << 30, TotalInodes: 1000, FreeInodes: 5, InodesKnown: true}, nil, StatusFail, CodeDiskInodesCrit},
		{"stat failed", DiskUsage{}, errDiskUnavailable, StatusWarn, CodeDiskUnavailable},
		{"no inode concept", DiskUsage{TotalBytes: 100 << 30, FreeBytes: 50 << 30}, nil, StatusOK, CodeDiskNoInodes},
		{"small tmpfs mostly free", DiskUsage{TotalBytes: 64 << 20, FreeBytes: 60 << 20, TotalInodes: 1000, FreeInodes: 990, InodesKnown: true}, nil, StatusOK, CodeDiskOK},
		{"small tmpfs full", DiskUsage{TotalBytes: 64 << 20, FreeBytes: 1 << 20, TotalInodes: 1000, FreeInodes: 990, InodesKnown: true}, nil, StatusFail, CodeDiskSpaceCritical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := healthyEnv(t)
			env.Statfs = func(string) (DiskUsage, error) { return tc.usage, tc.err }
			r := runCheck(t, env, "disk")
			expect(t, r, tc.status, tc.code)
			if tc.code != CodeDiskOK && r.Findings[0].Subject != "tempdir" {
				t.Fatalf("subject = %q", r.Findings[0].Subject)
			}
		})
	}
	t.Run("real statfs", func(t *testing.T) {
		usage, err := statfs(t.TempDir())
		if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
			if err == nil {
				t.Fatal("unsupported platform reported usage")
			}
			return
		}
		if err != nil || usage.TotalBytes == 0 || (runtime.GOOS == "linux") != usage.InodesKnown {
			t.Fatalf("statfs = %+v err=%v", usage, err)
		}
	})
}

func TestNetworkFaults(t *testing.T) {
	for _, tc := range []struct {
		name, listen string
		proxies      []string
		status       Status
		code         string
	}{
		{"hostname listen", "localhost:8097", nil, StatusFail, CodeNetListen},
		{"bad port", "127.0.0.1:0", nil, StatusFail, CodeNetListen},
		{"invalid proxy", "127.0.0.1:8097", []string{"proxy.example"}, StatusFail, CodeNetProxyInvalid},
		{"everyone trusted", "127.0.0.1:8097", []string{"0.0.0.0/0"}, StatusFail, CodeNetProxyBroad},
		{"broad v6", "127.0.0.1:8097", []string{"::/8"}, StatusFail, CodeNetProxyBroad},
		{"narrow proxy", "127.0.0.1:8097", []string{"10.0.0.0/8", "::1/128"}, StatusOK, CodeNetOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := healthyEnv(t)
			env.Config.Listen, env.Config.TrustedProxies = tc.listen, tc.proxies
			expect(t, runCheck(t, env, "network"), tc.status, tc.code)
		})
	}
}

func TestDirectoryFaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not used on Windows")
	}
	private := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("too broad", func(t *testing.T) {
		env, _ := healthyEnv(t)
		dir := private(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		env.Config.Images.TempRoot = dir
		r := runCheck(t, env, "directories")
		expect(t, r, StatusFail, CodeDirPermissive)
		if r.Findings[1].Subject != "images.tempRoot" {
			t.Fatalf("subject = %q", r.Findings[1].Subject)
		}
	})
	t.Run("missing", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Images.StoreRoot = filepath.Join(private(t), "absent")
		expect(t, runCheck(t, env, "directories"), StatusFail, CodeDirMissing)
	})
	t.Run("images enabled without temp root", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.EnableImages = true
		expect(t, runCheck(t, env, "directories"), StatusFail, CodeDirMissing)
	})
	t.Run("not a directory", func(t *testing.T) {
		env, _ := healthyEnv(t)
		file := filepath.Join(private(t), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		env.Config.Images.TempRoot = file
		expect(t, runCheck(t, env, "directories"), StatusFail, CodeDirNotDirectory)
	})
	t.Run("symlink", func(t *testing.T) {
		env, _ := healthyEnv(t)
		link := filepath.Join(private(t), "link")
		if err := os.Symlink(private(t), link); err != nil {
			t.Fatal(err)
		}
		env.Config.Images.TempRoot = link
		expect(t, runCheck(t, env, "directories"), StatusFail, CodeDirSymlink)
	})
	t.Run("read only", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses write permission")
		}
		env, _ := healthyEnv(t)
		dir := private(t)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		env.Config.Images.TempRoot = dir
		expect(t, runCheck(t, env, "directories"), StatusFail, CodeDirNotWritable)
	})
	t.Run("shared temp without sticky bit", func(t *testing.T) {
		env, _ := healthyEnv(t)
		dir := private(t)
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		env.TempDir = dir
		expect(t, runCheck(t, env, "directories"), StatusWarn, CodeDirNotSticky)
		if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
			t.Fatal(err)
		}
		expect(t, runCheck(t, env, "directories"), StatusOK, CodeDirOK)
	})
	t.Run("log file readable by others", func(t *testing.T) {
		env, _ := healthyEnv(t)
		dir := private(t)
		path := filepath.Join(dir, "jelee.log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		env.Config.Logging.Output, env.Config.Logging.File.Path = "file", path
		expect(t, runCheck(t, env, "directories"), StatusWarn, CodeLogFileMode)
	})
	t.Run("private directories pass", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Images.TempRoot, env.Config.Images.StoreRoot = private(t), private(t)
		expect(t, runCheck(t, env, "directories"), StatusOK, CodeDirOK)
	})
}

func TestPrivacyAndDevMode(t *testing.T) {
	t.Run("public listen", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Listen = "203.0.113.7:8097"
		r := runCheck(t, env, "privacy")
		expect(t, r, StatusWarn, CodePrivacyPublic)
		if r.Facts["listenScope"] != "public" {
			t.Fatalf("facts = %v", r.Facts)
		}
	})
	t.Run("all interfaces", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Listen = "0.0.0.0:8097"
		expect(t, runCheck(t, env, "privacy"), StatusWarn, CodePrivacyExposed)
	})
	t.Run("debug logging", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Logging.Components = map[string]string{"scan": "debug"}
		expect(t, runCheck(t, env, "privacy"), StatusWarn, CodePrivacyDebugLog)
	})
	t.Run("switch states reported", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Config.Logging.IPMode, env.Config.Logging.PathMode, env.Config.TMDBAPIKey = "mask", "relative", testTMDBKey
		r := runCheck(t, env, "privacy")
		expect(t, r, StatusOK, CodePrivacyLoopback)
		for _, code := range []string{CodePrivacyIPMask, CodePrivacyRelPath, CodePrivacyTMDB} {
			if !hasFinding(r, StatusOK, code) {
				t.Fatalf("missing %s in %+v", code, r.Findings)
			}
		}
	})
	t.Run("developer mode requested", func(t *testing.T) {
		env, _ := healthyEnv(t)
		env.Lookup = lookupMap(map[string]string{"JELEE_DEV_MODE": "yes"})
		expect(t, runCheck(t, env, "devmode"), StatusFail, CodeDevEnvInvalid)
		env.Lookup = lookupMap(map[string]string{"JELEE_DEV_MODE": "true"})
		expect(t, runCheck(t, env, "devmode"), StatusWarn, CodeDevEnvSet)
		env.Config.Dev.Enabled = true
		r := runCheck(t, env, "devmode")
		expect(t, r, StatusWarn, CodeDevCapable)
		if r.Facts["devMode"] != "capable" {
			t.Fatalf("result = %+v", r)
		}
		env.Lookup = lookupMap(map[string]string{"JELEE_DEV_MODE": "true", "JELEE_ENV": "production"})
		r = runCheck(t, env, "devmode")
		expect(t, r, StatusWarn, CodeDevProductionIgnored)
		if !hasFinding(r, StatusOK, CodeDevProduction) || r.Facts["devMode"] != "off" {
			t.Fatalf("result = %+v", r)
		}
		env.Config.Dev.Enabled = false
		env.Lookup = lookupMap(map[string]string{"JELEE_DEV_MODE": "false", "JELEE_ENV": "production"})
		r = runCheck(t, env, "devmode")
		expect(t, r, StatusOK, CodeDevProduction)
		if hasFinding(r, StatusWarn, CodeDevProductionIgnored) || r.Facts["devMode"] != "off" {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestExternalCheckIsOptIn(t *testing.T) {
	env, _ := healthyEnv(t)
	env.Config.TMDBAPIKey = testTMDBKey
	called := 0
	env.TMDB = func(context.Context) ExternalOutcome { called++; return ExternalCredentials }
	for _, c := range NewSession(env).Checks() {
		if c.Name() == "external" {
			t.Fatal("external check runs without --external")
		}
	}
	env.External = true
	for _, tc := range []struct {
		outcome ExternalOutcome
		status  Status
		code    string
	}{
		{ExternalOK, StatusOK, CodeExternalOK},
		{ExternalCredentials, StatusFail, CodeExternalCredentials},
		{ExternalRateLimited, StatusWarn, CodeExternalRateLimited},
		{ExternalUnreachable, StatusFail, CodeExternalUnreachable},
	} {
		env.TMDB = func(context.Context) ExternalOutcome { called++; return tc.outcome }
		expect(t, runCheck(t, env, "external"), tc.status, tc.code)
	}
	env.Config.TMDBAPIKey = ""
	expect(t, runCheck(t, env, "external"), StatusOK, CodeExternalNotConfigured)
	if called != 4 {
		t.Fatalf("probe called %d times", called)
	}
}

func TestRunnerContainsPanicsAndTimeouts(t *testing.T) {
	old := checkTimeout
	checkTimeout = 50 * time.Millisecond
	t.Cleanup(func() { checkTimeout = old })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	report := Run(context.Background(), []Check{
		CheckFunc{"boom", func(context.Context) Result { panic("x") }},
		CheckFunc{"hang", func(context.Context) Result { <-release; return Result{} }},
		CheckFunc{"fine", func(context.Context) Result { return newResult("fine") }},
	}, nil)
	if report.Results[0].Code != CodeCheckPanicked || report.Results[1].Code != CodeCheckTimeout || report.Results[2].Status != StatusOK {
		t.Fatalf("report = %+v", report.Results)
	}
	if report.Status != StatusFail || report.Summary != (Summary{OK: 1, Fail: 2}) {
		t.Fatalf("summary = %+v %s", report.Summary, report.Status)
	}
}

func TestReportJSONShape(t *testing.T) {
	env, db := healthyEnv(t)
	db.dirty = true
	data, err := MarshalReport(NewSession(env).Doctor(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Format      int       `json:"format"`
		GeneratedAt time.Time `json:"generatedAt"`
		Status      string    `json:"status"`
		Summary     Summary   `json:"summary"`
		Results     []struct {
			Check, Status, Code, Fix string
			Findings                 []map[string]string
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Format != ReportFormat || decoded.Status != "fail" || decoded.Summary.Fail != 1 || len(decoded.Results) != 12 || decoded.GeneratedAt.IsZero() {
		t.Fatalf("decoded = %+v", decoded)
	}
	names := []string{"config", "database", "migrations", "library_roots", "tools", "matroska_tools", "subtitle_ocr", "disk", "network", "directories", "privacy", "devmode"}
	for i, r := range decoded.Results {
		if r.Check != names[i] || len(r.Findings) == 0 || r.Status == "" || r.Code == "" {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
	if decoded.Results[2].Code != CodeMigrationDirty || decoded.Results[2].Fix == "" {
		t.Fatalf("migration result = %+v", decoded.Results[2])
	}
}

// TestDoctorOutputHasNoSecrets injects credentials and absolute paths into
// every configuration channel and fails every check that touches them.
func TestDoctorOutputHasNoSecrets(t *testing.T) {
	env, db := healthyEnv(t)
	secretDir := filepath.Join(t.TempDir(), "very-private-media")
	env.Config.TMDBAPIKey = testTMDBKey
	env.Config.Images.TempRoot = filepath.Join(secretDir, "img-temp")
	env.Config.Images.StoreRoot = filepath.Join(secretDir, "img-store")
	env.Config.Logging.Output, env.Config.Logging.File.Path = "file", filepath.Join(secretDir, "logs", "jelee.log")
	env.ConfigErr = errors.New("cannot read " + testDSN)
	db.roots = []Root{{ID: "r1", Path: filepath.Join(secretDir, "movies")}}
	env.Tools = []ToolCandidate{{Label: "runtime", Path: filepath.Join(secretDir, "ffprobe")}}
	report := NewSession(env).Doctor(context.Background())
	data, err := MarshalReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var table strings.Builder
	if err := RenderTable(&table, report); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{string(data), table.String()} {
		for _, needle := range []string{testPassword, testDBHost, "postgres://", testTMDBKey, secretDir, "very-private-media"} {
			if strings.Contains(out, needle) {
				t.Fatalf("doctor output leaked %q", needle)
			}
		}
		if !NewScanner(env.Config, env.Lookup).Clean([]byte(out)) {
			t.Fatal("doctor output failed the sensitive scan")
		}
	}
}

func TestEveryCodeHasMessageAndIsDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "troubleshooting.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range Codes() {
		if codes[code].Message == "" {
			t.Errorf("%s has no message", code)
		}
		if !strings.Contains(string(doc), "`"+code+"`") {
			t.Errorf("%s is not documented in docs/troubleshooting.md", code)
		}
	}
}
