package probe

import (
	"context"
	"os"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Inspect reads at most 128 KiB of file edges through a safely opened readonly
// descriptor. It starts no process. An error always returns an empty stamp.
// Stat/edge/stat and re-opening the registered path detect observable changes
// during this call. The stamp is a quick key, not a content snapshot: replacing
// an object between calls with identical size, mtime and edges produces the same
// key; same-inode middle writes with restored mtime can also go undetected.
func Inspect(ctx context.Context, source Source) (stamp domain.ProbeStamp, resultErr error) {
	input, err := Open(ctx, source)
	if err != nil {
		return domain.ProbeStamp{}, err
	}
	defer func() {
		if err := input.Close(); err != nil && resultErr == nil {
			stamp = domain.ProbeStamp{}
			resultErr = ErrUnavailable
		}
	}()
	return inspectOpened(ctx, source, input)
}

// Inspect does not depend on runner availability; worker policy decides whether
// a cache phase may begin before making any calls to Inspect or Probe.
func (a *Adapter) Inspect(ctx context.Context, source Source) (domain.ProbeStamp, error) {
	if a == nil {
		return domain.ProbeStamp{}, ErrInvalidInput
	}
	return Inspect(ctx, source)
}

func inspectOpened(ctx context.Context, source Source, input *Input) (domain.ProbeStamp, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProbeStamp{}, err
	}
	before, err := input.Stdin().Stat()
	if err != nil {
		return domain.ProbeStamp{}, inputError(ctx)
	}
	if !matches(before, input.Metadata()) {
		return domain.ProbeStamp{}, ErrChanged
	}
	fingerprint, err := edgeFingerprint(ctx, input.Stdin(), before.Size())
	if err != nil {
		return domain.ProbeStamp{}, err
	}
	after, err := input.Stdin().Stat()
	if err != nil {
		return domain.ProbeStamp{}, inputError(ctx)
	}
	if !os.SameFile(before, after) || !matches(after, input.Metadata()) {
		return domain.ProbeStamp{}, ErrChanged
	}
	current, err := Open(ctx, source)
	if err != nil {
		if err := ctx.Err(); err != nil {
			return domain.ProbeStamp{}, err
		}
		return domain.ProbeStamp{}, ErrChanged
	}
	currentInfo, statErr := current.Stdin().Stat()
	closeErr := current.Close()
	if err := ctx.Err(); err != nil {
		return domain.ProbeStamp{}, err
	}
	if statErr != nil || closeErr != nil || !os.SameFile(before, currentInfo) || !matches(currentInfo, input.Metadata()) {
		return domain.ProbeStamp{}, ErrChanged
	}
	return domain.ProbeStamp{Size: before.Size(), ModifiedUnixNano: before.ModTime().UnixNano(), Fingerprint: fingerprint, FingerprintVersion: FingerprintVersion}, nil
}
