// Package mkvruntime registers the shipped, isolated mkvtoolnix and MediaInfo
// helpers (E4, G09.2, G15.5, G15.7, G19.1). Locations and digests come only
// from the embedded dependency manifest; a missing or altered file disables
// only its own capability. There is no unsandboxed fallback and no
// mkvpropedit registration: Jelee never writes into original media.
package mkvruntime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

// ErrUnavailable is the fixed failure of every registration step.
var ErrUnavailable = errors.New("matroska_runtime_unavailable")

// Executable names per mode. The image paths come from the manifest.
var executables = map[sandbox.ToolMode]string{sandbox.ToolMediaInfo: "mediainfo", sandbox.ToolIdentify: "mkvmerge", sandbox.ToolExtract: "mkvextract"}

// Modes lists the registered modes in a fixed order.
var Modes = []sandbox.ToolMode{sandbox.ToolMediaInfo, sandbox.ToolIdentify, sandbox.ToolExtract}

// Registration returns the shipped profile and policy of a mode. It reads
// only embedded data and never consults the filesystem, a descriptor or a
// request.
func Registration(mode sandbox.ToolMode) (sandbox.ToolProfile, sandbox.ToolPolicy, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, ErrUnavailable
	}
	return registration(mode)
}

func registration(mode sandbox.ToolMode) (sandbox.ToolProfile, sandbox.ToolPolicy, error) {
	name, ok := executables[mode]
	if !ok {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, ErrUnavailable
	}
	spec, err := tools.MatroskaToolSpec("linux-amd64", name)
	if err != nil || spec.Executable.ContainerPath == "" {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, ErrUnavailable
	}
	policy := sandbox.ToolPolicy{ExecutableSHA256: spec.Executable.SHA256, RequireProtectedFiles: true}
	for _, library := range spec.Libraries {
		policy.Libraries = append(policy.Libraries, sandbox.PinnedFile{Path: library.ContainerPath, SHA256: library.SHA256})
	}
	return sandbox.ToolProfile{Mode: mode, Path: spec.Executable.ContainerPath}, policy, nil
}

// Helper dispatch must run before service configuration, DB access or Fx.
// The caller immediately exits with the returned code; success replaces it.
func Helper(argv []string) int {
	return sandbox.RunToolHelper(argv, func(mode sandbox.ToolMode) (sandbox.ToolProfile, sandbox.ToolPolicy, bool) {
		profile, policy, err := Registration(mode)
		return profile, policy, err == nil
	})
}

// Limits per mode. Identification and MediaInfo print bounded JSON; the
// extraction writes files (bounded by RLIMIT_FSIZE) and prints nothing.
func config(mode sandbox.ToolMode, tempRoot string) process.Config {
	switch mode {
	case sandbox.ToolExtract:
		return process.Config{MaxConcurrent: 1, Timeout: 10 * time.Minute, MaxStdoutBytes: 64 << 10, MaxStderrBytes: 64 << 10, TempRoot: tempRoot}
	default:
		return process.Config{MaxConcurrent: 2, Timeout: time.Minute, MaxStdoutBytes: 4 << 20, MaxStderrBytes: 64 << 10, TempRoot: tempRoot}
	}
}

// New registers one mode. tempRoot is a caller-owned private scratch
// directory; the runner creates one private working directory per run.
func New(ctx context.Context, mode sandbox.ToolMode, tempRoot string) (*process.IsolatedToolRunner, error) {
	profile, policy, err := Registration(mode)
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.NewTool(ctx, profile, policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	runner, err := process.NewIsolatedTool(config(mode, tempRoot), launcher)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runner, nil
}

// SupplementIdentity describes the MediaInfo supplement for a probe
// identity: the executable and closure digest, and its argv digest. A probe
// identity built with it never matches one built without it.
func SupplementIdentity() (closure, arguments string, err error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return "", "", ErrUnavailable
	}
	return supplementIdentity()
}

func supplementIdentity() (string, string, error) {
	spec, err := tools.MatroskaToolSpec("linux-amd64", "mediainfo")
	if err != nil {
		return "", "", ErrUnavailable
	}
	files := slices.Clone(spec.Libraries)
	slices.SortFunc(files, func(a, b tools.RuntimeFile) int { return strings.Compare(a.ContainerPath, b.ContainerPath) })
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-mediainfo-closure-v1\x00" + spec.Version + "\x00"))
	for _, file := range append([]tools.RuntimeFile{spec.Executable}, files...) {
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 {
			return "", "", ErrUnavailable
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(file.ContainerPath)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(file.ContainerPath))
		_, _ = hash.Write(digest)
	}
	return hex.EncodeToString(hash.Sum(nil)), sandbox.ToolArgumentsDigest(sandbox.ToolMediaInfo), nil
}

// ToolStatus is a fixed diagnostic classification for one executable.
type ToolStatus struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
}

// Diagnose classifies each shipped executable without executing it:
// verified (sandbox verification of the file and its exact closure passed),
// missing, invalid, or platform_unsupported.
func Diagnose(ctx context.Context) []ToolStatus {
	result := make([]ToolStatus, 0, len(Modes))
	for _, mode := range Modes {
		status := ToolStatus{Name: executables[mode], State: "platform_unsupported"}
		if spec, err := tools.MatroskaToolSpec("linux-amd64", executables[mode]); err == nil {
			status.Version = spec.Version
		}
		profile, policy, err := Registration(mode)
		if err == nil {
			status.State = "verified"
			if _, statErr := os.Lstat(profile.Path); errors.Is(statErr, os.ErrNotExist) {
				status.State = "missing"
			} else if _, err := sandbox.NewTool(ctx, profile, policy); err != nil {
				status.State = "invalid"
			}
		}
		result = append(result, status)
	}
	return result
}
