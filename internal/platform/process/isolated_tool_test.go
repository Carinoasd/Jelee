package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunWithCollectsThePrivateDirectoryBeforeRemovingIt(t *testing.T) {
	runner := testRunner(t, 10*time.Second, map[string][]string{"write": helperArgs("write"), "failure": helperArgs("failure")})
	var seen string
	collect := func(directory string) error {
		data, err := os.ReadFile(filepath.Join(directory, "t0"))
		if err != nil || string(data) != "extracted output" || filepath.Dir(directory) != runner.config.TempRoot {
			return errors.New("unexpected output")
		}
		seen = directory
		return nil
	}
	if _, err := runner.runWith(context.Background(), Request{Tool: "helper"}, helperArgs("write"), nil, collect); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("private directory survived the run")
	}
	assertClean(t, runner)
	sentinel := errors.New("collect refused")
	if _, err := runner.runWith(context.Background(), Request{Tool: "helper"}, helperArgs("write"), nil, func(string) error { return sentinel }); err != sentinel {
		t.Fatalf("collect error not returned: %v", err)
	}
	called := false
	if _, err := runner.runWith(context.Background(), Request{Tool: "helper"}, helperArgs("failure"), nil, func(string) error { called = true; return nil }); !errors.Is(err, ErrExit) || called {
		t.Fatal("collect ran after a failed exit")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.runWith(cancelled, Request{Tool: "helper"}, helperArgs("write"), nil, func(string) error { called = true; return nil }); !errors.Is(err, ErrCancelled) || called {
		t.Fatal("collect ran for a cancelled run")
	}
	if _, err := runner.runWith(context.Background(), Request{Tool: "unknown"}, helperArgs("write"), nil, nil); err != ErrInvalid {
		t.Fatal("unknown tool accepted")
	}
	assertClean(t, runner)
}

func TestIsolatedToolRunnerRefusesUnsealedRequests(t *testing.T) {
	if _, err := NewIsolatedTool(Config{}, nil); err != ErrInvalid {
		t.Fatal("nil launcher accepted")
	}
	var runner *IsolatedToolRunner
	if _, err := runner.Run(context.Background(), ToolRequest{}); err != ErrInvalid {
		t.Fatal("nil runner accepted")
	}
	if runner.Stats() != (Stats{}) {
		t.Fatal("nil runner stats")
	}
}
