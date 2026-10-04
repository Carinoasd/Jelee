package matroska

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// Runner is one isolated tool mode (process.IsolatedToolRunner).
type Runner interface {
	Run(context.Context, process.ToolRequest) (process.Result, error)
}

// Bounds of one extraction. A source whose selected outputs exceed
// MaxEntryBytes is not cached; fonts larger than MaxFontBytes are skipped.
const (
	DefaultMaxCacheBytes = 1 << 30
	DefaultMaxEntryBytes = 256 << 20
	MaxFontBytes         = 32 << 20
	maxFontTotal         = 128 << 20
	entryFile            = "entry.json"
	entryVersion         = 1
	stagingPrefix        = ".staging-"
	staleStaging         = time.Hour
)

// Config locates the rebuildable cache. CacheRoot must be an existing,
// private (0700), absolute directory that holds nothing else; deleting it
// loses nothing but the time to extract again.
type Config struct {
	CacheRoot     string
	MaxCacheBytes int64
	MaxEntryBytes int64
}

// Kind selects what Locate looks up.
type Kind int

const (
	// KindSubtitle selects a text subtitle by probe stream index.
	KindSubtitle Kind = iota + 1
	// KindAttachment selects a font by 1-based Matroska attachment ID.
	KindAttachment
	// KindAttachmentStream selects a font by probe stream index.
	KindAttachmentStream
)

// Item is one cached file. RelativePath is below CacheRoot and uses slash
// separators; it never names the media library.
type Item struct {
	RelativePath string
	Format       string
	FileName     string
}

type entry struct {
	Version     int          `json:"version"`
	Tracks      int          `json:"tracks"`
	Subtitles   []entryTrack `json:"subtitles"`
	Attachments []entryFont  `json:"attachments"`
	Bytes       int64        `json:"bytes"`
}

type entryTrack struct {
	ID     int    `json:"id"`
	Format string `json:"format"`
	File   string `json:"file"`
}

type entryFont struct {
	ID       int    `json:"id"`
	FileName string `json:"fileName"`
	File     string `json:"file"`
}

// Extractor fills and serves the cache. Methods are safe for concurrent use;
// one source is identified and extracted at most once at a time.
type Extractor struct {
	identify, extract Runner
	config            Config
	mu                sync.Mutex
	inflight          map[string]*flight
	sweep             sync.Mutex
}

type flight struct {
	done  chan struct{}
	entry entry
	err   error
}

// New registers the isolated identification and extraction runners over an
// existing private cache root.
func New(identify, extract Runner, config Config) (*Extractor, error) {
	if identify == nil || extract == nil || !filepath.IsAbs(config.CacheRoot) || filepath.Clean(config.CacheRoot) != config.CacheRoot {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(config.CacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnavailable
	}
	if config.MaxCacheBytes == 0 {
		config.MaxCacheBytes = DefaultMaxCacheBytes
	}
	if config.MaxEntryBytes == 0 {
		config.MaxEntryBytes = DefaultMaxEntryBytes
	}
	if config.MaxEntryBytes < 1<<20 || config.MaxCacheBytes < config.MaxEntryBytes {
		return nil, ErrUnavailable
	}
	return &Extractor{identify: identify, extract: extract, config: config, inflight: make(map[string]*flight)}, nil
}

// Locate returns the cached file for one item of an authorized source,
// extracting the source first when its current revision is not cached. The
// caller has already authorized sourceID and resolved source from it.
func (e *Extractor) Locate(ctx context.Context, sourceID string, source domain.ProbeSource, kind Kind, index int) (Item, error) {
	if e == nil || ctx == nil || !domain.ValidID(sourceID) || index < 0 || kind < KindSubtitle || kind > KindAttachmentStream {
		return Item{}, ErrNotFound
	}
	input, err := probe.Open(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return Item{}, ctx.Err()
		}
		return Item{}, ErrNotFound
	}
	defer func() { _ = input.Close() }()
	stamp := revision(sourceID, input.Metadata())
	current, err := e.cached(sourceID, stamp)
	if err != nil {
		current, err = e.fill(ctx, sourceID, stamp, source, input)
		if err != nil {
			return Item{}, err
		}
	}
	return current.find(sourceID+"/"+stamp, kind, index)
}

// revision names the cached state of one source file. A changed size or
// modification time is a new revision; the previous one is removed.
func revision(sourceID string, metadata probe.Metadata) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-matroska-cache-v1\x00" + sourceID + "\x00"))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(metadata.Size)) //nolint:gosec // G115: sizes are non-negative
	_, _ = hash.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(metadata.ModifiedUnixNano)) //nolint:gosec // G115: bit pattern only
	_, _ = hash.Write(number[:])
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

