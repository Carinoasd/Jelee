package nfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// SourceFingerprintVersion identifies a full original-byte SHA-256, including
// BOM and original encoding. It is unrelated to a normalized XML representation.
const SourceFingerprintVersion = "sha256-full-v1"

type SourceStamp struct {
	Size               int64
	ModifiedUnixNano   int64
	SHA256             string
	FingerprintVersion string
}

// Source retains bounded original bytes privately. Reading it does not parse
// XML: a future cache can compare Stamp before deciding whether to call Parse.
// Disk observations retain private path and physical identity proofs for later
// writes, without open handles or mutable exported data.
type Source struct {
	original                       []byte
	stamp                          SourceStamp
	ready                          bool
	rootPath, relative             string
	rootInfo, parentInfo, fileInfo os.FileInfo
	maxBytes                       int64
}

func (*Source) String() string   { return "nfo source (data redacted)" }
func (*Source) GoString() string { return "nfo source (data redacted)" }

func (s *Source) Stamp() SourceStamp {
	if s == nil || !s.ready {
		return SourceStamp{}
	}
	return s.stamp
}

// Parse can be called repeatedly or concurrently. Each call creates a separate
// metadata view while retaining the same immutable original bytes.
func (s *Source) Parse(ctx context.Context) (*Document, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || !s.ready {
		return nil, ErrInvalidInput
	}
	return parseOriginal(ctx, s.original)
}

// ReadSource observes one regular file through os.Root, hashes its full bytes,
// checks size/mtime and file identity, then reopens both the configured root and
// relative path to detect observed replacements. Errors never contain paths.
//
// These are observation checkpoints, not a filesystem snapshot. Same-inode
// writes that restore size and mtime during this read can evade the stat checks.
// A later read hashes all bytes and detects changed content even with restored
// timestamps. Open/stat calls on a stalled filesystem are not hard-cancellable;
// cancellation closes the owned reading file and joins its cancellation callback.
func ReadSource(ctx context.Context, rootAbs, relativeSlash string, maxBytes int64) (*Source, error) {
	return readSource(ctx, rootAbs, relativeSlash, maxBytes, sourceAccess{
		statRoot: os.Stat,
		openRoot: func(path string) (sourceRoot, error) {
			root, err := os.OpenRoot(path)
			if err != nil {
				return nil, err
			}
			return diskSourceRoot{root}, nil
		},
	})
}

// Small per-invocation ports permit deterministic read/replace/close tests. No
// hook is global and callers of the exported API cannot replace these ports.
type sourceAccess struct {
	statRoot func(string) (os.FileInfo, error)
	openRoot func(string) (sourceRoot, error)
}
type sourceRoot interface {
	Stat(string) (os.FileInfo, error)
	OpenFile(string, int, os.FileMode) (sourceFile, error)
	Close() error
}
type sourceFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Close() error
}
type diskSourceRoot struct{ *os.Root }

func (r diskSourceRoot) OpenFile(path string, flags int, mode os.FileMode) (sourceFile, error) {
	return r.Root.OpenFile(path, flags, mode)
}

type ownedSourceFile struct {
	sourceFile
	once sync.Once
	err  error
}

func (f *ownedSourceFile) Close() error {
	f.once.Do(func() { f.err = f.sourceFile.Close() })
	return f.err
}

