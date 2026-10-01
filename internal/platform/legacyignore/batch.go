package legacyignore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"
)

// BatchVersion pins the private helper wire format, not the source syntax.
const BatchVersion = "JIG2"
const (
	MaxBatchSourceBytes = 384 << 10
	MaxBatchPaths       = 128
	MaxBatchPathBytes   = 4096
	MaxBatchBytes       = 1 << 20
)

var ErrBatch = errors.New("legacy_ignore_invalid_batch")

// Batch contains decoded source text and upstream-style full paths. It grants
// no filesystem access; lookup, decoding, root bounds and no-follow belong to
// the caller. A helper must evaluate the whole batch under a resource limit.
// Strings and slice storage are owned by the caller and must remain immutable
// during encoding. Never log the wire representation.
type Batch struct {
	Source string
	Paths  []string
}

func (Batch) String() string   { return "legacy ignore batch (data redacted)" }
func (Batch) GoString() string { return "legacy ignore batch (data redacted)" }

func batchStringValid(s string, max int, empty bool) bool {
	return (empty || len(s) > 0) && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// Rule text may contain literal NUL, as accepted by the pinned text reader and
// regex library. Paths remain NUL-free; source is never used as a path or argv.
func batchSourceValid(s string) bool { return len(s) <= MaxBatchSourceBytes && utf8.ValidString(s) }

// EncodeBatch creates one bounded frame. No executable, argv, environment,
// resource-limit override or filename-to-open can be supplied by this frame.
func EncodeBatch(ctx context.Context, batch Batch) ([]byte, error) {
	if ctx == nil {
		return nil, ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !batchSourceValid(batch.Source) || len(batch.Paths) < 1 || len(batch.Paths) > MaxBatchPaths {
		return nil, ErrBatch
	}
	size := len(BatchVersion) + 4 + len(batch.Source) + 4
	for _, path := range batch.Paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !batchStringValid(path, MaxBatchPathBytes, false) {
			return nil, ErrBatch
		}
		size += 4 + len(path)
	}
	if size > MaxBatchBytes {
		return nil, ErrBatch
	}
	out := make([]byte, 0, size)
	out = append(out, BatchVersion...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(batch.Source)))
	out = append(out, batch.Source...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(batch.Paths)))
	for _, path := range batch.Paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = binary.LittleEndian.AppendUint32(out, uint32(len(path)))
		out = append(out, path...)
	}
	return out, nil
}

// DecodeBatch validates every count before allocating and rejects trailing or
// concatenated frames. It copies strings, so mutation of data cannot alter the
// validated request. The caller must bound any I/O before passing data here.
func DecodeBatch(ctx context.Context, data []byte) (Batch, error) {
	if ctx == nil {
		return Batch{}, ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return Batch{}, err
	}
	if len(data) > MaxBatchBytes || len(data) < 12 || !bytes.Equal(data[:4], []byte(BatchVersion)) {
		return Batch{}, ErrBatch
	}
	remaining := data[4:]
	readString := func(max int, source bool) (string, bool) {
		if len(remaining) < 4 {
			return "", false
		}
		count := uint64(binary.LittleEndian.Uint32(remaining))
		remaining = remaining[4:]
		if count > uint64(max) || count > uint64(len(remaining)) {
			return "", false
		}
		s := string(remaining[:int(count)])
		remaining = remaining[int(count):]
		if source {
			return s, batchSourceValid(s)
		}
		return s, batchStringValid(s, max, false)
	}
	source, ok := readString(MaxBatchSourceBytes, true)
	if !ok || len(remaining) < 4 {
		return Batch{}, ErrBatch
	}
	count := binary.LittleEndian.Uint32(remaining)
	remaining = remaining[4:]
	if count < 1 || count > MaxBatchPaths {
		return Batch{}, ErrBatch
	}
	paths := make([]string, 0, int(count))
	for i := uint32(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return Batch{}, err
		}
		path, ok := readString(MaxBatchPathBytes, false)
		if !ok {
			return Batch{}, ErrBatch
		}
		paths = append(paths, path)
	}
	if len(remaining) != 0 {
		return Batch{}, ErrBatch
	}
	return Batch{Source: source, Paths: paths}, nil
}
