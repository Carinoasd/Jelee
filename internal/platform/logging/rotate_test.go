package logging

import (
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		r = gz
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits (Go reports 0666/0777 from the
	// read-only attribute); privacy there comes from the directory's ACL,
	// tracked as an owner verification item rather than asserted here.
	if runtime.GOOS != "windows" && info.Mode().Perm() != want {
		t.Fatalf("%s mode=%v want=%v", filepath.Base(path), info.Mode().Perm(), want)
	}
}

func TestSizeRotationBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jelee.log")
	f, err := openRotatingFile(RotateOptions{Path: path, MaxBytes: 10, MaxBackups: 5}, newClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write([]byte("12345"))
	f.Write([]byte("67890")) // exactly MaxBytes: no rotation
	if backups, _ := f.Backups(); len(backups) != 0 {
		t.Fatalf("rotated at the limit: %v", backups)
	}
	f.Write([]byte("X")) // would exceed: rotate first
	backups, _ := f.Backups()
	if len(backups) != 1 || readLog(t, backups[0]) != "1234567890" || readLog(t, path) != "X" {
		t.Fatalf("size rotation: %v", backups)
	}
	big := strings.Repeat("B", 25) // oversized record on an empty file is still written
	f.Write([]byte(big))
	if backups, _ := f.Backups(); len(backups) != 2 || readLog(t, path) != big {
		t.Fatalf("oversized record handling: %v", backups)
	}
}

func TestTimeRotationAndRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jelee.log")
	clock := newClock()
	f, err := openRotatingFile(RotateOptions{Path: path, Interval: time.Hour, MaxBackups: 3, Compress: true}, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write([]byte("hour0\n"))
	clock.Advance(59 * time.Minute)
	f.Write([]byte("still0\n"))
	if backups, _ := f.Backups(); len(backups) != 0 {
		t.Fatal("rotated before the interval")
	}
	for i := 1; i <= 5; i++ {
		clock.Advance(time.Hour)
		f.Write([]byte("hour" + string(rune('0'+i)) + "\n"))
	}
	backups, err := f.Backups()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 3 {
		t.Fatalf("retained %d backups: %v", len(backups), backups)
	}
	// Oldest first: hours 0..1 were pruned; the newest backup holds hour4.
	for i, want := range []string{"hour2\n", "hour3\n", "hour4\n"} {
		if !strings.HasSuffix(backups[i], ".log.gz") || readLog(t, backups[i]) != want {
			t.Fatalf("backup %d = %s", i, backups[i])
		}
		assertMode(t, backups[i], 0o600)
	}
	if readLog(t, path) != "hour5\n" {
		t.Fatal("active file content")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 4 {
		t.Fatalf("unexpected files: %d", len(entries))
	}
}

func TestSameInstantRotationsStayOrdered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	f, err := openRotatingFile(RotateOptions{Path: path, MaxBytes: 1, MaxBackups: 20}, newClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, c := range "abcdefghijkl" {
		f.Write([]byte(string(c)))
	}
	backups, _ := f.Backups()
	var got string
	for _, b := range backups {
		got += readLog(t, b)
	}
	if got != "abcdefghijk" {
		t.Fatalf("backup order %q", got)
	}
}

func TestLogFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs", "nested")
	path := filepath.Join(dir, "jelee.log")
	f, err := OpenRotatingFile(RotateOptions{Path: path, MaxBytes: 4, MaxBackups: 2})
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("abcd"))
	f.Write([]byte("efgh"))
	f.Close()
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
	backups, _ := f.Backups()
	if len(backups) != 1 {
		t.Fatal("no backup")
	}
	assertMode(t, backups[0], 0o600)

	// A pre-existing world-readable file is narrowed on open.
	loose := filepath.Join(t.TempDir(), "loose.log")
	if err := os.WriteFile(loose, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(loose, 0o644)
	g, err := OpenRotatingFile(RotateOptions{Path: loose})
	if err != nil {
		t.Fatal(err)
	}
	g.Write([]byte("new\n"))
	g.Close()
	assertMode(t, loose, 0o600)
	if readLog(t, loose) != "old\nnew\n" {
		t.Fatal("existing log was not appended")
	}
}

func TestRotatingFileRejectsUnsafeTargets(t *testing.T) {
	if _, err := OpenRotatingFile(RotateOptions{Path: "relative.log"}); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := OpenRotatingFile(RotateOptions{Path: t.TempDir()}); err == nil {
		t.Fatal("directory accepted as log file")
	}
	f, err := OpenRotatingFile(RotateOptions{Path: filepath.Join(t.TempDir(), "x.log")})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := f.Write([]byte("x")); err == nil {
		t.Fatal("write after close")
	}
}

func TestRouterWritesJSONFileAlongsideConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jelee.log")
	r, out := openTest(t, Options{Format: FormatConsole, Output: OutputBoth, File: RotateOptions{Path: path, MaxBytes: 1 << 20, MaxBackups: 1}})
	r.Logger().Info("both sinks", "component", "http", "status", 200)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "INFO  both sinks component=http status=200") {
		t.Fatalf("console sink: %q", out.String())
	}
	if !strings.Contains(readLog(t, path), `"msg":"both sinks","component":"http","status":200`) {
		t.Fatalf("file sink: %q", readLog(t, path))
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)
}

func TestRouterFileOnlyLeavesStdoutUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jelee.log")
	r, err := Open(Options{Output: OutputFile, File: RotateOptions{Path: path}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Warn("file only")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(t, path), "file only") {
		t.Fatal("file-only output lost the record")
	}
}
