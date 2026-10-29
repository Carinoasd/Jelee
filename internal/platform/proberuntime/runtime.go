// Package proberuntime registers the shipped, isolated Linux metadata helper
// and, on Windows amd64, the confined direct ffprobe.exe (ADR 0011). Its
// locations and digests come only from the embedded dependency manifest.
package proberuntime

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"runtime"
	"strconv"
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
	return policyFor(runtime.GOOS + "-" + runtime.GOARCH)
}

func policyFor(platform string) (sandbox.Policy, error) {
	if platform == "windows-amd64" {
		// The Windows vendor build is static: the policy is the executable
		// digest alone; the launcher pins the image while it lives.
		probe, err := tools.FFprobeSpec(platform)
		if err != nil {
			return sandbox.Policy{}, ErrUnavailable
		}
		return sandbox.Policy{FFprobeSHA256: probe.SHA256, RequireProtectedFiles: true}, nil
	}
	if platform != "linux-amd64" {
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
	if runtime.GOOS != "linux" {
		// Only Linux has a helper; Windows starts the tool directly.
		_, _ = os.Stderr.WriteString("probe_runtime_unavailable\n")
		return sandbox.ExitUnavailable
	}
	policy, err := Policy()
	if err != nil {
		_, _ = os.Stderr.WriteString("probe_runtime_unavailable\n")
		return sandbox.ExitUnavailable
	}
	// Production accepts only the canonical descriptor for the shipped path.
	// The sandbox then independently validates its schema, policy and file bytes.
	if len(argv) != 1 || !expectedDescriptor(argv[0]) {
		_, _ = os.Stderr.WriteString("probe_runtime_invalid\n")
		return sandbox.ExitInvalid
	}
	return sandbox.RunHelper(argv, policy)
}

// expectedDescriptor accepts the metadata descriptor and the cover and
// chapter image read descriptors of the shipped path only, in their canonical
// encodings.
func expectedDescriptor(value string) bool {
	if value == base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"mode":"metadata","ffprobePath":"`+FFprobePath+`"}`)) {
		return true
	}
	for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
		for _, mode := range []string{"cover", "chapters"} {
			if value == base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"mode":"`+mode+`","ffprobePath":"`+FFprobePath+`","stream":`+strconv.Itoa(stream)+`}`)) {
				return true
			}
		}
	}
	return false
}

// ffprobeLocation is the fixed image path on Linux. On Windows it is the
// manifest's project-relative ffprobe.exe under the installation root, the
// parent of the directory holding the running executable (bin\jelee.exe);
// neither the working directory nor the environment can select it.
func ffprobeLocation() (string, error) {
	if runtime.GOOS != "windows" {
		return FFprobePath, nil
	}
	spec, err := tools.FFprobeSpec("windows-amd64")
	if err != nil {
		return "", ErrUnavailable
	}
	executable, err := os.Executable()
	if err != nil {
		return "", ErrUnavailable
	}
	location, ok := sandbox.WindowsToolLocation(executable, spec.ExecutablePath, "ffprobe.exe")
	if !ok {
		return "", ErrUnavailable
	}
	return location, nil
}

// CoverMaxStdoutBytes bounds one cover read's hex dump. ffprobe prints about
// 4.3 bytes per payload byte, so this admits pictures somewhat above
// domain.EmbeddedCoverMaxBytes; anything larger fails as an output limit.
const CoverMaxStdoutBytes = 16 << 20

// NewCover creates only the embedded cover reads (G40.4) with their own
// process admission: one child at a time, a short timeout and a bounded
// output. TempRoot is a caller-owned private scratch directory.
func NewCover(ctx context.Context, tempRoot string) (*process.IsolatedRunner, error) {
	policy, err := Policy()
	if err != nil {
		return nil, ErrUnavailable
	}
	location, err := ffprobeLocation()
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.New(ctx, sandbox.Profile{FFprobePath: location}, policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	runner, err := process.NewIsolatedFFprobeCover(process.Config{
		MaxConcurrent: 1, Timeout: 20 * time.Second,
		MaxStdoutBytes: CoverMaxStdoutBytes, MaxStderrBytes: 64 << 10, TempRoot: tempRoot,
	}, launcher)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runner, nil
}

// ChapterMaxStdoutBytes bounds one chapter image read: the hex dumps of up to
// domain.ChapterImageMaxTotalBytes of pictures (about 4.3 output bytes per
// payload byte) plus one more picture, the attached picture copy, and the
// JSON envelope. More fails as an output limit and is remembered as
// too_large. It is the process runner's maximum.
const ChapterMaxStdoutBytes = 16 << 20

// NewChapters creates only the chapter image reads (G40.1) with their own
// process admission: one child at a time, a short timeout and a bounded
// output. TempRoot is a caller-owned private scratch directory.
func NewChapters(ctx context.Context, tempRoot string) (*process.IsolatedRunner, error) {
	policy, err := Policy()
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.New(ctx, sandbox.Profile{FFprobePath: FFprobePath}, policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	runner, err := process.NewIsolatedFFprobeChapters(process.Config{
		MaxConcurrent: 1, Timeout: 30 * time.Second,
		MaxStdoutBytes: ChapterMaxStdoutBytes, MaxStderrBytes: 64 << 10, TempRoot: tempRoot,
	}, launcher)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runner, nil
}

// New creates only the fixed metadata operation. TempRoot is a caller-owned,
// private scratch directory; no media path is passed in argv or environment.
func New(ctx context.Context, tempRoot string) (*process.IsolatedRunner, error) {
	policy, err := Policy()
	if err != nil {
		return nil, ErrUnavailable
	}
	location, err := ffprobeLocation()
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.New(ctx, sandbox.Profile{FFprobePath: location}, policy)
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
