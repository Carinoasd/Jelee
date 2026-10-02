package images

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const imageDirectoryEntries = 10000
const imageDirectoryNameBytes = 4 << 20

type imageObservation struct {
	root, parent, media, image os.FileInfo
	selected                   string
	matches                    [6]string
}

// stagedImage owns a private file, independent of later changes to the original.
// It is not safe to read while closing; the processor owns that sequencing.
type stagedImage struct {
	ctx        context.Context
	source     domain.LocalImageSource
	observed   imageObservation
	temp       *os.Root
	file       *os.File
	name       string
	size       int64
	content    [32]byte
	key        [32]byte
	closeOnce  sync.Once
	closeError error
}

func (*stagedImage) String() string   { return "staged image (redacted)" }
func (*stagedImage) GoString() string { return "staged image (redacted)" }

func stageLocalPrimary(ctx context.Context, source domain.LocalImageSource, tempRoot string, maxSourceBytes int64) (result *stagedImage, resultErr error) {
	if ctx == nil || maxSourceBytes < 1 || maxSourceBytes > 64<<20 || !validImageSource(source) {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	temp, err := imageTempRoot(source.RootPath, tempRoot)
	if err != nil {
		return nil, imageSourceError(ctx, err)
	}
	staged := &stagedImage{ctx: ctx, source: source, temp: temp}
	defer func() {
		if resultErr != nil {
			if staged.Close() != nil && ctx.Err() == nil {
				resultErr = domain.ErrImageUnavailable
			}
			result = nil
		}
	}()
	observed, err := observeImageSource(ctx, source)
	if err != nil {
		return nil, err
	}
	if observed.image.Size() <= 0 || observed.image.Size() > maxSourceBytes {
		return nil, domain.ErrImageTooLarge
	}
	staged.observed, staged.size = observed, observed.image.Size()
	input, err := probe.Open(ctx, domain.ProbeSource{RootPath: source.RootPath, RelativePath: observed.selected})
	if err != nil {
		return nil, imageSourceError(ctx, err)
	}
	defer func() {
		if input.Close() != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
			_ = staged.Close()
			result = nil
		}
	}()
	info, err := input.Stdin().Stat()
	if err != nil || !sameImageFile(observed.image, info) {
		return nil, imageSourceError(ctx, err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, domain.ErrImageUnavailable
	}
	staged.name = "image-" + hex.EncodeToString(nonce[:]) + ".partial"
	writer, err := temp.OpenFile(staged.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		staged.name = "" // An existing file is never owned by this invocation.
		return nil, domain.ErrImageUnavailable
	}
	writtenInfo, statErr := writer.Stat()
	if statErr != nil || privateImageObject(writer, false) != nil {
		_ = writer.Close()
		return nil, domain.ErrImageUnavailable
	}
	digest := sha256.New()
	count, copyErr := copyImageBytes(ctx, io.MultiWriter(writer, digest), input.Stdin(), maxSourceBytes)
	closeErr := writer.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil || count != staged.size {
		return nil, domain.ErrImageUnavailable
	}
	copy(staged.content[:], digest.Sum(nil))
	staged.file, err = temp.Open(staged.name)
	if err != nil {
		return nil, domain.ErrImageUnavailable
	}
	openedInfo, statErr := staged.file.Stat()
	if statErr != nil || !os.SameFile(writtenInfo, openedInfo) || privateImageObject(staged.file, false) != nil {
		return nil, domain.ErrImageUnavailable
	}
	// This also hashes the current source again. A writer that restores mtime
	// cannot silently change bytes while the original is being staged.
	if err := staged.Verify(ctx); err != nil {
		return nil, err
	}
	key := sha256.New()
	for _, value := range []string{"local-primary-v1", source.ItemID, source.LibraryID, source.SourceID, source.RootID, filepath.Clean(source.RootPath), source.MediaPath, observed.selected} {
		imageHashString(key, value)
	}
	var numbers [16]byte
	binary.BigEndian.PutUint64(numbers[:8], uint64(staged.size))
	binary.BigEndian.PutUint64(numbers[8:], uint64(observed.image.ModTime().UnixNano()))
	_, _ = key.Write(numbers[:])
	_, _ = key.Write(staged.content[:])
	copy(staged.key[:], key.Sum(nil))
	return staged, nil
}

func (s *stagedImage) Reader() io.ReadSeeker { return imageContextFile{s.ctx, s.file} }
func (s *stagedImage) Size() int64           { return s.size }
func (s *stagedImage) Key() [32]byte         { return s.key }

func (s *stagedImage) Verify(ctx context.Context) error {
	if ctx == nil || s == nil || s.file == nil {
		return domain.ErrInvalid
	}
	current, err := observeImageSource(ctx, s.source)
	if err != nil {
		return imageSourceError(ctx, err)
	}
	if !sameImageObservation(s.observed, current) {
		return domain.ErrImageUnavailable
	}
	input, err := probe.Open(ctx, domain.ProbeSource{RootPath: s.source.RootPath, RelativePath: current.selected})
	if err != nil {
		return imageSourceError(ctx, err)
	}
	before, statErr := input.Stdin().Stat()
	digest := sha256.New()
	var count int64
	if statErr == nil && sameImageFile(s.observed.image, before) {
		count, err = copyImageBytes(ctx, digest, input.Stdin(), s.size)
	} else {
		err = domain.ErrImageUnavailable
	}
	after, afterErr := input.Stdin().Stat()
	closeErr := input.Close()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var got [32]byte
	copy(got[:], digest.Sum(nil))
	if err != nil || afterErr != nil || closeErr != nil || count != s.size || got != s.content || !sameImageFile(before, after) {
		return domain.ErrImageUnavailable
	}
	last, err := observeImageSource(ctx, s.source)
	if err != nil || !sameImageObservation(s.observed, last) {
		return imageSourceError(ctx, err)
	}
	return ctx.Err()
}

func (s *stagedImage) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.file != nil && s.file.Close() != nil {
			s.closeError = domain.ErrImageUnavailable
		}
		if s.temp != nil {
			if s.name != "" && s.temp.Remove(s.name) != nil {
				s.closeError = domain.ErrImageUnavailable
			}
			if s.temp.Close() != nil {
				s.closeError = domain.ErrImageUnavailable
			}
		}
	})
	return s.closeError
}

