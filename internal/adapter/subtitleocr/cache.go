package subtitleocr

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The cache is <CacheRoot>/<sourceID>/<revision>/{entry.json,s<index>.srt}.
// A revision binds the file's size and modification time, the recognizer
// identity and the decoder version; anything else is a new revision and the
// previous one is removed. The whole cache can be deleted at any time.

// cached reads the entry of a revision and marks it recently used.
func (s *Service) cached(sourceID, stamp string) (entry, error) {
	root, err := os.OpenRoot(s.config.CacheRoot)
	if err != nil {
		return entry{}, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	name := sourceID + "/" + stamp + "/" + entryFile
	value, err := readEntry(root, name)
	if err != nil {
		return entry{}, err
	}
	now := time.Now()
	_ = root.Chtimes(filepath.FromSlash(name), now, now)
	return value, nil
}

func readEntry(root *os.Root, name string) (entry, error) {
	file, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return entry{}, ErrNotFound
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(data) > 64<<10 {
		return entry{}, ErrNotFound
	}
	var value entry
	if json.Unmarshal(data, &value) != nil || value.Version != entryVersion {
		return entry{}, ErrNotFound
	}
	for _, track := range value.Tracks {
		// Only names this package writes are ever served.
		if track.File != "" && track.File != "s"+strconv.Itoa(track.Index)+".srt" {
			return entry{}, ErrNotFound
		}
	}
	return value, nil
}

func writeFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(filepath.FromSlash(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrUnavailable
	}
	_, writeErr := file.Write(data)
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	return nil
}

// publish writes the entry into staging and renames it to the revision,
// then removes older revisions of the source and enforces the size bound.
func (s *Service) publish(root *os.Root, next job, staging string, value entry) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrUnavailable
	}
	if err := writeFile(root, staging+"/"+entryFile, data); err != nil {
		return err
	}
	target := next.sourceID + "/" + next.stamp
	if err := root.Rename(filepath.FromSlash(staging), filepath.FromSlash(target)); err != nil {
		// Another instance published the same revision first.
		if _, readErr := readEntry(root, target+"/"+entryFile); readErr == nil {
			_ = root.RemoveAll(filepath.FromSlash(staging))
			return nil
		}
		return ErrUnavailable
	}
	s.removeOtherRevisions(root, next.sourceID, next.stamp)
	s.enforce(root, target)
	return nil
}

func (s *Service) removeOtherRevisions(root *os.Root, sourceID, keep string) {
	directory, err := root.Open(sourceID)
	if err != nil {
		return
	}
	names, _ := directory.Readdirnames(256)
	_ = directory.Close()
	for _, name := range names {
		if name != keep && !strings.HasPrefix(name, stagingPrefix) {
			_ = root.RemoveAll(filepath.FromSlash(sourceID + "/" + name))
		}
	}
}

type cachedEntry struct {
	name  string
	bytes int64
	used  time.Time
}

// enforce removes least recently used revisions until the cache fits
// MaxCacheBytes, keeping the one just written, and removes staging
// directories a crashed instance left behind.
func (s *Service) enforce(root *os.Root, keep string) {
	s.sweep.Lock()
	defer s.sweep.Unlock()
	var entries []cachedEntry
	var total int64
	sources, err := root.Open(".")
	if err != nil {
		return
	}
	names, _ := sources.Readdirnames(100000)
	_ = sources.Close()
	for _, source := range names {
		if !domain.ValidID(source) {
			continue
		}
		directory, err := root.Open(source)
		if err != nil {
			continue
		}
		revisions, _ := directory.Readdirnames(256)
		_ = directory.Close()
		for _, name := range revisions {
			relative := source + "/" + name
			if strings.HasPrefix(name, stagingPrefix) {
				if info, err := root.Lstat(filepath.FromSlash(relative)); err == nil && time.Since(info.ModTime()) > staleStaging {
					_ = root.RemoveAll(filepath.FromSlash(relative))
				}
				continue
			}
			info, err := root.Lstat(filepath.FromSlash(relative + "/" + entryFile))
			value, readErr := readEntry(root, relative+"/"+entryFile)
			if err != nil || readErr != nil {
				_ = root.RemoveAll(filepath.FromSlash(relative))
				continue
			}
			total += value.Bytes
			entries = append(entries, cachedEntry{name: relative, bytes: value.Bytes, used: info.ModTime()})
		}
	}
	slices.SortFunc(entries, func(a, b cachedEntry) int { return a.used.Compare(b.used) })
	for _, candidate := range entries {
		if total <= s.config.MaxCacheBytes {
			return
		}
		if candidate.name == keep {
			continue
		}
		if root.RemoveAll(filepath.FromSlash(candidate.name)) == nil {
			total -= candidate.bytes
		}
	}
}

// Clear removes every cached revision; the next lookup queues them again.
func (s *Service) Clear() error {
	root, err := os.OpenRoot(s.config.CacheRoot)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return ErrUnavailable
	}
	names, _ := directory.Readdirnames(100000)
	_ = directory.Close()
	for _, name := range names {
		if domain.ValidID(name) {
			if err := root.RemoveAll(name); err != nil {
				return ErrUnavailable
			}
		}
	}
	return nil
}
