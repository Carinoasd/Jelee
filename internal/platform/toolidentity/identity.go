// Package toolidentity diagnoses an optional pinned ffprobe installation.
// It runs only -version on a verified private snapshot. This trusted-host
// diagnostic does not isolate the OS loader or certify shared libraries.
package toolidentity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/tools"
)

const maxExecutableBytes int64 = 512 << 20
const maxLicenseBytes int64 = 4 << 20

// Diagnostic is deliberately limited to fixed public classifications.
type Diagnostic struct {
	Tool            string `json:"tool"`
	Platform        string `json:"platform"`
	ExpectedVersion string `json:"expectedVersion"`
	State           string `json:"state"`
	Reason          string `json:"reason"`
	Capability      string `json:"capability"`
}

type executeVersion func(context.Context, string, string) ([]byte, error)

// Diagnose uses the trusted operator's absolute project directory. It never
// reads a mutable manifest, searches PATH, downloads, or invokes ffmpeg.
// It needs write access below .testdata for a unique private snapshot, removed
// after diagnosis. Cancellation bounds copying and execution; an OS filesystem
// call stuck in the kernel may not return immediately on cancellation.
// Linux requires enforceable 0700 permissions (some shared/DrvFS mounts cannot
// supply them); Windows requires a token allowed to protect a fresh directory's
// DACL. Unsupported permission semantics fail closed before snapshot execution.
func Diagnose(ctx context.Context, project string) Diagnostic {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	spec, err := tools.FFprobeSpec(platform)
	if err != nil {
		return Diagnostic{Tool: "ffprobe", Platform: platform, State: "unsupported", Reason: "unsupported_platform", Capability: "disabled_sandbox"}
	}
	return diagnose(ctx, project, spec, runVersion)
}

func diagnose(ctx context.Context, project string, spec tools.FFprobeSpecification, execute executeVersion) (result Diagnostic) {
	result = Diagnostic{Tool: "ffprobe", Platform: spec.Platform, ExpectedVersion: spec.VendorVersion, State: "unavailable", Reason: "unavailable", Capability: "disabled_sandbox"}
	fail := func(reason string) Diagnostic {
		result.Reason = reason
		switch reason {
		case "missing_tool", "missing_license":
			result.State = "missing"
		case "hash_mismatch", "license_hash_mismatch":
			result.State = "tampered"
		case "version_mismatch", "unsafe_path":
			result.State = "invalid"
		case "cancelled":
			result.State = "cancelled"
		case "timeout":
			result.State = "timed_out"
		}
		return result
	}
	if ctx == nil {
		return fail("invalid_context")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return fail(contextReason(ctx))
	}
	if !filepath.IsAbs(project) || strings.ContainsFunc(project, unicode.IsControl) {
		return fail("unsafe_path")
	}
	project = filepath.Clean(project)
	before, err := os.Lstat(project)
	if err != nil || !safeFileInfo(before, true) {
		return fail("unsafe_path")
	}
	root, err := os.OpenRoot(project + string(os.PathSeparator) + ".")
	if err != nil {
		return fail("unsafe_path")
	}
	defer root.Close()
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return fail("unsafe_path")
	}
	source, reason := openRegular(ctx, root, spec.ExecutablePath, maxExecutableBytes)
	if reason != "" {
		return fail(reason)
	}
	defer source.Close()
	for _, license := range spec.Licenses {
		file, reason := openRegular(ctx, root, license.Path, maxLicenseBytes)
		if reason != "" {
			if reason == "missing_tool" {
				reason = "missing_license"
			}
			return fail(reason)
		}
		digest, err := hashCopy(ctx, file, io.Discard, maxLicenseBytes)
		_ = file.Close()
		if err != nil {
			return fail(copyReason(ctx))
		}
		if digest != license.SHA256 {
			return fail("license_hash_mismatch")
		}
	}
	info, err := root.Lstat(".testdata")
	if errors.Is(err, fs.ErrNotExist) {
		if err := root.Mkdir(".testdata", 0700); err != nil {
			return fail("temporary_unavailable")
		}
		info, err = root.Lstat(".testdata")
	}
	if err != nil || !safeFileInfo(info, true) {
		return fail("unsafe_path")
	}
	temporary := ".testdata/tool-doctor-" + rand.Text()
	if err := root.Mkdir(temporary, 0700); err != nil {
		return fail("temporary_unavailable")
	}
	defer func() {
		if err := root.RemoveAll(temporary); err != nil {
			result.State = "unavailable"
			result.Reason = "cleanup_failed"
		}
	}()
	tempPath := filepath.Join(project, filepath.FromSlash(temporary))
	if err := makePrivate(tempPath); err != nil {
		return fail("temporary_unavailable")
	}
	name := "ffprobe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	snapshot, err := root.OpenFile(temporary+"/"+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0500)
	if err != nil {
		return fail("temporary_unavailable")
	}
	digest, copyErr := hashCopy(ctx, source, snapshot, maxExecutableBytes)
	closeErr := snapshot.Close()
	if copyErr != nil || closeErr != nil {
		return fail(copyReason(ctx))
	}
	if digest != spec.SHA256 {
		return fail("hash_mismatch")
	}
	if ctx.Err() != nil {
		return fail(contextReason(ctx))
	}
	output, err := execute(ctx, filepath.Join(tempPath, name), tempPath)
	if err != nil {
		return fail(executionReason(ctx, err))
	}
	first, _, _ := strings.Cut(string(output), "\n")
	if !strings.HasPrefix(first, "ffprobe version "+spec.VendorVersion+" ") {
		return fail("version_mismatch")
	}
	result.State, result.Reason = "verified", "verified"
	return result
}

