// Package proberuntime registers the shipped, isolated Linux metadata helper.
// Its locations and digests come only from the embedded dependency manifest.
package proberuntime

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

const FFprobePath = "/usr/lib/jelee/ffprobe"

var ErrUnavailable = errors.New("probe_runtime_unavailable")

// Policy is independent of the helper descriptor and the mutable filesystem.
// Project-local developer installations cannot enable this production profile.
func Policy() (sandbox.Policy, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return sandbox.Policy{}, ErrUnavailable
	}
	probe, err := tools.FFprobeSpec("linux-amd64")
	if err != nil {
		return sandbox.Policy{}, ErrUnavailable
	}
	spec, err := tools.RuntimeSpec("linux-amd64")
	if err != nil || len(spec.Libraries) != 8 {
		return sandbox.Policy{}, ErrUnavailable
	}
	policy := sandbox.Policy{FFprobeSHA256: probe.SHA256, RequireProtectedFiles: true}
	for _, library := range spec.Libraries {
		policy.Libraries = append(policy.Libraries, sandbox.PinnedFile{Path: library.ContainerPath, SHA256: library.SHA256})
	}
	return policy, nil
}

// Helper dispatch must run before service configuration, DB access or Fx.
// The caller immediately exits with the returned code; success replaces itself.
func Helper(argv []string) int {
	policy, err := Policy()
	if err != nil {
		_, _ = os.Stderr.WriteString("probe_runtime_unavailable\n")
		return sandbox.ExitUnavailable
	}
	// Production accepts only the canonical descriptor for the shipped path.
	// The sandbox then independently validates its schema, policy and file bytes.
	expected := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"mode":"metadata","ffprobePath":"` + FFprobePath + `"}`))
	if len(argv) != 1 || argv[0] != expected {
		_, _ = os.Stderr.WriteString("probe_runtime_invalid\n")
		return sandbox.ExitInvalid
	}
	return sandbox.RunHelper(argv, policy)
}

// New creates only the fixed metadata operation. TempRoot is a caller-owned,
// private scratch directory; no media path is passed in argv or environment.
func New(ctx context.Context, tempRoot string) (*process.IsolatedRunner, error) {
	policy, err := Policy()
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.New(ctx, sandbox.Profile{FFprobePath: FFprobePath}, policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	runner, err := process.NewIsolatedFFprobe(process.Config{
		MaxConcurrent: 2, Timeout: 30 * time.Second,
		MaxStdoutBytes: 4 << 20, MaxStderrBytes: 64 << 10, TempRoot: tempRoot,
	}, launcher)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runner, nil
}
