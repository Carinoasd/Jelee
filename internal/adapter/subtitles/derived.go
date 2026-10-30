package subtitles

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Derived UTF-8 cache (G15.4). It lives in the "utf8" directory of the
// rebuildable subtitle cache root that the Matroska extraction cache also
// uses; that cache only touches directories named by a source ID, so the
// two never meet. Below it every source has a directory named by its ID
// holding, per external subtitle track, at most one copy:
//
//	utf8/<sourceID>/<trackID>-<key>.<ext>       the UTF-8 copy
//	utf8/<sourceID>/<trackID>-<key>.rejected    conversion proved the charset wrong
//	utf8/<sourceID>/.staging-<trackID>-...      a copy being written
//
// key is DerivedUTF8Key over the track ID, the library-relative path, the
// size and modification time of the original as opened, its recorded
// charset and ConverterVersion, so a changed original, charset or converter
// never hits an old entry. The modification time of an entry is its last
// use; the subtitle_cache_cleanup job removes idle entries and the size
// bound evicts the least recently used.
const (
	DerivedDirectory = "utf8"
	// DefaultDerivedMaxBytes bounds the derived cache when no bound is set.
	DefaultDerivedMaxBytes int64 = 64 << 20
	// MinDerivedMaxBytes is the smallest accepted bound.
	MinDerivedMaxBytes int64 = 1 << 20
	// MaxReplacementPermille is the share of input bytes, in thousandths,
	// that may become U+FFFD before a conversion is taken as proof of a
	// wrong charset (G15.4): above 1% the copy is withheld.
	MaxReplacementPermille = 10

	derivedStaging      = ".staging-"
	privateMask         = 0o077
	derivedRejected     = "rejected"
	derivedStaleStaging = time.Hour
	derivedTrackPrefix  = 36
	derivedKeyHex       = 64
)

// Reasons a UTF-8 copy is not offered. They are fixed codes for logs.
const (
	ReasonCharsetUnknown    = "charset_unknown"
	ReasonAlreadyUTF8       = "already_utf8"
	ReasonFormat            = "format_not_convertible"
	ReasonCharsetMismatch   = "charset_mismatch"
	ReasonLowConfidence     = "charset_low_confidence"
	ReasonNotText           = "not_text"
	ReasonReplacementRatio  = "replacement_ratio"
	ReasonTooLarge          = "too_large"
	ReasonSourceChanged     = "source_changed"
	ReasonCacheUnavailable  = "cache_unavailable"
	ReasonSourceUnavailable = "source_unavailable"
)

var (
	// ErrDerivedUnavailable reports an unusable cache root.
	ErrDerivedUnavailable = errors.New("subtitles: derived cache unavailable")
	// ErrDerivedInvalid reports an invalid request.
	ErrDerivedInvalid = errors.New("subtitles: invalid derived request")
)

// NotDerivedError reports why no UTF-8 copy exists for a track. The
// original stays available; callers either fall back to it or answer like
// a missing item.
type NotDerivedError struct{ Reason string }

func (e *NotDerivedError) Error() string { return "subtitles: no UTF-8 copy: " + e.Reason }

func notDerived(reason string) error { return &NotDerivedError{Reason: reason} }

// DerivedReason returns the reason code of err, or "" when err does not
// say why no copy exists.
func DerivedReason(err error) string {
	var nd *NotDerivedError
	if errors.As(err, &nd) {
		return nd.Reason
	}
	return ""
}

// DerivedRequest names one authorized external text subtitle. Root and
// RelativePath locate the original, which is only ever opened read-only;
// Charset is the charset the scan recorded for it and Format its
// extension.
type DerivedRequest struct {
	SourceID, TrackID  string
	Root, RelativePath string
	Format             string
	Charset            string
}

// String keeps library paths out of diagnostics.
func (r DerivedRequest) String() string { return "derived subtitle request (redacted)" }

// GoString keeps library paths out of %#v.
func (r DerivedRequest) GoString() string { return r.String() }