func openRegular(ctx context.Context, root *os.Root, relative string, maximum int64) (*os.File, string) {
	if !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\:") || strings.ContainsFunc(relative, unicode.IsControl) {
		return nil, "unsafe_path"
	}
	parts := strings.Split(relative, "/")
	var leaf os.FileInfo
	for i := range parts {
		if ctx.Err() != nil {
			return nil, contextReason(ctx)
		}
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "missing_tool"
		}
		if err != nil {
			return nil, "unavailable"
		}
		if !safeFileInfo(info, i < len(parts)-1) {
			return nil, "unsafe_path"
		}
		leaf = info
	}
	if leaf.Size() <= 0 || leaf.Size() > maximum {
		return nil, "size_limit"
	}
	file, err := root.OpenFile(filepath.FromSlash(relative), readFlags(), 0)
	if err != nil {
		return nil, "unavailable"
	}
	info, err := file.Stat()
	if err != nil || !safeFileInfo(info, false) || !os.SameFile(leaf, info) {
		file.Close()
		return nil, "unsafe_path"
	}
	return file, ""
}

func hashCopy(ctx context.Context, source io.Reader, dest io.Writer, maximum int64) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := source.Read(buffer[:min(int64(len(buffer)), maximum-total+1)])
		if n > 0 {
			total += int64(n)
			if total > maximum {
				return "", errors.New("identity_size_limit")
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if written, writeErr := dest.Write(buffer[:n]); writeErr != nil {
				return "", writeErr
			} else if written != n {
				return "", io.ErrShortWrite
			}
			_, _ = hash.Write(buffer[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if n == 0 {
			return "", io.ErrNoProgress
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func contextReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}
func copyReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return contextReason(ctx)
	}
	return "read_failed"
}
func executionReason(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return contextReason(ctx)
	}
	switch {
	case errors.Is(err, process.ErrCancelled):
		return "cancelled"
	case errors.Is(err, process.ErrTimeout):
		return "timeout"
	case errors.Is(err, process.ErrOutputLimit):
		return "output_limit"
	default:
		return "execution_failed"
	}
}
func runVersion(ctx context.Context, path, temp string) ([]byte, error) {
	runner, err := process.New(process.Config{MaxConcurrent: 1, Timeout: 10 * time.Second, MaxStdoutBytes: 64 << 10, MaxStderrBytes: 64 << 10, TempRoot: temp}, []process.Tool{{ID: "ffprobe", Path: path, Operations: map[string][]string{"version": {"-version"}}}})
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, process.Request{Tool: "ffprobe", Operation: "version"})
	return result.Stdout, err
}