type imageContextFile struct {
	ctx  context.Context
	file *os.File
}

func (f imageContextFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := f.file.Read(p)
	if err != nil && err != io.EOF {
		err = imageSourceError(f.ctx, err)
	}
	return n, err
}
func (f imageContextFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := f.file.Seek(offset, whence)
	if err != nil {
		err = imageSourceError(f.ctx, err)
	}
	return n, err
}

func copyImageBytes(ctx context.Context, out io.Writer, input *os.File, limit int64) (int64, error) {
	buffer := make([]byte, 32<<10)
	n, err := io.CopyBuffer(out, io.LimitReader(imageContextFile{ctx, input}, limit+1), buffer)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	if n > limit {
		return n, domain.ErrImageTooLarge
	}
	if err != nil {
		return n, domain.ErrImageUnavailable
	}
	return n, nil
}

func validImageSource(source domain.LocalImageSource) bool {
	return domain.ValidID(source.ItemID) && domain.ValidID(source.LibraryID) && domain.ValidID(source.SourceID) && domain.ValidID(source.RootID) &&
		filepath.IsAbs(source.RootPath) && len(source.RootPath) <= 4096 && utf8.ValidString(source.RootPath) && !strings.ContainsFunc(source.RootPath, unicode.IsControl) &&
		len(source.MediaPath) <= probe.MaxPathBytes && strings.Count(source.MediaPath, "/") < probe.MaxDepth && fs.ValidPath(source.MediaPath) &&
		!strings.ContainsAny(source.MediaPath, "\\:") && !strings.ContainsFunc(source.MediaPath, unicode.IsControl) && domain.ImportVideoContentType(source.MediaPath) != ""
}

func imageSourceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, domain.ErrImageTooLarge) {
		return domain.ErrImageTooLarge
	}
	return domain.ErrImageUnavailable
}

func imageHashString(h hash.Hash, text string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(text)))
	_, _ = h.Write(length[:])
	_, _ = io.WriteString(h, text)
}

func imageTempRoot(mediaRoot, tempRoot string) (*os.Root, error) {
	if !filepath.IsAbs(tempRoot) || len(tempRoot) > 4096 || !utf8.ValidString(tempRoot) || strings.ContainsFunc(tempRoot, unicode.IsControl) {
		return nil, domain.ErrImageUnavailable
	}
	mediaReal, err := filepath.EvalSymlinks(mediaRoot)
	if err != nil {
		return nil, domain.ErrImageUnavailable
	}
	tempReal, err := filepath.EvalSymlinks(tempRoot)
	if err != nil || imagePathInside(mediaReal, tempReal) || imagePathInside(tempReal, mediaReal) {
		return nil, domain.ErrImageUnavailable
	}
	// Reject aliases for the configured scratch directory, including ancestors.
	if !imageSamePath(tempReal, filepath.Clean(tempRoot)) {
		return nil, domain.ErrImageUnavailable
	}
	before, err := os.Lstat(tempRoot)
	if err != nil || before.Mode().Type() != os.ModeDir {
		return nil, domain.ErrImageUnavailable
	}
	root, err := os.OpenRoot(filepath.Clean(tempRoot) + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, domain.ErrImageUnavailable
	}
	// Security and identity come from the same held object. Windows Lstat
	// FileInfo can lazily resolve its file ID later, after a pathname swap.
	held, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, domain.ErrImageUnavailable
	}
	privateErr := privateImageObject(held, true)
	closeErr := held.Close()
	if privateErr != nil || closeErr != nil {
		_ = root.Close()
		return nil, domain.ErrImageUnavailable
	}
	return root, nil
}

func imageSamePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func imagePathInside(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func observeImageSource(ctx context.Context, source domain.LocalImageSource) (value imageObservation, resultErr error) {
	if ctx.Err() != nil {
		return value, ctx.Err()
	}
	anchor, err := probe.Open(ctx, domain.ProbeSource{RootPath: source.RootPath, RelativePath: source.MediaPath})
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	defer func() {
		if anchor.Close() != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
		}
	}()
	value.media, err = anchor.Stdin().Stat()
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	root, err := os.OpenRoot(filepath.Clean(source.RootPath) + string(os.PathSeparator) + ".")
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	defer func() {
		if root.Close() != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
		}
	}()
	value.root, err = root.Stat(".")
	if err != nil || !value.root.IsDir() {
		return value, imageSourceError(ctx, err)
	}
	for i, parts := 0, strings.Split(source.MediaPath, "/"); i < len(parts); i++ {
		info, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() {
			return value, imageSourceError(ctx, err)
		}
	}
	parentPath := path.Dir(source.MediaPath)
	directory, err := root.OpenRoot(filepath.FromSlash(parentPath))
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	defer func() {
		if directory.Close() != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
		}
	}()
	listing, err := directory.Open(".")
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	var once sync.Once
	var closeErr error
	closeListing := func() { once.Do(func() { closeErr = listing.Close() }) }
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); closeListing() })
	defer func() {
		if !stop() {
			<-done
		}
		closeListing()
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		} else if closeErr != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
		}
	}()
	value.parent, err = listing.Stat()
	if err != nil || !value.parent.IsDir() {
		return value, imageSourceError(ctx, err)
	}
	base := strings.TrimSuffix(path.Base(source.MediaPath), path.Ext(source.MediaPath))
	candidates := [6]string{base + "-poster.jpg", base + "-poster.jpeg", base + "-poster.png", "poster.jpg", "poster.jpeg", "poster.png"}
	entries, nameBytes := 0, 0
	for {
		if ctx.Err() != nil {
			return value, ctx.Err()
		}
		batch, readErr := listing.ReadDir(128)
		entries += len(batch)
		if entries > imageDirectoryEntries {
			return value, domain.ErrImageTooLarge
		}
		for _, entry := range batch {
			nameBytes += len(entry.Name())
			if nameBytes > imageDirectoryNameBytes {
				return value, domain.ErrImageTooLarge
			}
			for i, candidate := range candidates {
				if !strings.EqualFold(entry.Name(), candidate) {
					continue
				}
				info, statErr := directory.Lstat(entry.Name())
				if statErr != nil || !info.Mode().IsRegular() || value.matches[i] != "" {
					return value, imageSourceError(ctx, statErr)
				}
				value.matches[i] = path.Join(parentPath, entry.Name())
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return value, imageSourceError(ctx, readErr)
		}
	}
	for _, match := range value.matches {
		if match != "" {
			value.selected = match
			break
		}
	}
	if value.selected == "" {
		return value, domain.ErrNotFound
	}
	image, err := probe.Open(ctx, domain.ProbeSource{RootPath: source.RootPath, RelativePath: value.selected})
	if err != nil {
		return value, imageSourceError(ctx, err)
	}
	value.image, err = image.Stdin().Stat()
	imageCloseErr := image.Close()
	after, parentErr := listing.Stat()
	currentMedia, mediaErr := root.Stat(filepath.FromSlash(source.MediaPath))
	currentRoot, rootErr := os.Stat(source.RootPath)
	currentParent, dirErr := root.Stat(filepath.FromSlash(parentPath))
	if err != nil || imageCloseErr != nil || parentErr != nil || mediaErr != nil || rootErr != nil || dirErr != nil ||
		!sameImageFile(value.media, currentMedia) || !sameImageDirectory(value.root, currentRoot) || !sameImageDirectory(value.parent, after) || !sameImageDirectory(value.parent, currentParent) {
		return value, imageSourceError(ctx, err)
	}
	return value, ctx.Err()
}

func sameImageFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}
func sameImageDirectory(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.IsDir() && b.IsDir() && os.SameFile(a, b) && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}
func sameImageObservation(a, b imageObservation) bool {
	return a.selected == b.selected && a.matches == b.matches && sameImageDirectory(a.root, b.root) && sameImageDirectory(a.parent, b.parent) && sameImageFile(a.media, b.media) && sameImageFile(a.image, b.image)
}