func readSource(ctx context.Context, rootAbs, relativeSlash string, maxBytes int64, access sourceAccess) (result *Source, resultErr error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 1 || maxBytes > MaxAllowedBytes {
		return nil, ErrInvalidInput
	}
	if !filepath.IsAbs(rootAbs) || !utf8.ValidString(rootAbs) || !fs.ValidPath(relativeSlash) || relativeSlash == "." || strings.ContainsAny(relativeSlash, "\\:") || strings.ContainsFunc(relativeSlash, unicode.IsControl) || !filepath.IsLocal(filepath.FromSlash(relativeSlash)) {
		return nil, ErrNotFound
	}
	var resources []io.Closer
	var stop func() bool
	var cancelled chan struct{}
	defer func() {
		if stop != nil && !stop() {
			<-cancelled
		}
		closeFailed := false
		for i := len(resources) - 1; i >= 0; i-- {
			if resources[i].Close() != nil {
				closeFailed = true
			}
		}
		if err := ctx.Err(); err != nil {
			result, resultErr = nil, err
		} else if closeFailed {
			result, resultErr = nil, ErrRead
		}
	}()
	rootPath := filepath.Clean(rootAbs)
	// The final directory component also prevents opening a swapped FIFO root.
	root, err := access.openRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, ErrNotFound
	}
	resources = append(resources, root)
	// Identity starts at the held root handle. In particular, Windows path
	// Stat may defer loading its file ID until a later SameFile call; Root.Stat
	// obtains it from the already-open handle instead.
	rootInfo, err := root.Stat(".")
	if err != nil || rootInfo == nil || !rootInfo.IsDir() {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	relative := filepath.FromSlash(relativeSlash)
	parentInfo, err := root.Stat(filepath.Dir(relative))
	if err != nil || !parentInfo.IsDir() {
		return nil, ErrNotFound
	}
	opened, err := root.OpenFile(relative, readOnlyFlags(), 0)
	if err != nil {
		return nil, ErrNotFound
	}
	file := &ownedSourceFile{sourceFile: opened}
	resources = append(resources, file)
	cancelled = make(chan struct{})
	stop = context.AfterFunc(ctx, func() { defer close(cancelled); _ = file.Close() })
	before, err := file.Stat()
	if err != nil || !validSourceInfo(before) {
		return nil, ErrNotFound
	}
	if before.Size() > maxBytes {
		return nil, ErrTooLarge
	}
	original, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, file}, maxBytes+1))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, ErrRead
	}
	if !sameSourceInfo(before, after) {
		return nil, ErrChanged
	}
	if readErr != nil {
		return nil, ErrRead
	}
	if int64(len(original)) != before.Size() {
		return nil, ErrChanged
	}
	currentRoot, err := access.openRoot(rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, ErrChanged
	}
	resources = append(resources, currentRoot)
	currentRootInfo, err := currentRoot.Stat(".")
	if err != nil || !currentRootInfo.IsDir() || !os.SameFile(rootInfo, currentRootInfo) {
		return nil, ErrChanged
	}
	currentParentInfo, err := currentRoot.Stat(filepath.Dir(relative))
	if err != nil || !currentParentInfo.IsDir() || !os.SameFile(parentInfo, currentParentInfo) {
		return nil, ErrChanged
	}
	currentFile, err := currentRoot.OpenFile(relative, readOnlyFlags(), 0)
	if err != nil {
		return nil, ErrChanged
	}
	resources = append(resources, currentFile)
	currentInfo, err := currentFile.Stat()
	if err != nil || !sameSourceInfo(before, currentInfo) {
		return nil, ErrChanged
	}
	currentPathRoot, err := access.statRoot(rootPath)
	if err != nil || !currentPathRoot.IsDir() || !os.SameFile(rootInfo, currentPathRoot) {
		return nil, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(original)
	return &Source{original: original, ready: true, rootPath: rootPath, relative: relativeSlash,
		rootInfo: rootInfo, parentInfo: parentInfo, fileInfo: before, maxBytes: maxBytes, stamp: SourceStamp{
			Size: before.Size(), ModifiedUnixNano: before.ModTime().UnixNano(),
			SHA256: hex.EncodeToString(hash[:]), FingerprintVersion: SourceFingerprintVersion,
		}}, nil
}

func validSourceInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() >= 0 && time.Unix(0, info.ModTime().UnixNano()).Equal(info.ModTime())
}

func sameSourceInfo(before, after os.FileInfo) bool {
	return validSourceInfo(after) && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
