package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

func TestStartupSweepRemovesDeadLeftoversWithoutLoggingPaths(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("liveness is proven on Linux in this test")
	}
	processRoot, imageRoot := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", processRoot)
	// A PID above the kernel maximum never exists, so its owner is dead.
	deadOwner := func(name string) string {
		return strings.Replace(name, "o"+strconv.Itoa(os.Getpid())+"-", "o"+strconv.Itoa(1<<22+1)+"-", 1)
	}
	live, err := scratch.MkdirOwned("", scratch.ServiceProbe)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(live) != processRoot {
		t.Skip("platform does not honor TMPDIR")
	}
	dead := deadOwner(live)
	if dead == live {
		t.Skip("process identity unavailable on this host")
	}
	if err := os.MkdirAll(filepath.Join(dead, "run-1"), 0700); err != nil {
		t.Fatal(err)
	}
	name, err := scratch.ImageStage.NewName()
	if err != nil {
		t.Fatal(err)
	}
	deadImage := filepath.Join(imageRoot, deadOwner(name))
	foreign := filepath.Join(imageRoot, "image-operator.partial")
	for _, path := range []string{deadImage, foreign} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	cfg := config.Config{EnableImages: true}
	cfg.Images.TempRoot = imageRoot
	sweepStartupTemporaries(context.Background(), cfg, slog.New(slog.NewJSONHandler(&output, nil)))
	for _, path := range []string{dead, deadImage} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("dead leftover survived the startup sweep")
		}
	}
	for _, path := range []string{live, foreign} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("live or foreign object was removed")
		}
	}
	logged := output.String()
	if strings.Count(logged, "startup temporary sweep removed leftovers") != 2 || strings.Contains(logged, processRoot) || strings.Contains(logged, imageRoot) || strings.Contains(logged, "jelee-service") {
		t.Fatalf("unexpected sweep log: %s", logged)
	}
}
