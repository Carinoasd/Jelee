// Package ocrruntime registers the shipped, isolated Tesseract helper for
// subtitle OCR (G15.6). Locations and digests come only from the embedded
// dependency manifest; a missing or altered file disables OCR alone. There
// is no unsandboxed fallback: the runtime reads one decoded subtitle picture
// at a time and never touches the original media.
package ocrruntime

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

	"github.com/MoYuanCN/Jelee/internal/platform/mkvruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

// ErrUnavailable is the fixed failure of every registration step.
var ErrUnavailable = errors.New("ocr_runtime_unavailable")

// MaxConcurrency bounds simultaneous Tesseract children of one instance.
const MaxConcurrency = 4

// Registration returns the shipped profile and policy of the OCR mode. It
// reads only embedded data and never consults the filesystem, a descriptor
// or a request. Every pinned language is granted to the policy; each run
// still receives only the data files of its own languages.
func Registration() (sandbox.ToolProfile, sandbox.ToolPolicy, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, ErrUnavailable
	}
	return registration()
}

func registration() (sandbox.ToolProfile, sandbox.ToolPolicy, error) {
	spec, err := tools.OCRToolSpec("linux-amd64")
	if err != nil || spec.Executable.ContainerPath == "" {
		return sandbox.ToolProfile{}, sandbox.ToolPolicy{}, ErrUnavailable
	}
	policy := sandbox.ToolPolicy{ExecutableSHA256: spec.Executable.SHA256, RequireProtectedFiles: true}
	for _, library := range spec.Libraries {
		policy.Libraries = append(policy.Libraries, sandbox.PinnedFile{Path: library.ContainerPath, SHA256: library.SHA256})
	}
	for _, code := range tools.OCRLanguages {
		file := spec.Languages[code]
		policy.DataFiles = append(policy.DataFiles, sandbox.PinnedFile{Path: file.ContainerPath, SHA256: file.SHA256})
	}
	return sandbox.ToolProfile{Mode: sandbox.ToolOCR, Path: spec.Executable.ContainerPath}, policy, nil
}

// ToolHelper dispatches --internal-media-tool-helper for every shipped tool
// mode: the OCR mode here, the Matroska and MediaInfo modes through
// mkvruntime. It must run before service configuration, DB access or Fx;
// the caller immediately exits with the returned code.
func ToolHelper(argv []string) int {
	return sandbox.RunToolHelper(argv, func(mode sandbox.ToolMode) (sandbox.ToolProfile, sandbox.ToolPolicy, bool) {
		if mode == sandbox.ToolOCR {
			profile, policy, err := Registration()
			return profile, policy, err == nil
		}
		profile, policy, err := mkvruntime.Registration(mode)
		return profile, policy, err == nil
	})
}

// config bounds one runner: each run recognizes one picture, prints at most
// a few lines of text and is killed after a minute (the sandbox also caps
// CPU at 30 seconds).
func config(concurrency int, tempRoot string) process.Config {
	return process.Config{MaxConcurrent: concurrency, Timeout: time.Minute, MaxStdoutBytes: 64 << 10, MaxStderrBytes: 64 << 10, TempRoot: tempRoot}
}

// New registers the OCR mode with 1..MaxConcurrency simultaneous runs.
// tempRoot is a caller-owned private scratch directory; the runner creates
// one private working directory per run.
func New(ctx context.Context, concurrency int, tempRoot string) (*process.IsolatedToolRunner, error) {
	if concurrency < 1 || concurrency > MaxConcurrency {
		return nil, ErrUnavailable
	}
	profile, policy, err := Registration()
	if err != nil {
		return nil, ErrUnavailable
	}
	launcher, err := sandbox.NewTool(ctx, profile, policy)
	if err != nil {
		return nil, ErrUnavailable
	}
	runner, err := process.NewIsolatedTool(config(concurrency, tempRoot), launcher)
	if err != nil {
		return nil, ErrUnavailable
	}
	return runner, nil
}

// Identity describes the recognizer for the OCR cache: the executable,
// closure and language data digests, the argv digest and the selected
// languages. A cached result made by any other recognizer never matches.
func Identity(languages []string) (string, error) {
	spec, err := tools.OCRToolSpec("linux-amd64")
	if err != nil || len(languages) == 0 {
		return "", ErrUnavailable
	}
	files := slices.Concat([]tools.RuntimeFile{spec.Executable}, spec.Libraries)
	for _, code := range languages {
		file, ok := spec.Languages[code]
		if !ok {
			return "", ErrUnavailable
		}
		files = append(files, file)
	}
	slices.SortStableFunc(files[1:], func(a, b tools.RuntimeFile) int { return strings.Compare(a.ContainerPath, b.ContainerPath) })
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-ocr-recognizer-v1\x00" + spec.DebianVersion + "\x00" + sandbox.ToolArgumentsDigest(sandbox.ToolOCR) + "\x00" + strings.Join(languages, "+") + "\x00"))
	for _, file := range files {
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 {
			return "", ErrUnavailable
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(file.ContainerPath)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(file.ContainerPath))
		_, _ = hash.Write(digest)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ToolStatus is a fixed diagnostic classification of the recognizer or one
// language data file.
type ToolStatus struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	State   string `json:"state"`
}

// Diagnose classifies the shipped executable (with its exact closure and
// all language data) without executing it: verified, missing, invalid or
// platform_unsupported; then each language data file: verified, missing
// or invalid.
func Diagnose(ctx context.Context) []ToolStatus {
	status := ToolStatus{Name: "tesseract", State: "platform_unsupported"}
	spec, specErr := tools.OCRToolSpec("linux-amd64")
	if specErr == nil {
		status.Version = spec.Version
	}
	profile, policy, err := Registration()
	if err != nil {
		return []ToolStatus{status}
	}
	status.State = "verified"
	if _, statErr := os.Lstat(profile.Path); errors.Is(statErr, os.ErrNotExist) {
		status.State = "missing"
	} else if _, err := sandbox.NewTool(ctx, profile, policy); err != nil {
		status.State = "invalid"
	}
	result := []ToolStatus{status}
	for _, file := range policy.DataFiles {
		name := strings.TrimSuffix(file.Path[strings.LastIndexByte(file.Path, '/')+1:], sandbox.OCRDataSuffix)
		language := ToolStatus{Name: "tessdata/" + name, Version: spec.Version, State: "verified"}
		if _, statErr := os.Lstat(file.Path); errors.Is(statErr, os.ErrNotExist) {
			language.State = "missing"
		} else if digest, err := fileDigest(file.Path); err != nil || digest != file.SHA256 {
			language.State = "invalid"
		}
		result = append(result, language)
	}
	return result
}

// fileDigest hashes one regular file of at most 64 MiB without following
// a final symbolic link.
func fileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return "", ErrUnavailable
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: fixed manifest path
	if err != nil {
		return "", ErrUnavailable
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