// DerivedCache produces and serves UTF-8 copies of external text subtitles
// whose recorded charset is not UTF-8. Methods are safe for concurrent use;
// one copy is written at most once at a time.
type DerivedCache struct {
	cacheRoot string
	maxBytes  int64
	mu        sync.Mutex
	inflight  map[string]*derivedFlight
	sweep     sync.Mutex
	// builds counts conversions started, for tests.
	builds atomic.Int64
}

type derivedFlight struct {
	done chan struct{}
	err  error
}

// NewDerivedCache opens the derived directory below cacheRoot, which must
// be an existing private (0700) absolute directory as for the Matroska
// cache; the directory itself is created when missing. maxBytes zero
// selects DefaultDerivedMaxBytes.
func NewDerivedCache(cacheRoot string, maxBytes int64) (*DerivedCache, error) {
	if !filepath.IsAbs(cacheRoot) || filepath.Clean(cacheRoot) != cacheRoot {
		return nil, ErrDerivedUnavailable
	}
	if maxBytes == 0 {
		maxBytes = DefaultDerivedMaxBytes
	}
	if maxBytes < MinDerivedMaxBytes {
		return nil, ErrDerivedUnavailable
	}
	if !privateDirectory(cacheRoot) {
		return nil, ErrDerivedUnavailable
	}
	directory := filepath.Join(cacheRoot, DerivedDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, ErrDerivedUnavailable
	}
	if !privateDirectory(directory) {
		return nil, ErrDerivedUnavailable
	}
	return &DerivedCache{cacheRoot: cacheRoot, maxBytes: maxBytes, inflight: map[string]*derivedFlight{}}, nil
}

func privateDirectory(name string) bool {
	info, err := os.Lstat(name)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&privateMask == 0
}

// CacheRoot is the root the returned relative paths are below.
func (c *DerivedCache) CacheRoot() string { return c.cacheRoot }

func (c *DerivedCache) root() (*os.Root, error) {
	root, err := os.OpenRoot(filepath.Join(c.cacheRoot, DerivedDirectory))
	if err != nil {
		return nil, ErrDerivedUnavailable
	}
	return root, nil
}

// convertibleFormat mirrors domain.SidecarCharsetState: the text formats
// that declare no charset of their own.
func convertibleFormat(format string) bool {
	return domain.SidecarCharsetState(domain.SidecarKindSubtitle, format, domain.SidecarSubtitleMicroDVD, "UTF-8", true) != ""
}

// Locate returns the path, relative to CacheRoot and slash separated, of
// the UTF-8 copy of one external text subtitle, writing it first when it
// is not cached. A *NotDerivedError says why there is none; the original
// is never changed either way.
func (c *DerivedCache) Locate(ctx context.Context, req DerivedRequest) (string, error) {
	if c == nil || ctx == nil || !domain.ValidID(req.SourceID) || !domain.ValidID(req.TrackID) || !filepath.IsAbs(req.Root) ||
		req.RelativePath == "" || req.RelativePath == ".." || path.IsAbs(req.RelativePath) || path.Clean(req.RelativePath) != req.RelativePath || strings.HasPrefix(req.RelativePath, "../") {
		return "", ErrDerivedInvalid
	}
	format := strings.ToLower(req.Format)
	if !convertibleFormat(format) {
		return "", notDerived(ReasonFormat)
	}
	cs, ok := ParseCharset(req.Charset)
	switch {
	case req.Charset == "" || !ok:
		return "", notDerived(ReasonCharsetUnknown)
	case cs == UTF8:
		return "", notDerived(ReasonAlreadyUTF8)
	}
	library, err := os.OpenRoot(req.Root)
	if err != nil {
		return "", notDerived(ReasonSourceUnavailable)
	}
	defer func() { _ = library.Close() }()
	original, err := library.Open(filepath.FromSlash(req.RelativePath))
	if err != nil {
		return "", notDerived(ReasonSourceUnavailable)
	}
	defer func() { _ = original.Close() }()
	info, err := original.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", notDerived(ReasonSourceUnavailable)
	}
	if info.Size() > DefaultConvertLimit {
		return "", notDerived(ReasonTooLarge)
	}
	key, err := DerivedUTF8Key(DerivedSource{LibraryID: req.TrackID, RelPath: req.RelativePath, Size: info.Size(), ModTimeUnixNS: info.ModTime().UnixNano()}, cs)
	if err != nil {
		return "", notDerived(ReasonCharsetUnknown)
	}
	stem := req.TrackID + "-" + key
	copyName, rejectedName := stem+"."+format, stem+"."+derivedRejected
	if relative, err := c.cached(req.SourceID, copyName, rejectedName); err == nil || DerivedReason(err) != "" {
		return relative, err
	}
	if err := c.fill(ctx, req, original, info, cs, stem, format); err != nil {
		return "", err
	}
	return DerivedDirectory + "/" + req.SourceID + "/" + copyName, nil
}

