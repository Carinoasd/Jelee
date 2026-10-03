package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a goroutine-safe sink for asynchronous writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func openTest(t *testing.T, opts Options) (*Router, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	r, err := Open(opts, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, out
}

func flushed(t *testing.T, r *Router, out *syncBuffer) string {
	t.Helper()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	out.Reset()
	return s
}

func TestDebugIsOffByDefault(t *testing.T) {
	r, out := openTest(t, Options{})
	log := r.Logger()
	log.Debug("debug hidden", "component", "http")
	log.Debug("debug hidden")
	log.Info("info shown", "component", "http")
	got := flushed(t, r, out)
	if strings.Contains(got, "debug hidden") || !strings.Contains(got, "info shown") {
		t.Fatalf("default level output: %s", got)
	}
	if r.Level(GlobalComponent) != slog.LevelInfo || log.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("DEBUG enabled by default")
	}
}

func TestComponentLevelsFilterIndependently(t *testing.T) {
	r, out := openTest(t, Options{Level: slog.LevelWarn, Components: map[string]slog.Level{"scan": slog.LevelDebug, "http": slog.LevelError}})
	log := r.Logger()
	log.Debug("scan debug kept", "component", "scan")
	log.Debug("ignore debug kept", "component", "ignore")
	log.Info("jobs info dropped", "component", "jobs")
	log.Warn("jobs warn kept", "component", "jobs")
	log.Warn("http warn dropped", "component", "http")
	log.Error("http error kept", "component", "http")
	log.Info("global info dropped")
	log.Warn("unknown component follows global", "component", "elsewhere")
	log.With("component", "scan").Debug("scoped scan debug kept")
	log.With("component", "http").WithGroup("request").Warn("scoped http warn dropped")
	got := flushed(t, r, out)
	for _, kept := range []string{"scan debug kept", "ignore debug kept", "jobs warn kept", "http error kept", "unknown component follows global", "scoped scan debug kept"} {
		if !strings.Contains(got, kept) {
			t.Errorf("missing %q", kept)
		}
	}
	for _, dropped := range []string{"jobs info dropped", "http warn dropped", "global info dropped", "scoped http warn dropped"} {
		if strings.Contains(got, dropped) {
			t.Errorf("unexpected %q", dropped)
		}
	}
}

func TestSetLevelTakesEffectImmediately(t *testing.T) {
	r, out := openTest(t, Options{})
	log := r.Logger()
	derived := log.With("component", "probe")
	derived.Debug("before")
	if err := r.SetLevel("probe", slog.LevelDebug); err != nil {
		t.Fatal(err)
	}
	derived.Debug("after component change")
	log.Debug("global still info", "component", "nfo")
	if err := r.SetLevel(GlobalComponent, slog.LevelError); err != nil {
		t.Fatal(err)
	}
	log.Warn("global raised", "component", "nfo")
	derived.Debug("component override survives global change")
	if err := r.ResetLevel("probe"); err != nil {
		t.Fatal(err)
	}
	derived.Warn("probe follows global again")
	if err := r.SetLevel("metrics", slog.LevelDebug); err != nil || r.Level("db") != slog.LevelDebug {
		t.Fatal("alias did not address its scope")
	}
	got := flushed(t, r, out)
	for _, kept := range []string{"after component change", "component override survives global change"} {
		if !strings.Contains(got, kept) {
			t.Errorf("missing %q", kept)
		}
	}
	for _, dropped := range []string{"before", "global still info", "global raised", "probe follows global again"} {
		if strings.Contains(got, "\""+dropped+"\"") {
			t.Errorf("unexpected %q", dropped)
		}
	}
	if !errors.Is(r.SetLevel("unknown", slog.LevelDebug), ErrUnknownComponent) || !errors.Is(r.ResetLevel("unknown"), ErrUnknownComponent) {
		t.Fatal("unknown component accepted")
	}
}

func TestConcurrentLevelChangesAndLogging(t *testing.T) {
	r, _ := openTest(t, Options{BufferEntries: 64})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				r.Logger().Debug("concurrent", "component", Components[j%len(Components)])
			}
		}()
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				level := slog.LevelDebug
				if (i+j)%2 == 0 {
					level = slog.LevelError
				}
				_ = r.SetLevel(Components[j%len(Components)], level)
				_ = r.SetLevel(GlobalComponent, level)
			}
		}(i)
	}
	wg.Wait()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestParseLevel(t *testing.T) {
	for name, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := ParseLevel(name); err != nil || got != want {
			t.Errorf("ParseLevel(%q)=%v,%v", name, got, err)
		}
	}
	for _, name := range []string{"", "trace", "debug ", "-4"} {
		if _, err := ParseLevel(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestOpenRejectsInvalidOptions(t *testing.T) {
	for name, opts := range map[string]Options{
		"format":     {Format: "xml"},
		"output":     {Output: "syslog"},
		"buffer":     {BufferEntries: -1},
		"ip":         {IPMode: "raw"},
		"path":       {PathMode: "absolute"},
		"component":  {Components: map[string]slog.Level{"nope": slog.LevelDebug}},
		"file":       {Output: OutputFile, File: RotateOptions{Path: "relative.log"}},
		"fileLimits": {Output: OutputFile, File: RotateOptions{Path: "/tmp/x.log", MaxBytes: -1}},
	} {
		if _, err := Open(opts, &syncBuffer{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Open(Options{}, nil); err == nil {
		t.Error("nil stdout accepted")
	}
}
