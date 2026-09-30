package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/toolidentity"
)

func TestDoctorToolsRejectsAllExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"--path", "private"}, {"ffmpeg"}, {"--"}, {"-h"}} {
		var out, errOut bytes.Buffer
		if code := runMediaTools(context.Background(), args, &out, &errOut); code != 2 || out.Len() != 0 || strings.Contains(errOut.String(), "private") {
			t.Fatalf("unsafe usage result: %d %s", code, &errOut)
		}
	}
}

func TestDoctorToolsDoesNotLoadDatabaseConfig(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "invalid private database value")
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	if code := runMediaTools(context.Background(), nil, &out, &errOut); code != 1 {
		t.Fatalf("missing tool exit %d", code)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 || result["tool"] != "ffprobe" || result["state"] != "missing" || result["reason"] != "missing_tool" || result["capability"] != "disabled_sandbox" || errOut.Len() != 0 {
		t.Fatalf("unexpected diagnostic: %s %s", &out, &errOut)
	}
	if strings.Contains(out.String(), "database") || strings.Contains(out.String(), "private") {
		t.Fatal("configuration leaked into tool diagnostic")
	}
}

type toolFailWriter struct{}

func (toolFailWriter) Write([]byte) (int, error) { return 0, errors.New("private/path secret") }
func TestDoctorToolsOutputFailureAndExitCodes(t *testing.T) {
	for state, want := range map[string]int{"verified": 0, "missing": 1, "timed_out": 124, "cancelled": 130} {
		var out, errOut bytes.Buffer
		code := writeMediaDiagnostic(context.Background(), toolidentity.Diagnostic{State: state}, &out, &errOut)
		if code != want {
			t.Fatalf("%s code %d", state, code)
		}
	}
	var errOut bytes.Buffer
	if code := writeMediaDiagnostic(context.Background(), toolidentity.Diagnostic{}, toolFailWriter{}, &errOut); code != 1 || errOut.String() != "tool_output_failed\n" {
		t.Fatal("unsafe write error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := writeMediaDiagnostic(ctx, toolidentity.Diagnostic{}, toolFailWriter{}, &errOut); code != 130 {
		t.Fatal("cancelled output exit")
	}
}

func TestDoctorToolsMainDispatchBeforeConfig(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "invalid")
	t.Chdir(t.TempDir())
	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	os.Args = []string{"jelee-cli", "doctor", "tools"}
	os.Stdout = writer
	os.Stderr = writer
	defer func() { os.Args = oldArgs; os.Stdout = oldOut; os.Stderr = oldErr }()
	if code := run(); code != 1 {
		t.Fatalf("exit %d", code)
	}
	writer.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), `"reason":"missing_tool"`) || strings.Contains(buffer.String(), "configuration") {
		t.Fatal("main dispatch loaded config")
	}
}

type blockedToolOutput struct {
	closed chan struct{}
	once   sync.Once
}

func (w *blockedToolOutput) Write([]byte) (int, error) { <-w.closed; return 0, io.ErrClosedPipe }
func (w *blockedToolOutput) Close() error              { w.once.Do(func() { close(w.closed) }); return nil }
func TestDoctorToolsOutputCancellation(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	output := &blockedToolOutput{closed: make(chan struct{})}
	var errOut bytes.Buffer
	if code := runMediaToolsWithOutputCancellation(ctx, nil, output, &errOut); code != 124 {
		t.Fatalf("blocked output exit %d", code)
	}
}