// cached answers from the cache and marks the entry recently used. It
// returns ErrDerivedUnavailable on a miss.
func (c *DerivedCache) cached(sourceID, copyName, rejectedName string) (string, error) {
	root, err := c.root()
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	now := time.Now()
	if info, err := root.Lstat(sourceID + "/" + copyName); err == nil && info.Mode().IsRegular() {
		_ = root.Chtimes(sourceID+"/"+copyName, now, now)
		return DerivedDirectory + "/" + sourceID + "/" + copyName, nil
	}
	if data, err := readSmall(root, sourceID+"/"+rejectedName); err == nil {
		_ = root.Chtimes(sourceID+"/"+rejectedName, now, now)
		reason := strings.TrimSpace(string(data))
		if !slices.Contains([]string{ReasonCharsetMismatch, ReasonLowConfidence, ReasonNotText, ReasonReplacementRatio, ReasonTooLarge}, reason) {
			reason = ReasonReplacementRatio
		}
		return "", notDerived(reason)
	}
	return "", ErrDerivedUnavailable
}

func readSmall(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, 64))
}

// fill writes one copy once, sharing the outcome with concurrent callers.
func (c *DerivedCache) fill(ctx context.Context, req DerivedRequest, original *os.File, info os.FileInfo, cs Charset, stem, format string) error {
	key := req.SourceID + "/" + stem
	c.mu.Lock()
	for {
		call, ok := c.inflight[key]
		if !ok {
			break
		}
		c.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		// A run cancelled by its own caller is retried by the next waiter.
		if !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, context.DeadlineExceeded) {
			return call.err
		}
		c.mu.Lock()
	}
	call := &derivedFlight{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()
	call.err = c.build(ctx, req, original, info, cs, stem, format)
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(call.done)
	return call.err
}

// build checks the recorded charset against the bytes as they are now,
// converts them into a staging file and publishes it, or records why the
// copy is withheld.
func (c *DerivedCache) build(ctx context.Context, req DerivedRequest, original *os.File, info os.FileInfo, cs Charset, stem, format string) error {
	c.builds.Add(1)
	root, err := c.root()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(req.SourceID, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return ErrDerivedUnavailable
	}
	reject := func(reason string) error {
		if err := writeSmall(root, req.SourceID, stem, reason); err != nil {
			return err
		}
		return notDerived(reason)
	}
	// Detection again, on the current bytes: the recorded charset may be
	// from an older file, and only a confident, matching answer is used.
	detected, confidence, err := DetectCharset(ctx, io.NewSectionReader(original, 0, info.Size()), 0)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrNotText):
		return reject(ReasonNotText)
	case err != nil:
		return notDerived(ReasonSourceUnavailable)
	case detected != cs:
		return reject(ReasonCharsetMismatch)
	case confidence.Low:
		return reject(ReasonLowConfidence)
	}
	staging := req.SourceID + "/" + derivedStaging + stem + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrDerivedUnavailable
	}
	published := false
	defer func() {
		if !published {
			_ = root.Remove(staging)
		}
	}()
	result, err := ToUTF8(ctx, io.NewSectionReader(original, 0, info.Size()), file, cs, DefaultConvertLimit)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrTooLarge):
		return reject(ReasonTooLarge)
	case err != nil:
		return notDerived(ReasonSourceUnavailable)
	case result.Replaced*1000 > result.Read*MaxReplacementPermille:
		return reject(ReasonReplacementRatio)
	}
	derivedAfterConvert(req.TrackID)
	// A file rewritten while it was read is not cached; the next request
	// converts the new bytes under their own key.
	if now, err := original.Stat(); err != nil || now.Size() != info.Size() || !now.ModTime().Equal(info.ModTime()) {
		return notDerived(ReasonSourceChanged)
	}
	if err := root.Rename(staging, req.SourceID+"/"+stem+"."+format); err != nil {
		return ErrDerivedUnavailable
	}
	published = true
	c.removeOtherRevisions(root, req.SourceID, req.TrackID, stem)
	c.enforce(root, req.SourceID+"/"+stem+"."+format)
	return nil
}

