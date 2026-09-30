package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func TestDoctorProbeArgumentsAndSafeExitCodes(t *testing.T) {
	for _, argv := range [][]string{{"--path", "private-path"}, {"ffmpeg"}, {"--"}, {"-h"}} {
		var out, errOut bytes.Buffer
		if code := runProbeDiagnostic(context.Background(), argv, &out, &errOut); code != 2 || out.Len() != 0 || errOut.String() != "usage: jelee-cli doctor probe\n" {
			t.Fatal("invalid diagnostic usage contract")
		}
	}
	for state, want := range map[string]int{"available": 0, "unavailable": 1, "timed_out": 124, "cancelled": 130} {
		var out, errOut bytes.Buffer
		if code := writeProbeDiagnostic(context.Background(), proberuntime.Diagnostic{State: state}, &out, &errOut); code != want {
			t.Fatalf("%s exit %d", state, code)
		}
	}
	var errOut bytes.Buffer
	if code := writeProbeDiagnostic(context.Background(), proberuntime.Diagnostic{}, toolFailWriter{}, &errOut); code != 1 || errOut.String() != "probe_output_failed\n" {
		t.Fatal("raw output error leaked")
	}
}

func TestDoctorProbeMainBypassesConfigAndDB(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "invalid private database")
	t.Setenv("JELEE_DATABASE_URL_FILE", "/private/missing-config")
	t.Chdir(t.TempDir())
	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	os.Args = []string{"jelee-cli", "doctor", "probe"}
	os.Stdout = writer
	os.Stderr = writer
	defer func() { os.Args, os.Stdout, os.Stderr = oldArgs, oldOut, oldErr }()
	code := run()
	writer.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	var result proberuntime.Diagnostic
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal("main did not return only safe diagnostic JSON")
	}
	if code != 1 || result.Capability != "disabled" || result.State != "unavailable" || strings.Contains(out.String(), "private") || strings.Contains(out.String(), "configuration") {
		t.Fatal("diagnostic entered service/DB path or leaked configuration")
	}
}

func TestProbeHelperDispatchBeforeConfig(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL_FILE", "/private/missing-config")
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"jelee-cli", sandbox.HelperCommand, "malformed"}
	if code := run(); code != sandbox.ExitInvalid && code != sandbox.ExitUnavailable {
		t.Fatal("helper entered ordinary CLI/service path")
	}
}

func TestDoctorProbeOutputCancellationJoinsCloser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	output := &blockedToolOutput{closed: make(chan struct{})}
	var errOut bytes.Buffer
	if code := runProbeDiagnosticWithOutputCancellation(ctx, nil, output, &errOut); code != 124 {
		t.Fatalf("blocked output exit %d", code)
	}
	select {
	case <-output.closed:
	default:
		t.Fatal("output close callback not joined")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if code := writeProbeDiagnostic(ctx, proberuntime.Diagnostic{}, toolFailWriter{}, &errOut); code != 130 {
		t.Fatal("cancelled output exit")
	}
}

type probeCancelWriter struct{ cancel context.CancelFunc }

func (writer probeCancelWriter) Write(value []byte) (int, error) {
	writer.cancel()
	return len(value), nil
}

func TestDoctorProbeCancellationDuringSuccessfulWriteCannotReturnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errOut bytes.Buffer
	if code := writeProbeDiagnostic(ctx, proberuntime.Diagnostic{State: "available"}, probeCancelWriter{cancel}, &errOut); code != 130 {
		t.Fatal("cancelled output returned successful capability exit")
	}
}
