// Package probe prepares read-only file descriptors for future isolated media
// probing. It does not start processes or enable media probing.
package probe

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	MaxPathBytes = 1024
	MaxDepth     = 128
)

var (
	ErrInvalidInput = errors.New("probe_invalid_input")
	ErrUnavailable  = errors.New("probe_input_unavailable")
)

// Source is supplied by the authorized catalog resolver, not by HTTP input.
// RootPath is an operator-owned absolute directory. RelativePath is canonical
// slash-separated text beneath that directory.
type Source = domain.ProbeSource

// Metadata describes the opened object before any subprocess uses it. It is
// not an immutable snapshot: another process can still modify the same inode.
type Metadata struct {
	Size             int64
	ModifiedUnixNano int64
}

// Input owns one read-only regular-file descriptor. Use it for one subprocess
// at a time; its seek offset is shared with inherited child descriptors.
type Input struct {
	file       *os.File
	metadata   Metadata
	closeOnce  sync.Once
	closeErr   error
	stopOnce   sync.Once
	stopCancel func() bool
	cancelDone chan struct{}
}

// Open validates and opens the file through os.Root. No file contents are read,
// copied, staged, normalized or changed. Visible symlink components are refused;
// os.Root enforces the root boundary during pathname replacement races.
func Open(ctx context.Context, source Source) (*Input, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validSource(source) {
		return nil, ErrInvalidInput
	}
	if !supportedPlatform() {
		return nil, ErrUnavailable
	}
	file, info, err := openFile(ctx, source)
	if err != nil {
		return nil, err
	}
	input := &Input{
		file:       file,
		metadata:   Metadata{Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()},
		cancelDone: make(chan struct{}),
	}
	input.stopCancel = context.AfterFunc(ctx, func() {
		defer close(input.cancelDone)
		input.closeFile()
	})
	if err := ctx.Err(); err != nil {
		_ = input.Close()
		return nil, err
	}
	return input, nil
}

func validSource(source Source) bool {
	if !filepath.IsAbs(source.RootPath) || !utf8.ValidString(source.RootPath) || hasControl(source.RootPath) {
		return false
	}
	path := source.RelativePath
	return path != "." && len(path) <= MaxPathBytes && strings.Count(path, "/") < MaxDepth &&
		fs.ValidPath(path) && !strings.ContainsAny(path, "\\:") && !hasControl(path) &&
		filepath.IsLocal(filepath.FromSlash(path))
}

func hasControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

// Stdin returns the existing read-only handle for an internal process runner.
// Set Cmd.Stdin directly to this *os.File. Do not reopen Name(), wrap it in an
// io.Reader pipe, pass it in argv, or close it before the child has exited.
// The receiving runner must sanitize any os.File errors and enforce its own
// timeout/cancellation; closing this parent handle cannot kill an inherited one.
func (i *Input) Stdin() *os.File {
	if i == nil {
		return nil
	}
	return i.file
}

// Metadata contains no local pathname or untrusted text.
func (i *Input) Metadata() Metadata {
	if i == nil {
		return Metadata{}
	}
	return i.metadata
}

func (i *Input) closeFile() {
	i.closeOnce.Do(func() {
		if err := i.file.Close(); err != nil {
			i.closeErr = ErrUnavailable
		}
	})
}

// Close releases the descriptor and waits for an already-running cancellation
// callback. It is safe to call concurrently and more than once.
func (i *Input) Close() error {
	if i == nil || i.file == nil {
		return nil
	}
	i.stopOnce.Do(func() {
		if !i.stopCancel() {
			<-i.cancelDone
		}
	})
	i.closeFile()
	return i.closeErr
}

func inputError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}

func openFile(ctx context.Context, source Source) (*os.File, os.FileInfo, error) {
	absolute := filepath.Clean(source.RootPath)
	beforeRoot, err := os.Lstat(absolute)
	if err != nil || beforeRoot.Mode().Type() != os.ModeDir {
		return nil, nil, inputError(ctx)
	}
	// A terminal directory component avoids a blocking FIFO open if the
	// configured directory is replaced on Unix before OpenRoot.
	root, err := os.OpenRoot(absolute + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, nil, inputError(ctx)
	}
	defer root.Close()
	actualRoot, err := root.Stat(".")
	if err != nil || !os.SameFile(beforeRoot, actualRoot) {
		return nil, nil, inputError(ctx)
	}
	components := strings.Split(source.RelativePath, "/")
	prefixes := make([]string, 0, len(components))
	identities := make([]os.FileInfo, 0, len(components))
	prefix := ""
	for index, component := range components {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if prefix != "" {
			prefix += "/"
		}
		prefix += component
		info, err := root.Lstat(filepath.FromSlash(prefix))
		want := os.ModeDir
		if index == len(components)-1 {
			want = 0
		}
		if err != nil || info.Mode().Type() != want {
			return nil, nil, inputError(ctx)
		}
		prefixes = append(prefixes, prefix)
		identities = append(identities, info)
	}
	file, err := root.OpenFile(filepath.FromSlash(source.RelativePath), readFlags(), 0)
	if err != nil {
		return nil, nil, inputError(ctx)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 ||
		!os.SameFile(identities[len(identities)-1], info) ||
		!time.Unix(0, info.ModTime().UnixNano()).Equal(info.ModTime()) {
		return nil, nil, inputError(ctx)
	}
	for index, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		now, err := root.Lstat(filepath.FromSlash(prefix))
		if err != nil || now.Mode().Type() != identities[index].Mode().Type() || !os.SameFile(identities[index], now) {
			return nil, nil, inputError(ctx)
		}
	}
	if offset, err := file.Seek(0, io.SeekStart); err != nil || offset != 0 {
		return nil, nil, inputError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	keep = true
	return file, info, nil
}