func (e *Extractor) root() (*os.Root, error) {
	root, err := os.OpenRoot(e.config.CacheRoot)
	if err != nil {
		return nil, ErrUnavailable
	}
	return root, nil
}

// cached reads the entry of a revision and marks it recently used.
func (e *Extractor) cached(sourceID, stamp string) (entry, error) {
	root, err := e.root()
	if err != nil {
		return entry{}, err
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
	return value, nil
}

func (v entry) find(directory string, kind Kind, index int) (Item, error) {
	switch kind {
	case KindSubtitle:
		for _, track := range v.Subtitles {
			if track.ID == index {
				return Item{RelativePath: directory + "/" + track.File, Format: track.Format}, nil
			}
		}
	case KindAttachmentStream:
		// Probes list every track before the attachments, in file order.
		return v.find(directory, KindAttachment, index-v.Tracks+1)
	case KindAttachment:
		for _, font := range v.Attachments {
			if font.ID == index {
				return Item{RelativePath: directory + "/" + font.File, Format: strings.TrimPrefix(path.Ext(font.File), "."), FileName: font.FileName}, nil
			}
		}
	}
	return Item{}, ErrNotFound
}

// fill identifies and extracts one revision once, sharing the result with
// concurrent callers for the same source.
func (e *Extractor) fill(ctx context.Context, sourceID, stamp string, source domain.ProbeSource, input *probe.Input) (entry, error) {
	key := sourceID + "/" + stamp
	e.mu.Lock()
	for {
		call, ok := e.inflight[key]
		if !ok {
			break
		}
		e.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return entry{}, ctx.Err()
		}
		// A run cancelled by its own caller is retried by the next waiter.
		if !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, context.DeadlineExceeded) {
			return call.entry, call.err
		}
		e.mu.Lock()
	}
	call := &flight{done: make(chan struct{})}
	e.inflight[key] = call
	e.mu.Unlock()
	call.entry, call.err = e.build(ctx, sourceID, stamp, source, input)
	e.mu.Lock()
	delete(e.inflight, key)
	e.mu.Unlock()
	close(call.done)
	return call.entry, call.err
}

func toolError(err error) error {
	switch {
	case errors.Is(err, process.ErrBusy):
		return ErrBusy
	case errors.Is(err, process.ErrCancelled), errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, process.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, process.ErrExit):
		// The tool rejected this file: nothing to extract.
		return ErrNotFound
	default:
		return ErrUnavailable
	}
}

func (e *Extractor) build(ctx context.Context, sourceID, stamp string, source domain.ProbeSource, input *probe.Input) (entry, error) {
	result, err := e.identify.Run(ctx, process.ToolRequest{Stdin: input.Stdin()})
	if err != nil {
		return entry{}, toolError(err)
	}
	// Identification output is deterministic for one revision: a file that
	// is not usable Matroska is cached as an empty entry, not identified again.
	container, err := ParseIdentify(result.Stdout)
	if err != nil {
		container = Container{}
	}
	value := entry{Version: entryVersion, Tracks: len(container.Tracks), Subtitles: []entryTrack{}, Attachments: []entryFont{}}
	var plan sandbox.Extraction
	for _, track := range container.Tracks {
		if format, ok := track.TextFormat(); ok && len(plan.Tracks) < sandbox.MaxExtractTracks && track.ID <= sandbox.MaxExtractTrackID {
			plan.Tracks = append(plan.Tracks, track.ID)
			value.Subtitles = append(value.Subtitles, entryTrack{ID: track.ID, Format: format, File: "t" + strconv.Itoa(track.ID) + "." + format})
		}
	}
	var fonts int64
	for _, attachment := range container.Attachments {
		if !attachment.Font() || attachment.Size < 1 || attachment.Size > MaxFontBytes || fonts+attachment.Size > maxFontTotal ||
			len(plan.Attachments) >= sandbox.MaxExtractAttachments || attachment.ID > sandbox.MaxExtractAttachment {
			continue
		}
		fonts += attachment.Size
		plan.Attachments = append(plan.Attachments, attachment.ID)
		value.Attachments = append(value.Attachments, entryFont{ID: attachment.ID, FileName: attachment.FileName, File: "a" + strconv.Itoa(attachment.ID) + "." + fontExtension(attachment)})
	}
	root, err := e.root()
	if err != nil {
		return entry{}, err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(sourceID, 0o700); err != nil {
		return entry{}, ErrUnavailable
	}
	staging := sourceID + "/" + stagingPrefix + stamp + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := root.Mkdir(filepath.FromSlash(staging), 0o700); err != nil {
		return entry{}, ErrUnavailable
	}
	published := false
	defer func() {
		if !published {
			_ = root.RemoveAll(filepath.FromSlash(staging))
		}
	}()
	if len(plan.Tracks)+len(plan.Attachments) > 0 {
		collect := func(directory string) error { return e.collect(root, staging, directory, &value) }
		if _, err := e.extract.Run(ctx, process.ToolRequest{Stdin: input.Stdin(), Extraction: plan, Collect: collect}); err != nil {
			if errors.Is(err, ErrTooLarge) {
				return entry{}, ErrTooLarge
			}
			return entry{}, toolError(err)
		}
	}
	if err := unchanged(ctx, source, input); err != nil {
		return entry{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return entry{}, ErrUnavailable
	}
	if err := writeFile(root, staging+"/"+entryFile, data); err != nil {
		return entry{}, err
	}
	target := sourceID + "/" + stamp
	if err := root.Rename(filepath.FromSlash(staging), filepath.FromSlash(target)); err != nil {
		// A concurrent instance published the same revision first.
		if existing, readErr := readEntry(root, target+"/"+entryFile); readErr == nil {
			return existing, nil
		}
		return entry{}, ErrUnavailable
	}
	published = true
	e.removeOtherRevisions(root, sourceID, stamp)
	e.enforce(root, target)
	return value, nil
}

func fontExtension(a Attachment) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(a.FileName), "."))
	if domain.IsFontFileName(a.FileName) {
		return ext
	}
	switch a.ContentType {
	case "font/otf", "application/x-font-otf", "application/vnd.ms-opentype", "application/x-font-opentype":
		return "otf"
	case "font/collection":
		return "ttc"
	case "font/woff":
		return "woff"
	case "font/woff2":
		return "woff2"
	}
	return "ttf"
}