// derivedAfterConvert runs between conversion and publication; tests
// change the original there.
var derivedAfterConvert = func(string) {}

func writeSmall(root *os.Root, sourceID, stem, reason string) error {
	name := sourceID + "/" + stem + "." + derivedRejected
	staging := sourceID + "/" + derivedStaging + stem + "-r" + strconv.FormatInt(time.Now().UnixNano(), 36)
	file, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrDerivedUnavailable
	}
	_, err = file.WriteString(reason + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(staging, name)
	}
	if err != nil {
		_ = root.Remove(staging)
		return ErrDerivedUnavailable
	}
	return nil
}

// entryName splits a cache file name into its track ID and key stem; ok is
// false for anything this cache did not write.
func entryName(name string) (track, stem string, ok bool) {
	if len(name) < derivedTrackPrefix+1+derivedKeyHex+2 || name[derivedTrackPrefix] != '-' || name[derivedTrackPrefix+1+derivedKeyHex] != '.' {
		return "", "", false
	}
	track, stem = name[:derivedTrackPrefix], name[:derivedTrackPrefix+1+derivedKeyHex]
	if !domain.ValidID(track) {
		return "", "", false
	}
	for _, r := range name[derivedTrackPrefix+1 : derivedTrackPrefix+1+derivedKeyHex] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", "", false
		}
	}
	extension := name[derivedTrackPrefix+1+derivedKeyHex+1:]
	if extension != derivedRejected && !convertibleFormat(extension) {
		return "", "", false
	}
	return track, stem, true
}

// removeOtherRevisions drops older copies and rejections of the same track.
func (c *DerivedCache) removeOtherRevisions(root *os.Root, sourceID, trackID, keep string) {
	names := readNames(root, sourceID)
	for _, name := range names {
		if track, stem, ok := entryName(name); ok && track == trackID && stem != keep {
			_ = root.Remove(sourceID + "/" + name)
		}
	}
}

func readNames(root *os.Root, name string) []string {
	directory, err := root.Open(name)
	if err != nil {
		return nil
	}
	names, _ := directory.Readdirnames(-1)
	_ = directory.Close()
	return names
}

type derivedEntry struct {
	name  string
	bytes int64
	used  time.Time
}

// entries lists every cache file with its size and last use.
func (c *DerivedCache) entries(root *os.Root, sources []string) ([]derivedEntry, int64) {
	var result []derivedEntry
	var total int64
	for _, source := range sources {
		for _, name := range readNames(root, source) {
			if _, _, ok := entryName(name); !ok {
				continue
			}
			info, err := root.Lstat(source + "/" + name)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			total += info.Size()
			result = append(result, derivedEntry{name: source + "/" + name, bytes: info.Size(), used: info.ModTime()})
		}
	}
	return result, total
}

