package probe

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

var (
	ErrChanged         = errors.New("probe_source_changed")
	ErrFailed          = errors.New("probe_failed")
	ErrToolUnavailable = errors.New("probe_tool_unavailable")
)

const FingerprintVersion = domain.ProbeFingerprintVersion

type executor interface {
	Run(context.Context, process.Request) (process.Result, error)
}

// Adapter holds only an isolated runner; production has no unsandboxed fallback.
type Adapter struct {
	runner   executor
	identity string
}

// Observation is internal candidate data, not a catalog/cache transaction.
// The fingerprint covers size and bounded edge samples, not every media byte.
// Same-inode writes outside those samples with restored mtime can go undetected.
type Observation struct {
	Metadata           domain.MediaMetadata
	File               Metadata
	Fingerprint        string
	FingerprintVersion string
	ToolIdentity       string
}

func NewAdapter(runner *process.IsolatedRunner, identity string) (*Adapter, error) {
	if runner == nil || len(identity) < 1 || len(identity) > 256 || !utf8.ValidString(identity) || strings.ContainsFunc(identity, unicode.IsControl) {
		return nil, ErrInvalidInput
	}
	return &Adapter{runner: runner, identity: identity}, nil
}

// Probe uses one safely opened readonly file. A replaced pathname, changed stat,
// changed edge fingerprint, malformed output, or child failure discards metadata.
// Caller authorization must resolve Source from a registered library first.
func (a *Adapter) Probe(ctx context.Context, source Source) (observation Observation, resultErr error) {
	if a == nil || a.runner == nil || ctx == nil {
		return Observation{}, ErrInvalidInput
	}
	input, err := Open(ctx, source)
	if err != nil {
		return Observation{}, err
	}
	defer func() {
		if err := input.Close(); err != nil && resultErr == nil {
			observation = Observation{}
			resultErr = ErrUnavailable
		}
	}()
	before, err := input.Stdin().Stat()
	if err != nil || !matches(before, input.Metadata()) {
		return Observation{}, inputError(ctx)
	}
	fingerprint, err := edgeFingerprint(ctx, input.Stdin(), before.Size())
	if err != nil {
		return Observation{}, err
	}
	result, err := a.runner.Run(ctx, process.Request{Tool: "ffprobe", Operation: "metadata", Stdin: input.Stdin()})
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err != nil {
		return Observation{}, classifyProcessError(err)
	}
	metadata, err := ParseJSON(result.Stdout)
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err != nil {
		return Observation{}, err
	}
	after, err := input.Stdin().Stat()
	if err != nil {
		return Observation{}, inputError(ctx)
	}
	if !os.SameFile(before, after) || !matches(after, input.Metadata()) {
		return Observation{}, ErrChanged
	}
	afterFingerprint, err := edgeFingerprint(ctx, input.Stdin(), before.Size())
	if err != nil {
		return Observation{}, err
	}
	if afterFingerprint != fingerprint || (metadata.Format.SizeBytes != nil && *metadata.Format.SizeBytes != before.Size()) {
		return Observation{}, ErrChanged
	}
	// Re-resolve the authorized path to reject replacement of the catalog target
	// while ffprobe was reading the original descriptor. Do not probe this handle.
	current, err := Open(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return Observation{}, ctx.Err()
		}
		return Observation{}, ErrChanged
	}
	currentInfo, statErr := current.Stdin().Stat()
	closeErr := current.Close()
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if statErr != nil || closeErr != nil || !os.SameFile(before, currentInfo) || !matches(currentInfo, input.Metadata()) {
		return Observation{}, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	return Observation{Metadata: metadata, File: input.Metadata(), Fingerprint: fingerprint, FingerprintVersion: FingerprintVersion, ToolIdentity: a.identity}, nil
}

func matches(info os.FileInfo, metadata Metadata) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() == metadata.Size && info.ModTime().UnixNano() == metadata.ModifiedUnixNano
}

func classifyProcessError(err error) error {
	switch {
	case errors.Is(err, process.ErrSandboxUnavailable), errors.Is(err, process.ErrSandboxInvalid), errors.Is(err, process.ErrUnsupported), errors.Is(err, process.ErrStart), errors.Is(err, process.ErrCleanup), errors.Is(err, process.ErrInvalid), errors.Is(err, process.ErrUnexpectedExit):
		return ErrToolUnavailable
	case errors.Is(err, process.ErrCancelled), errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, process.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, process.ErrBusy):
		return process.ErrBusy
	case errors.Is(err, process.ErrOutputLimit):
		return process.ErrOutputLimit
	case errors.Is(err, process.ErrExit):
		return ErrFailed
	default:
		// Only the isolated helper's verified media failure is cacheable. An
		// unfamiliar executor failure must not mark healthy media as corrupt.
		return ErrToolUnavailable
	}
}

// EdgeFingerprint is the bounded content fingerprint of a media file: its
// size and at most the first and last 64 KiB, never the whole file. Scans
// reuse it for external subtitle and audio files. The result is hex SHA-256.
func EdgeFingerprint(ctx context.Context, file *os.File, size int64) (string, error) {
	return edgeFingerprint(ctx, file, size)
}

func edgeFingerprint(ctx context.Context, file *os.File, size int64) (string, error) {
	if size < 0 {
		return "", ErrInvalidInput
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(FingerprintVersion))
	var number [8]byte
	binary.LittleEndian.PutUint64(number[:], uint64(size))
	_, _ = hash.Write(number[:])
	buffer := make([]byte, 64<<10)
	end := int64(len(buffer))
	if size < end {
		end = size
	}
	for _, span := range [][2]int64{{0, end}, {max(end, size-int64(len(buffer))), size}} {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		length := span[1] - span[0]
		if length <= 0 {
			continue
		}
		read, err := file.ReadAt(buffer[:length], span[0])
		if int64(read) != length || (err != nil && err != io.EOF) {
			return "", inputError(ctx)
		}
		_, _ = hash.Write(buffer[:read])
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
