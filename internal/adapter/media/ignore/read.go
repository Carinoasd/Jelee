package ignoresource

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"sync"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

type ownedFile struct {
	sourceFile
	once sync.Once
	err  error
}

func (f *ownedFile) Close() error {
	f.once.Do(func() { f.err = f.sourceFile.Close() })
	return f.err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func readRule(ctx context.Context, parent ruleReader, remaining *int) (stamp sourceStamp, original []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return sourceStamp{}, nil, err
	}
	opened, err := parent.OpenRule()
	if err != nil {
		if errors.Is(err, errAbsent) {
			return sourceStamp{}, nil, ctx.Err()
		}
		return sourceStamp{}, nil, safeError(err)
	}
	file := &ownedFile{sourceFile: opened}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(joined); _ = file.Close() })
	defer func() {
		if !stop() {
			<-joined
		}
		closeErr := file.Close()
		if err := ctx.Err(); err != nil {
			stamp, original, resultErr = sourceStamp{}, nil, err
		} else if closeErr != nil {
			stamp, original, resultErr = sourceStamp{}, nil, ErrRead
		}
	}()
	before, err := file.Stat()
	if err != nil {
		return sourceStamp{}, nil, safeError(err)
	}
	if before.kind != nodeRegular || before.size < 0 {
		return sourceStamp{}, nil, ErrUnsafe
	}
	if before.size > ignore.MaxSourceBytes || before.size > int64(*remaining) {
		return sourceStamp{}, nil, ErrLimit
	}
	// One extra byte distinguishes exact EOF from growth without an unbounded
	// read. Allocation is based on a validated, capped held-handle size.
	buffer := make([]byte, int(before.size)+1)
	n, readErr := io.ReadFull(contextReader{ctx, file}, buffer)
	if err := ctx.Err(); err != nil {
		return sourceStamp{}, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return sourceStamp{}, nil, safeError(err)
	}
	if after != before {
		return sourceStamp{}, nil, ErrChanged
	}
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return sourceStamp{}, nil, ErrRead
	}
	if int64(n) != before.size {
		return sourceStamp{}, nil, ErrChanged
	}
	original = buffer[:n:n]
	digest := sha256.Sum256(original)
	if err := ctx.Err(); err != nil {
		return sourceStamp{}, nil, err
	}
	*remaining -= n
	return sourceStamp{state: before, digest: digest, present: true}, original, nil
}

// Raw operating-system errors and injected port errors must not expose local
// paths, content, host details or wrapped errors through the public boundary.
func safeError(err error) error {
	for _, fixed := range []error{ErrInvalid, ErrRead, ErrUnsafe, ErrUnavailable, ErrChanged, ErrLimit, ErrWorkLimit, ErrBusy, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, fixed) {
			return fixed
		}
	}
	switch {
	case errors.Is(err, ignore.ErrInvalid):
		return ErrInvalid
	case errors.Is(err, ignore.ErrLimit):
		return ErrLimit
	case errors.Is(err, ignore.ErrWorkLimit):
		return ErrWorkLimit
	default:
		return ErrRead
	}
}
