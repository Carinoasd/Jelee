package matroska

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Sweeper maintains the extraction cache for the subtitle_cache_cleanup job
// (G13.6). It only touches what this package writes below CacheRoot: source
// directories named by a media source ID and, inside them, revision
// directories (32 hexadecimal digits) and staging directories. The directory
// of a deleted source goes as a whole; inside a live source any other name
// is left alone, and the os.Root never follows a path out of the cache.
type Sweeper struct {
	config Config
	lock   *sync.Mutex
}

// NewSweeper checks the cache root like New does, without the tool runners:
// a cache left behind while the runtime is missing can still be cleaned.
func NewSweeper(config Config) (*Sweeper, error) {
	if !supportedPlatform || !filepath.IsAbs(config.CacheRoot) || filepath.Clean(config.CacheRoot) != config.CacheRoot {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(config.CacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnavailable
	}
	if config.MaxCacheBytes == 0 {
		config.MaxCacheBytes = DefaultMaxCacheBytes
	}
	if config.MaxCacheBytes < 1<<20 {
		return nil, ErrUnavailable
	}
	return &Sweeper{config: config, lock: &sync.Mutex{}}, nil
}

// Sweeper returns a sweeper over the extractor's cache that shares its size
// enforcement lock.
func (e *Extractor) Sweeper() *Sweeper {
	return &Sweeper{config: e.config, lock: &e.sweep}
}

func (s *Sweeper) root() (*os.Root, error) {
	root, err := os.OpenRoot(s.config.CacheRoot)
	if err != nil {
		return nil, ErrUnavailable
	}
	return root, nil
}

func revisionName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Sweeper) sources(root *os.Root) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrUnavailable
	}
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		return nil, ErrUnavailable
	}
	result := names[:0]
	for _, name := range names {
		if domain.ValidID(name) {
			if info, err := root.Lstat(name); err == nil && info.IsDir() {
				result = append(result, name)
			}
		}
	}
	slices.Sort(result)
	return result, nil
}

// CountSources implements app.SubtitleCacheSweeper.
func (s *Sweeper) CountSources(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	root, err := s.root()
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	names, err := s.sources(root)
	return int64(len(names)), err
}

// ListSources implements app.SubtitleCacheSweeper.
func (s *Sweeper) ListSources(ctx context.Context, after string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || after != "" && !domain.ValidID(after) {
		return nil, ErrInvalid
	}
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	names, err := s.sources(root)
	if err != nil {
		return nil, err
	}
	start, _ := slices.BinarySearch(names, after)
	if start < len(names) && names[start] == after {
		start++
	}
	names = names[start:]
	if len(names) > limit {
		names = names[:limit]
	}
	return slices.Clone(names), nil
}

// size sums the regular files below name without following links.
func size(root *os.Root, name string) int64 {
	var total int64
	_ = fs.WalkDir(root.FS(), filepath.ToSlash(name), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// remove deletes name and counts the bytes it held; a failure is counted
// instead of returned.
func remove(root *os.Root, name string, sweep *domain.SubtitleCacheSweep) bool {
	bytes := size(root, name)
	if err := root.RemoveAll(filepath.FromSlash(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		sweep.Failed++
		return false
	}
	sweep.Bytes += bytes
	return true
}

// SweepSource implements app.SubtitleCacheSweeper. A revision's last use is
// the modification time of its entry, which every cache hit renews.
func (s *Sweeper) SweepSource(ctx context.Context, source string, live bool, idleBefore time.Time) (domain.SubtitleCacheSweep, error) {
	var sweep domain.SubtitleCacheSweep
	if err := ctx.Err(); err != nil {
		return sweep, err
	}
	if !domain.ValidID(source) {
		return sweep, ErrInvalid
	}
	root, err := s.root()
	if err != nil {
		return sweep, err
	}
	defer func() { _ = root.Close() }()
	if !live {
		if remove(root, source, &sweep) {
			sweep.Orphaned++
		}
		return sweep, nil
	}
	directory, err := root.Open(source)
	if errors.Is(err, fs.ErrNotExist) {
		return sweep, nil
	}
	if err != nil {
		sweep.Failed++
		return sweep, nil
	}
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		sweep.Failed++
		return sweep, nil
	}
	for _, name := range names {
		relative := source + "/" + name
		switch {
		case strings.HasPrefix(name, stagingPrefix):
			// A staging directory younger than staleStaging may belong to a
			// running extraction.
			if info, err := root.Lstat(filepath.FromSlash(relative)); err == nil && time.Since(info.ModTime()) > staleStaging && remove(root, relative, &sweep) {
				sweep.Revisions++
			}
		case revisionName(name):
			info, statErr := root.Lstat(filepath.FromSlash(relative + "/" + entryFile))
			_, readErr := readEntry(root, relative+"/"+entryFile)
			if (statErr != nil || readErr != nil || info.ModTime().Before(idleBefore)) && remove(root, relative, &sweep) {
				sweep.Revisions++
			}
		}
	}
	return sweep, nil
}

// EnforceCapacity implements app.SubtitleCacheSweeper with the extractor's
// own accounting: entry sizes, least recently used first.
func (s *Sweeper) EnforceCapacity(ctx context.Context) (domain.SubtitleCacheSweep, error) {
	var sweep domain.SubtitleCacheSweep
	if err := ctx.Err(); err != nil {
		return sweep, err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	root, err := s.root()
	if err != nil {
		return sweep, err
	}
	defer func() { _ = root.Close() }()
	sources, err := s.sources(root)
	if err != nil {
		return sweep, err
	}
	var entries []cachedEntry
	var total int64
	for _, source := range sources {
		directory, err := root.Open(source)
		if err != nil {
			continue
		}
		names, _ := directory.Readdirnames(-1)
		_ = directory.Close()
		for _, name := range names {
			if !revisionName(name) {
				continue
			}
			relative := source + "/" + name
			info, statErr := root.Lstat(filepath.FromSlash(relative + "/" + entryFile))
			value, readErr := readEntry(root, relative+"/"+entryFile)
			if statErr != nil || readErr != nil {
				continue
			}
			total += value.Bytes
			entries = append(entries, cachedEntry{name: relative, bytes: value.Bytes, used: info.ModTime()})
		}
	}
	slices.SortFunc(entries, func(a, b cachedEntry) int { return a.used.Compare(b.used) })
	for _, candidate := range entries {
		if total <= s.config.MaxCacheBytes {
			break
		}
		if root.RemoveAll(filepath.FromSlash(candidate.name)) != nil {
			sweep.Failed++
			continue
		}
		total -= candidate.bytes
		sweep.Bytes += candidate.bytes
		sweep.Capacity++
	}
	return sweep, nil
}
