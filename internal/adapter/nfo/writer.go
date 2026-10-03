package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// Writer coalesces identical in-flight intents. It does not grant library
// read-write policy, item authorization or execution authority to callers.
// Runtime must share one instance when connecting the application writer.
type Writer struct {
	group    singleflight.Group
	mu       sync.Mutex
	intents  map[string][]*writerIntent
	sequence uint64
}

type writerIntent struct {
	source     *Source
	key        string
	references int
}

func (w *Writer) acquireIntent(base string, source *Source) (string, func()) {
	w.mu.Lock()
	if w.intents == nil {
		w.intents = make(map[string][]*writerIntent)
	}
	var selected *writerIntent
	for _, active := range w.intents[base] {
		if os.SameFile(active.source.rootInfo, source.rootInfo) && os.SameFile(active.source.parentInfo, source.parentInfo) && os.SameFile(active.source.fileInfo, source.fileInfo) {
			selected = active
			break
		}
	}
	if selected == nil {
		w.sequence++
		selected = &writerIntent{source: source, key: base + ":" + strconv.FormatUint(w.sequence, 16)}
		w.intents[base] = append(w.intents[base], selected)
	}
	selected.references++
	w.mu.Unlock()
	return selected.key, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		selected.references--
		if selected.references != 0 {
			return
		}
		active := w.intents[base]
		for i, intent := range active {
			if intent == selected {
				active = append(active[:i], active[i+1:]...)
				break
			}
		}
		if len(active) == 0 {
			delete(w.intents, base)
		} else {
			w.intents[base] = active
		}
	}
}

func (*Writer) String() string   { return "nfo writer (data redacted)" }
func (*Writer) GoString() string { return "nfo writer (data redacted)" }

// Replace uses only the disk binding and private bytes of a ReadSource result.
// The first caller owns the shared operation context. Other callers may cancel
// their wait without closing handles or cancelling that operation. If the owner
// cancels, all sharing callers receive its failure and may retry with a fresh
// observation. Calls arriving after completion revalidate the original source.
func (w *Writer) Replace(ctx context.Context, original *Source, replacement *Document, backups int) error {
	return w.replace(ctx, original, replacement, backups, nativeNFOWriteOperations())
}

func (w *Writer) replace(ctx context.Context, original *Source, replacement *Document, backups int, ops nfoWriteOperations) error {
	if w == nil || ctx == nil || original == nil || !original.ready || original.rootInfo == nil || original.parentInfo == nil || original.fileInfo == nil || replacement == nil || backups < 0 || backups > 16 || original.maxBytes < 1 || int64(len(replacement.original)) > original.maxBytes {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !bytes.Equal(original.original, replacement.original) && (!replacement.edited || replacement.editBaseHash != sha256.Sum256(original.original)) {
		return ErrInvalidInput
	}
	// Structured encoding avoids ambiguous concatenation and keeps paths/content
	// out of group keys. Different content, original stamps and options never join.
	newHash := sha256.Sum256(replacement.original)
	keyBytes, err := json.Marshal(struct {
		Root, Relative string
		Stamp          SourceStamp
		Replacement    [32]byte
		Backups        int
		MaxBytes       int64
	}{original.rootPath, original.relative, original.stamp, newHash, backups, original.maxBytes})
	if err != nil {
		return ErrInvalidInput
	}
	keyHash := sha256.Sum256(keyBytes)
	key, releaseIntent := w.acquireIntent(hex.EncodeToString(keyHash[:]), original)
	defer releaseIntent()
	var ownsOperation atomic.Bool
	result := w.group.DoChan(key, func() (any, error) {
		ownsOperation.Store(true)
		return nil, writeBoundNFOSource(ctx, original, replacement, backups, ops)
	})
	if ops.submitted != nil {
		ops.submitted()
	}
	select {
	case <-ctx.Done():
		if ownsOperation.Load() {
			return (<-result).Err
		}
		return ctx.Err()
	case done := <-result:
		return done.Err
	}
}

func writeBoundNFOSource(ctx context.Context, original *Source, replacement *Document, backups int, ops nfoWriteOperations) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(original.rootPath + string(os.PathSeparator) + ".")
	if err != nil {
		return ErrChanged
	}
	defer root.Close()
	parent := filepath.Dir(filepath.FromSlash(original.relative))
	// Do not follow even an in-root symlink when preparing a filesystem write.
	info, err := os.Lstat(original.rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrChanged
	}
	components := strings.Split(original.relative, "/")
	for i := range components {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(components[:i+1], "/")))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(components)-1 && !info.IsDir() || i == len(components)-1 && !regularNFOFile(info) {
			return ErrChanged
		}
	}
	directory, err := root.OpenRoot(parent)
	if err != nil {
		return ErrChanged
	}
	defer directory.Close()
	filename := filepath.Base(filepath.FromSlash(original.relative))
	check := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		rootInfo, err := root.Stat(".")
		if err != nil || !os.SameFile(original.rootInfo, rootInfo) {
			return ErrChanged
		}
		dirInfo, err := directory.Stat(".")
		if err != nil || !os.SameFile(original.parentInfo, dirInfo) {
			return ErrChanged
		}
		currentRoot, err := os.OpenRoot(original.rootPath + string(os.PathSeparator) + ".")
		if err != nil {
			return ErrChanged
		}
		defer currentRoot.Close()
		currentInfo, err := currentRoot.Stat(".")
		if err != nil || !os.SameFile(original.rootInfo, currentInfo) {
			return ErrChanged
		}
		pathRootInfo, err := os.Lstat(original.rootPath)
		if err != nil || !pathRootInfo.IsDir() || pathRootInfo.Mode()&os.ModeSymlink != 0 {
			return ErrChanged
		}
		for i := range components {
			info, err := currentRoot.Lstat(filepath.FromSlash(strings.Join(components[:i+1], "/")))
			if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(components)-1 && !info.IsDir() || i == len(components)-1 && !regularNFOFile(info) {
				return ErrChanged
			}
		}
		currentParent, err := currentRoot.Stat(parent)
		if err != nil || !os.SameFile(original.parentInfo, currentParent) {
			return ErrChanged
		}
		_, err = verifyNFOOriginal(checkCtx, directory, filename, original.original, original.fileInfo)
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	ops.checkSource = check
	base, err := original.Parse(ctx)
	if err != nil {
		return err
	}
	return replaceNFODocumentWithOperations(ctx, directory, filename, base, replacement, backups, ops)
}