// collect copies the expected outputs from the runner's private directory
// into staging. Missing tracks (an empty track writes nothing) are dropped;
// any output larger than the entry budget fails the whole extraction.
func (e *Extractor) collect(root *os.Root, staging, directory string, value *entry) error {
	output, err := os.OpenRoot(directory)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = output.Close() }()
	var total int64
	copyOne := func(source, target string) (bool, error) {
		info, err := output.Lstat(source)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil || !info.Mode().IsRegular() {
			return false, ErrUnavailable
		}
		total += info.Size()
		if info.Size() > sandbox.ExtractFileLimit || total > e.config.MaxEntryBytes {
			return false, ErrTooLarge
		}
		in, err := output.Open(source)
		if err != nil {
			return false, ErrUnavailable
		}
		defer func() { _ = in.Close() }()
		out, err := root.OpenFile(filepath.FromSlash(staging+"/"+target), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return false, ErrUnavailable
		}
		written, copyErr := io.Copy(out, io.LimitReader(in, info.Size()+1))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || written != info.Size() {
			return false, ErrUnavailable
		}
		return true, nil
	}
	tracks := value.Subtitles[:0]
	for _, track := range value.Subtitles {
		ok, err := copyOne(sandbox.ExtractTrackName(track.ID), track.File)
		if err != nil {
			return err
		}
		if ok {
			tracks = append(tracks, track)
		}
	}
	value.Subtitles = tracks
	fonts := value.Attachments[:0]
	for _, font := range value.Attachments {
		ok, err := copyOne(sandbox.ExtractAttachmentName(font.ID), font.File)
		if err != nil {
			return err
		}
		if ok {
			fonts = append(fonts, font)
		}
	}
	value.Attachments = fonts
	value.Bytes = total
	return nil
}

// unchanged rejects a result when the source was replaced or modified while
// the tools read it.
func unchanged(ctx context.Context, source domain.ProbeSource, input *probe.Input) error {
	before := input.Metadata()
	info, err := input.Stdin().Stat()
	if err != nil || info.Size() != before.Size || info.ModTime().UnixNano() != before.ModifiedUnixNano {
		return ErrChanged
	}
	current, err := probe.Open(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrChanged
	}
	defer func() { _ = current.Close() }()
	now, err := current.Stdin().Stat()
	if err != nil || !os.SameFile(info, now) || current.Metadata() != before {
		return ErrChanged
	}
	return nil
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

func (e *Extractor) removeOtherRevisions(root *os.Root, sourceID, keep string) {
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
// directories that a crashed instance left behind.
func (e *Extractor) enforce(root *os.Root, keep string) {
	e.sweep.Lock()
	defer e.sweep.Unlock()
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
		if total <= e.config.MaxCacheBytes {
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

// Clear removes every cached revision; the next request extracts again.
func (e *Extractor) Clear() error {
	root, err := e.root()
	if err != nil {
		return err
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
