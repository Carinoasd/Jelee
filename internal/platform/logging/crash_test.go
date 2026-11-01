package logging

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// crashNeedles are values a crash report must never contain: the panic
// value, argument words, absolute directories and the scan samples.
func crashNeedles(t *testing.T, extra ...string) []string {
	t.Helper()
	wd, _ := os.Getwd()
	needles := append([]string{wd, "0x", "goroutine "}, extra...)
	for _, s := range sensitiveSamples {
		needles = append(needles, s.needles...)
	}
	return append(needles, defaultOnlyNeedles...)
}

func assertClean(t *testing.T, what, text string, needles []string) {
	t.Helper()
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			t.Fatalf("%s leaked %q:\n%s", what, needle, text)
		}
	}
}

func readReport(t *testing.T, a *CrashArchive, name string) string {
	t.Helper()
	path := filepath.Join(a.Dir(), name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func secretPanicValue() string {
	values := make([]string, 0, len(sensitiveSamples))
	for _, s := range sensitiveSamples {
		values = append(values, s.value)
	}
	return "secret-panic-value " + strings.Join(values, " ")
}

func TestOpenCrashArchiveOptions(t *testing.T) {
	if a, err := OpenCrashArchive(CrashOptions{}); a != nil || err != nil {
		t.Fatal("empty directory must disable the archive")
	}
	for _, opts := range []CrashOptions{{Dir: "relative/crash"}, {Dir: t.TempDir(), Keep: -1}, {Dir: t.TempDir(), Keep: MaxCrashKeep + 1}} {
		if _, err := OpenCrashArchive(opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	dir := filepath.Join(t.TempDir(), "logs", "crash")
	a, err := OpenCrashArchive(CrashOptions{Dir: dir})
	if err != nil || a.keep != DefaultCrashKeep || a.Dir() != dir {
		t.Fatalf("default archive: %+v %v", a, err)
	}
	if info, err := os.Stat(dir); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("archive directory: %v %v", info, err)
	}
	// A nil archive is a no-op everywhere.
	var none *CrashArchive
	if name, err := none.Record("process_panic", "x", nil); name != "" || err != nil || none.Arm() != nil || none.Collect(nil) != 0 || none.Dir() != "" {
		t.Fatal("nil archive acted")
	}
	none.Disarm()
}

// Reports are masked, 0600, named by time, and only the newest Keep are
// kept; other files in the directory are left alone.
func TestCrashArchiveRecordMasksAndRotates(t *testing.T) {
	a, err := OpenCrashArchive(CrashOptions{Dir: t.TempDir(), Keep: 3})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { at = at.Add(time.Second); return at }
	if err := os.WriteFile(filepath.Join(a.Dir(), "operator-notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stack []byte
	func() {
		defer func() {
			if recover() != nil {
				stack = debug.Stack()
			}
		}()
		panicWith(secretPanicValue())
	}()
	var names []string
	for range 5 {
		name, err := a.Record(crashSourceProcess, "*errors.errorString", stack)
		if err != nil || !validCrashName(name) {
			t.Fatalf("record %q: %v", name, err)
		}
		names = append(names, name)
	}
	reports, err := a.Reports()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reports, ",") != strings.Join(names[2:], ",") {
		t.Fatalf("kept %v, want the newest of %v", reports, names)
	}
	if _, err := os.Stat(filepath.Join(a.Dir(), "operator-notes.txt")); err != nil {
		t.Fatal("pruning removed a foreign file")
	}
	report := readReport(t, a, reports[2])
	for _, want := range []string{"Jelee crash report", "source: process_panic", "type: *errors.errorString", "logging.panicWith(...)", "internal/platform/logging/crash_test.go:"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	assertClean(t, "report", report, crashNeedles(t, "secret-panic-value"))
	// A type name that is not one is not written.
	name, err := a.Record(crashSourceProcess, "jk_live 8f3a9c2e", stack)
	if err != nil || !strings.Contains(readReport(t, a, name), "type: unknown") {
		t.Fatal("unsafe type written")
	}
}

// CapturePanic logs and archives a panic of the calling goroutine, closes
// the router and panics again with the same value.
func TestCapturePanicLogsArchivesAndRepanics(t *testing.T) {
	a, err := OpenCrashArchive(CrashOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Arm(); err != nil {
		t.Fatal(err)
	}
	r, out := openTest(t, Options{})
	value := errors.New(secretPanicValue())
	var again any
	func() {
		defer func() { again = recover() }()
		func() {
			defer CapturePanic(r, a)
			panic(value)
		}()
	}()
	if again != value {
		t.Fatalf("re-raised %v", again)
	}
	recs := records(t, out.String())
	if len(recs) != 1 {
		t.Fatalf("records %v", recs)
	}
	rec := recs[0]
	name, _ := rec["crashFile"].(string)
	if rec["msg"] != "process panic" || rec["level"] != "ERROR" || rec["component"] != "system" || rec["code"] != "process_panic" ||
		rec["panicType"] != "*errors.errorString" || !validCrashName(name) || !strings.Contains(rec["stack"].(string), "crash_test.go:") {
		t.Fatalf("panic record %v", rec)
	}
	assertClean(t, "log", out.String(), crashNeedles(t, "secret-panic-value"))
	assertClean(t, "report", readReport(t, a, name), crashNeedles(t, "secret-panic-value"))
	// The crash output was disarmed: only the report remains.
	entries, _ := os.ReadDir(a.Dir())
	if len(entries) != 1 || a.pending != "" {
		t.Fatalf("directory after capture: %v", entries)
	}
	// Without a panic CapturePanic does nothing.
	func() { defer CapturePanic(r, a) }()
}

// The HTTP boundary archives at most one request panic per second, and
// nothing without an installed archive.
func TestArchivePanicIsInstalledAndRateLimited(t *testing.T) {
	if ArchivePanic("x", nil) != "" {
		t.Fatal("archived without an archive")
	}
	a, err := OpenCrashArchive(CrashOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	SetCrashArchive(a)
	t.Cleanup(func() { SetCrashArchive(nil) })
	stack := []byte("main.handler(0xc000012345)\n\t/home/someone/jelee/internal/adapter/http/server.go:42 +0x1d\n")
	first := ArchivePanic(errors.New("boom"), stack)
	if !validCrashName(first) {
		t.Fatalf("first archive %q", first)
	}
	if second := ArchivePanic(errors.New("boom"), stack); second != "" {
		t.Fatalf("second archive within a second: %q", second)
	}
	now = now.Add(crashArchiveInterval)
	if third := ArchivePanic(errors.New("boom"), stack); !validCrashName(third) || third == first {
		t.Fatalf("third archive %q", third)
	}
	report := readReport(t, a, first)
	if !strings.Contains(report, "source: request_panic") || !strings.Contains(report, "internal/adapter/http/server.go:42") {
		t.Fatalf("request report:\n%s", report)
	}
	assertClean(t, "report", report, []string{"/home/someone", "0xc000012345", "boom"})
}

// Collect turns a crashed process's pending file into a report and a log
// record; empty pending files of live processes stay, old ones go.
func TestCollectPendingCrashOutput(t *testing.T) {
	a, err := OpenCrashArchive(CrashOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	raw := "panic: " + secretPanicValue() + "\n\ngoroutine 7 [running]:\ngithub.com/MoYuanCN/Jelee/internal/app.(*Jobs).run(0xc0000a2000, {0x1, 0x2})\n\t/home/someone/jelee/internal/app/jobs.go:120 +0x45\ncreated by github.com/MoYuanCN/Jelee/internal/app.Start in goroutine 1\n\t/home/someone/jelee/internal/app/jobs.go:80 +0x99\n"
	write := func(name, text string, age time.Duration) {
		path := filepath.Join(a.Dir(), name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("pending-20261101T110000.000000000Z-41.raw", raw, time.Hour)
	write("pending-20261101T100000.000000000Z-42.raw", "", time.Minute)
	write("pending-20261020T100000.000000000Z-43.raw", "", 48*time.Hour)
	r, out := openTest(t, Options{})
	if n := a.Collect(r.Logger()); n != 1 {
		t.Fatalf("collected %d", n)
	}
	reports, _ := a.Reports()
	if len(reports) != 1 {
		t.Fatalf("reports %v", reports)
	}
	report := readReport(t, a, reports[0])
	for _, want := range []string{"source: crash_output", "type: panic", "app.(*Jobs).run(...)", "internal/app/jobs.go:120"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	needles := crashNeedles(t, "secret-panic-value", "/home/someone")
	assertClean(t, "report", report, needles)
	for name, want := range map[string]bool{"pending-20261101T110000.000000000Z-41.raw": false, "pending-20261101T100000.000000000Z-42.raw": true, "pending-20261020T100000.000000000Z-43.raw": false} {
		if _, err := os.Stat(filepath.Join(a.Dir(), name)); (err == nil) != want {
			t.Fatalf("%s present=%v, want %v", name, err == nil, want)
		}
	}
	text := flushed(t, r, out)
	recs := records(t, text)
	if len(recs) != 1 || recs[0]["msg"] != "previous process crashed" || recs[0]["code"] != "process_crashed" || recs[0]["panicType"] != "panic" || recs[0]["crashFile"] != reports[0] {
		t.Fatalf("collect record %v", recs)
	}
	assertClean(t, "log", text, needles)
	if crashKind([]byte("fatal error: concurrent map writes\n")) != "fatal_error" || crashKind([]byte("unexpected signal\n")) != "crash" {
		t.Fatal("crash kinds")
	}
}

// crashHelperEnv names the archive directory of the crashing child.
const crashHelperEnv = "JELEE_TEST_CRASH_HELPER_DIR"

// TestCrashHelperProcess is the child of TestUnrecoveredGoroutinePanicIsArchived:
// it arms the archive and panics in a goroutine nothing recovers.
func TestCrashHelperProcess(t *testing.T) {
	dir := os.Getenv(crashHelperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	a, err := OpenCrashArchive(CrashOptions{Dir: dir})
	if err != nil {
		os.Exit(3)
	}
	if err := a.Arm(); err != nil {
		os.Exit(4)
	}
	go func() { panicWith(secretPanicValue()) }()
	time.Sleep(10 * time.Second)
	os.Exit(5)
}

// An unrecovered panic in another goroutine still ends the process as Go
// does (exit status 2, dump on stderr); the runtime's crash output reaches
// the pending file and the next start archives and logs it masked.
func TestUnrecoveredGoroutinePanicIsArchived(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a child process")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+dir)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(output), "secret-panic-value") {
		t.Fatalf("child did not crash like Go: %v\n%s", err, output)
	}
	a, err := OpenCrashArchive(CrashOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	r, out := openTest(t, Options{})
	if n := a.Collect(r.Logger()); n != 1 {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("collected %d from %v", n, entries)
	}
	reports, _ := a.Reports()
	report := readReport(t, a, reports[0])
	if !strings.Contains(report, "type: panic") || !strings.Contains(report, "logging.panicWith(...)") {
		t.Fatalf("report:\n%s", report)
	}
	assertClean(t, "report", report, crashNeedles(t, "secret-panic-value"))
	assertClean(t, "log", flushed(t, r, out), crashNeedles(t, "secret-panic-value"))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), crashPendingPrefix) {
			t.Fatalf("pending file left: %s", e.Name())
		}
	}
}

// Arm leaves one empty pending file for this process; Disarm removes it.
func TestArmAndDisarm(t *testing.T) {
	a, err := OpenCrashArchive(CrashOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Arm(); err != nil {
		t.Fatal(err)
	}
	if err := a.Arm(); err != nil {
		t.Fatal("second arm")
	}
	pending := filepath.Join(a.Dir(), a.pending)
	info, err := os.Stat(pending)
	if err != nil || info.Size() != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("pending file: %v %v", info, err)
	}
	// Our own pending file is never collected.
	if a.Collect(nil) != 0 {
		t.Fatal("collected own pending file")
	}
	a.Disarm()
	if _, err := os.Stat(pending); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("pending file kept")
	}
	a.Disarm()
}
