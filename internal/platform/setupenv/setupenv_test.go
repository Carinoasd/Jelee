package setupenv

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
)

func testEnvironment(t *testing.T, facts DatabaseFacts, listen string) *Environment {
	t.Helper()
	e, err := New(Options{
		Database:       func(context.Context) (DatabaseFacts, error) { return facts, nil },
		RequiredSchema: 71, Listen: listen, TMDBConfigured: true,
		DetectTools: func(context.Context) (app.SetupToolReport, error) {
			return app.SetupToolReport{Available: []string{"ffprobe"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNewRequiresDatabaseProbe(t *testing.T) {
	if _, err := New(Options{RequiredSchema: 1}); err == nil {
		t.Fatal("missing database probe accepted")
	}
	if _, err := New(Options{Database: func(context.Context) (DatabaseFacts, error) { return DatabaseFacts{}, nil }}); err == nil {
		t.Fatal("missing schema version accepted")
	}
}

func TestDatabaseStatusMapsFacts(t *testing.T) {
	e := testEnvironment(t, DatabaseFacts{ServerVersion: 140010, SchemaVersion: 70, Dirty: true}, "")
	status, err := e.DatabaseStatus(context.Background())
	if err != nil || status != (app.SetupDatabaseStatus{ServerVersion: 140010, MinServerVersion: MinServerVersion, SchemaVersion: 70, RequiredSchema: 71, Clean: false}) {
		t.Fatalf("%+v %v", status, err)
	}
	failing, _ := New(Options{RequiredSchema: 1, Database: func(context.Context) (DatabaseFacts, error) { return DatabaseFacts{}, errors.New("down") }})
	if _, err = failing.DatabaseStatus(context.Background()); err == nil {
		t.Fatal("database failure hidden")
	}
	if !e.TMDBCredentialConfigured() {
		t.Fatal("TMDB flag lost")
	}
	if report, err := e.DetectTools(context.Background()); err != nil || len(report.Available) != 1 {
		t.Fatalf("tools: %+v %v", report, err)
	}
}

// G18.3: missing, not a directory and unreadable are told apart.
func TestInspectDirectory(t *testing.T) {
	e := testEnvironment(t, DatabaseFacts{}, "")
	ctx := context.Background()
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		path string
		want app.SetupDirectoryStatus
	}{
		"readable": {root, app.SetupDirectoryStatus{Exists: true, Directory: true, Readable: true}},
		"missing":  {filepath.Join(root, "missing"), app.SetupDirectoryStatus{}},
		"file":     {file, app.SetupDirectoryStatus{Exists: true}},
	} {
		if got, err := e.InspectDirectory(ctx, c.path); err != nil || got != c.want {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		if got, err := e.InspectDirectory(ctx, locked); err != nil || got != (app.SetupDirectoryStatus{Exists: true, Directory: true}) {
			t.Fatalf("unreadable: %+v %v", got, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.InspectDirectory(cancelled, root); err == nil {
		t.Fatal("cancelled inspection succeeded")
	}
}

// G18.3: a port held by another program is a conflict; the server's own
// configured address is always accepted.
func TestListenAvailable(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	busy := held.Addr().String()
	ctx := context.Background()
	if ok, err := testEnvironment(t, DatabaseFacts{}, "").ListenAvailable(ctx, busy); err != nil || ok {
		t.Fatalf("busy port: %t %v", ok, err)
	}
	if ok, err := testEnvironment(t, DatabaseFacts{}, busy).ListenAvailable(ctx, busy); err != nil || !ok {
		t.Fatalf("own address: %t %v", ok, err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := free.Addr().String()
	free.Close()
	if ok, err := testEnvironment(t, DatabaseFacts{}, "").ListenAvailable(ctx, address); err != nil || !ok {
		t.Fatalf("free port: %t %v", ok, err)
	}
}