// enforce removes least recently used entries until the cache fits its
// bound, keeping the one just written.
func (c *DerivedCache) enforce(root *os.Root, keep string) domain.SubtitleCacheSweep {
	c.sweep.Lock()
	defer c.sweep.Unlock()
	var sweep domain.SubtitleCacheSweep
	sources, err := listSources(root)
	if err != nil {
		return sweep
	}
	entries, total := c.entries(root, sources)
	slices.SortFunc(entries, func(a, b derivedEntry) int { return a.used.Compare(b.used) })
	for _, candidate := range entries {
		if total <= c.maxBytes {
			break
		}
		if candidate.name == keep {
			continue
		}
		if err := root.Remove(candidate.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			sweep.Failed++
			continue
		}
		total -= candidate.bytes
		sweep.Bytes += candidate.bytes
		sweep.Capacity++
	}
	return sweep
}

func listSources(root *os.Root) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrDerivedUnavailable
	}
	names, err := directory.Readdirnames(-1)
	_ = directory.Close()
	if err != nil {
		return nil, ErrDerivedUnavailable
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

// CountSources implements app.SubtitleCacheSweeper over the derived copies.
func (c *DerivedCache) CountSources(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	root, err := c.root()
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	names, err := listSources(root)
	return int64(len(names)), err
}

// ListSources implements app.SubtitleCacheSweeper.
func (c *DerivedCache) ListSources(ctx context.Context, after string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || after != "" && !domain.ValidID(after) {
		return nil, ErrDerivedInvalid
	}
	root, err := c.root()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	names, err := listSources(root)
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

// HasSource reports whether the derived cache holds a directory for source.
func (c *DerivedCache) HasSource(source string) bool {
	if !domain.ValidID(source) {
		return false
	}
	root, err := c.root()
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(source)
	return err == nil && info.IsDir()
}

// SweepSource implements app.SubtitleCacheSweeper: the directory of a
// deleted source goes as a whole; for a live one, entries last used before
// idleBefore and staging files older than an hour are removed. Each
// removed copy or rejection counts as a revision. Names this cache did not
// write are left alone.
func (c *DerivedCache) SweepSource(ctx context.Context, source string, live bool, idleBefore time.Time) (domain.SubtitleCacheSweep, error) {
	var sweep domain.SubtitleCacheSweep
	if err := ctx.Err(); err != nil {
		return sweep, err
	}
	if !domain.ValidID(source) {
		return sweep, ErrDerivedInvalid
	}
	root, err := c.root()
	if err != nil {
		return sweep, err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(source); errors.Is(err, fs.ErrNotExist) {
		return sweep, nil
	}
	if !live {
		_, bytes := c.entries(root, []string{source})
		if err := root.RemoveAll(source); err != nil && !errors.Is(err, fs.ErrNotExist) {
			sweep.Failed++
			return sweep, nil
		}
		sweep.Bytes += bytes
		sweep.Orphaned++
		return sweep, nil
	}
	for _, name := range readNames(root, source) {
		relative := source + "/" + name
		info, err := root.Lstat(relative)
		if err != nil {
			continue
		}
		_, _, entry := entryName(name)
		stale := strings.HasPrefix(name, derivedStaging) && time.Since(info.ModTime()) > derivedStaleStaging
		if !stale && !(entry && info.ModTime().Before(idleBefore)) {
			continue
		}
		if err := root.Remove(relative); err != nil && !errors.Is(err, fs.ErrNotExist) {
			sweep.Failed++
			continue
		}
		sweep.Revisions++
		if info.Mode().IsRegular() {
			sweep.Bytes += info.Size()
		}
	}
	// An empty source directory goes too; Remove fails on a non-empty one.
	_ = root.Remove(source)
	return sweep, nil
}

// EnforceCapacity implements app.SubtitleCacheSweeper with the cache's own
// least recently used order.
func (c *DerivedCache) EnforceCapacity(ctx context.Context) (domain.SubtitleCacheSweep, error) {
	if err := ctx.Err(); err != nil {
		return domain.SubtitleCacheSweep{}, err
	}
	root, err := c.root()
	if err != nil {
		return domain.SubtitleCacheSweep{}, err
	}
	defer func() { _ = root.Close() }()
	return c.enforce(root, ""), nil
}
