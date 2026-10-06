package subtitleocr

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Extractor copies the bitmap subtitle tracks of an opened source into a
// private directory (matroska.Extractor).
type Extractor interface {
	ExtractBitmaps(ctx context.Context, source domain.ProbeSource, input *probe.Input, directory string) ([]matroska.BitmapTrack, error)
}

// Bounds and defaults.
const (
	DefaultMaxCacheBytes = 256 << 20
	MaxConcurrency       = 4
	MaxQueue             = 256
	// decoderVersion is part of every cache revision: a decoder or picture
	// preparation change rebuilds all results.
	decoderVersion = "bitmapsub-v1"
	entryVersion   = 1
	entryFile      = "entry.json"
	stagingPrefix  = ".staging-"
	staleStaging   = time.Hour
	// retryAfter keeps a failed revision out of the queue for a while so
	// a broken file cannot keep the recognizer busy.
	retryAfter   = 10 * time.Minute
	maxFailures  = 1024
	busyAttempts = 5
)

// Lookup results. ErrPending means the result is not built yet and the
// source has been queued (or the queue was full); ErrNotFound means there is
// nothing to deliver for this index of the current revision.
var (
	ErrPending  = errors.New("subtitle_ocr_pending")
	ErrNotFound = errors.New("subtitle_ocr_not_found")
)

// Config configures the service. CacheRoot is an existing, private (0700)
// absolute directory holding nothing else; WorkRoot is a private scratch
// directory for extracted tracks and pictures, emptied after each job.
type Config struct {
	CacheRoot         string
	MaxCacheBytes     int64
	WorkRoot          string
	Languages         []string
	PicturesPerMinute int
	Concurrency       int
	QueueSize         int
	// Identity describes the recognizer (ocrruntime.Identity); a result made
	// by another recognizer is never served.
	Identity string
	Clock    Clock
	// Budget is the instance-wide CPU admission (G41); nil admits all.
	Budget app.WorkBudget
	Logger *slog.Logger
}

// Item is one cached SRT below CacheRoot, with slash separators.
type Item struct {
	RelativePath string
}

// Stats are aggregate counters without paths, IDs or text.
type Stats struct {
	Queued     int
	Running    int
	Completed  uint64
	Failed     uint64
	Dropped    uint64
	Pictures   uint64
	Recognized uint64
	Reused     uint64
	Rejected   uint64
	Skipped    uint64
	Throttled  uint64
}

type entry struct {
	Version int          `json:"version"`
	Tracks  []entryTrack `json:"tracks"`
	Bytes   int64        `json:"bytes"`
}

type entryTrack struct {
	Index     int    `json:"index"`
	Format    string `json:"format"`
	File      string `json:"file,omitempty"`
	Pictures  int    `json:"pictures"`
	Cues      int    `json:"cues"`
	Skipped   int    `json:"skipped"`
	Truncated bool   `json:"truncated,omitempty"`
}

type job struct {
	key, sourceID, stamp string
	source               domain.ProbeSource
}

// Service queues, runs and serves OCR results. Methods are safe for
// concurrent use; at most one source is processed at a time and at most
// Concurrency pictures are recognized at once.
type Service struct {
	extractor  Extractor
	recognizer PictureRecognizer
	config     Config
	limiter    *RateLimiter
	logger     *slog.Logger

	queue    chan job
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	pending  map[string]bool
	failures map[string]time.Time
	sweep    sync.Mutex
	closed   bool

	running                                atomic.Int64
	completed, failed, dropped             atomic.Uint64
	pictures, recognized, reused, rejected atomic.Uint64
	skipped                                atomic.Uint64
}

// New validates the configuration and starts the background dispatcher.
func New(extractor Extractor, recognizer PictureRecognizer, config Config) (*Service, error) {
	if !supportedPlatform || extractor == nil || recognizer == nil || !privateDirectory(config.CacheRoot) || !privateDirectory(config.WorkRoot) || config.Identity == "" ||
		len(config.Languages) == 0 || config.Concurrency < 1 || config.Concurrency > MaxConcurrency || config.QueueSize < 1 || config.QueueSize > MaxQueue {
		return nil, ErrUnavailable
	}
	if config.MaxCacheBytes == 0 {
		config.MaxCacheBytes = DefaultMaxCacheBytes
	}
	if config.MaxCacheBytes < MaxTrackBytes {
		return nil, ErrUnavailable
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	limiter, err := NewRateLimiter(config.PicturesPerMinute, config.Clock)
	if err != nil {
		return nil, ErrUnavailable
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{extractor: extractor, recognizer: recognizer, config: config, limiter: limiter, logger: logger,
		queue: make(chan job, config.QueueSize), cancel: cancel, done: make(chan struct{}), pending: make(map[string]bool), failures: make(map[string]time.Time)}
	go s.dispatch(ctx)
	return s, nil
}

func privateDirectory(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0
}

// Close stops the dispatcher, cancels the running job (its staging and
// work files are removed) and waits for it. Queued jobs are dropped.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	<-s.done
	return nil
}

// Stats returns the current counters.
func (s *Service) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	queued := len(s.pending) - int(s.running.Load())
	s.mu.Unlock()
	return Stats{Queued: max(queued, 0), Running: int(s.running.Load()), Completed: s.completed.Load(), Failed: s.failed.Load(), Dropped: s.dropped.Load(),
		Pictures: s.pictures.Load(), Recognized: s.recognized.Load(), Reused: s.reused.Load(), Rejected: s.rejected.Load(), Skipped: s.skipped.Load(), Throttled: s.limiter.Waits()}
}

// revision names the OCR state of one source file under one recognizer.
func (s *Service) revision(sourceID string, metadata probe.Metadata) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-subtitle-ocr-v1\x00" + sourceID + "\x00" + s.config.Identity + "\x00" + decoderVersion + "\x00"))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(metadata.Size)) //nolint:gosec // G115: sizes are non-negative
	_, _ = hash.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(metadata.ModifiedUnixNano)) //nolint:gosec // G115: bit pattern only
	_, _ = hash.Write(number[:])
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// Locate returns the cached SRT of a bitmap subtitle (by probe stream
// index) of the current source revision. Without a result it queues the
// source and returns ErrPending. The caller has authorized sourceID and
// resolved source from it.
func (s *Service) Locate(ctx context.Context, sourceID string, source domain.ProbeSource, index int) (Item, error) {
	if s == nil || ctx == nil || !domain.ValidID(sourceID) || index < 0 {
		return Item{}, ErrNotFound
	}
	input, err := probe.Open(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return Item{}, ctx.Err()
		}
		return Item{}, ErrNotFound
	}
	stamp := s.revision(sourceID, input.Metadata())
	_ = input.Close()
	value, err := s.cached(sourceID, stamp)
	if err != nil {
		s.enqueue(job{key: sourceID + "/" + stamp, sourceID: sourceID, stamp: stamp, source: source})
		return Item{}, ErrPending
	}
	for _, track := range value.Tracks {
		if track.Index == index && track.File != "" {
			return Item{RelativePath: sourceID + "/" + stamp + "/" + track.File}, nil
		}
	}
	return Item{}, ErrNotFound
}

func (s *Service) enqueue(next job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.pending[next.key] {
		return
	}
	if failed, ok := s.failures[next.key]; ok && s.config.Clock.Now().Sub(failed) < retryAfter {
		return
	}
	select {
	case s.queue <- next:
		s.pending[next.key] = true
	default:
		// A full queue drops the request; the next lookup asks again.
		s.dropped.Add(1)
	}
}

func (s *Service) dispatch(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-s.queue:
			s.running.Store(1)
			started := time.Now()
			summary, err := s.run(ctx, next)
			s.running.Store(0)
			s.mu.Lock()
			delete(s.pending, next.key)
			if err != nil && ctx.Err() == nil {
				if len(s.failures) >= maxFailures {
					clear(s.failures)
				}
				s.failures[next.key] = s.config.Clock.Now()
			}
			s.mu.Unlock()
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				s.failed.Add(1)
				s.logger.Warn("subtitle OCR job failed", "component", "subtitle_ocr", "code", errorCode(err))
			default:
				s.completed.Add(1)
				s.logger.Info("subtitle OCR job finished", "component", "subtitle_ocr", "tracks", summary.tracks, "pictures", summary.pictures, "cues", summary.cues, "durationMs", time.Since(started).Milliseconds())
			}
		}
	}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, matroska.ErrChanged):
		return "subtitle_ocr_source_changed"
	case errors.Is(err, matroska.ErrTooLarge), errors.Is(err, bitmapsub.ErrTooLarge):
		return "subtitle_ocr_too_large"
	case errors.Is(err, matroska.ErrBusy), errors.Is(err, ErrRecognizerBusy):
		return "subtitle_ocr_busy"
	case errors.Is(err, bitmapsub.ErrInvalid):
		return "subtitle_ocr_bitmap_invalid"
	}
	return "subtitle_ocr_unavailable"
}

type summary struct{ tracks, pictures, cues int }

// run processes one queued source: identify and extract its bitmap tracks
// into a private work directory, decode and recognize them, and publish all
// tracks of the revision at once.
func (s *Service) run(ctx context.Context, next job) (summary, error) {
	input, err := probe.Open(ctx, next.source)
	if err != nil {
		return summary{}, matroska.ErrChanged
	}
	defer func() { _ = input.Close() }()
	if s.revision(next.sourceID, input.Metadata()) != next.stamp {
		// Replaced since it was queued: the next lookup queues the new one.
		return summary{}, nil
	}
	if _, err := s.cached(next.sourceID, next.stamp); err == nil {
		return summary{}, nil
	}
	work, err := os.MkdirTemp(s.config.WorkRoot, "job-")
	if err != nil {
		return summary{}, ErrUnavailable
	}
	defer func() { _ = os.RemoveAll(work) }()
	tracks, err := s.extract(ctx, next.source, input, work)
	if err != nil {
		return summary{}, err
	}
	root, err := os.OpenRoot(s.config.CacheRoot)
	if err != nil {
		return summary{}, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(next.sourceID, 0o700); err != nil {
		return summary{}, ErrUnavailable
	}
	staging := next.sourceID + "/" + stagingPrefix + next.stamp + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := root.Mkdir(filepath.FromSlash(staging), 0o700); err != nil {
		return summary{}, ErrUnavailable
	}
	published := false
	defer func() {
		if !published {
			_ = root.RemoveAll(filepath.FromSlash(staging))
		}
	}()
	value := entry{Version: entryVersion, Tracks: []entryTrack{}}
	result := summary{tracks: len(tracks)}
	for _, track := range tracks {
		cues, decoded, err := s.recognizeTrack(ctx, work, track)
		if err != nil {
			return summary{}, err
		}
		data, truncated := FormatSRT(cues)
		record := entryTrack{Index: track.ID, Format: track.Format, Pictures: decoded.Events, Skipped: decoded.Skipped, Truncated: truncated || decoded.corrupt}
		for _, cue := range cues {
			if cue.Text != "" {
				record.Cues++
			}
		}
		if len(data) > 0 {
			record.File = "s" + strconv.Itoa(track.ID) + ".srt"
			if err := writeFile(root, staging+"/"+record.File, data); err != nil {
				return summary{}, err
			}
			value.Bytes += int64(len(data))
		}
		result.pictures += decoded.Events
		result.cues += record.Cues
		value.Tracks = append(value.Tracks, record)
	}
	if err := unchanged(input); err != nil {
		return summary{}, err
	}
	if err := s.publish(root, next, staging, value); err != nil {
		return summary{}, err
	}
	published = true
	return result, nil
}

// extract retries a busy mkvtoolnix runner (it is shared with on-demand
// text extraction) a few times before giving up for now.
func (s *Service) extract(ctx context.Context, source domain.ProbeSource, input *probe.Input, work string) ([]matroska.BitmapTrack, error) {
	for attempt := 1; ; attempt++ {
		tracks, err := s.extractor.ExtractBitmaps(ctx, source, input, work)
		if !errors.Is(err, matroska.ErrBusy) || attempt == busyAttempts {
			return tracks, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.config.Clock.After(time.Duration(attempt) * 2 * time.Second):
		}
	}
}

// unchanged rejects a result when the source was modified while it ran.
func unchanged(input *probe.Input) error {
	before := input.Metadata()
	info, err := input.Stdin().Stat()
	if err != nil || info.Size() != before.Size || info.ModTime().UnixNano() != before.ModifiedUnixNano {
		return matroska.ErrChanged
	}
	return nil
}

type pictureTask struct {
	key     [32]byte
	picture *image.Gray
}

type dedupSlot struct {
	done bool
	text string
	seqs []int
}

// recognizeTrack decodes one extracted track and recognizes its pictures
// with Concurrency workers, each waiting for the rate limiter and the CPU
// budget. Identical pictures are recognized once. Pictures Tesseract
// refuses are skipped; any other recognizer failure ends the job.
func (s *Service) recognizeTrack(ctx context.Context, work string, track matroska.BitmapTrack) ([]Cue, trackResult, error) {
	languages := languagesFor(track.Language, s.config.Languages)
	group, gctx := errgroup.WithContext(ctx)
	tasks := make(chan pictureTask, s.config.Concurrency)
	var mu sync.Mutex
	var cues []Cue
	slots := make(map[[32]byte]*dedupSlot)
	for range s.config.Concurrency {
		group.Go(func() error {
			for task := range tasks {
				text, err := s.recognizeOne(gctx, task.picture, languages)
				if err != nil {
					return err
				}
				mu.Lock()
				slot := slots[task.key]
				slot.done, slot.text = true, text
				for _, seq := range slot.seqs {
					cues[seq].Text = text
				}
				mu.Unlock()
			}
			return nil
		})
	}
	var decoded bitmapsub.Stats
	corrupt := false
	group.Go(func() error {
		defer close(tasks)
		visit := func(event bitmapsub.Event) error {
			if err := gctx.Err(); err != nil {
				return err
			}
			s.pictures.Add(1)
			picture := bitmapsub.OCRImage(event.Bitmap)
			mu.Lock()
			seq := len(cues)
			cues = append(cues, Cue{Start: event.Start, End: event.End})
			if picture == nil {
				mu.Unlock()
				return nil
			}
			key := pictureKey(picture)
			slot, seen := slots[key]
			if seen {
				s.reused.Add(1)
				if slot.done {
					cues[seq].Text = slot.text
				} else {
					slot.seqs = append(slot.seqs, seq)
				}
				mu.Unlock()
				return nil
			}
			slots[key] = &dedupSlot{seqs: []int{seq}}
			mu.Unlock()
			select {
			case tasks <- pictureTask{key: key, picture: picture}:
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		var err error
		decoded, err = s.decode(work, track, visit)
		if errors.Is(err, bitmapsub.ErrInvalid) || errors.Is(err, bitmapsub.ErrTooLarge) {
			// A corrupt or oversized track keeps the cues decoded before
			// the fault; the workers finish them and the track is marked
			// truncated rather than failing the whole source.
			mu.Lock()
			corrupt = true
			mu.Unlock()
			return nil
		}
		return err
	})
	if err := group.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, trackResult{}, ctx.Err()
		}
		return nil, trackResult{}, err
	}
	s.skipped.Add(uint64(decoded.Skipped)) //nolint:gosec // G115: counts are non-negative
	return cues, trackResult{Stats: decoded, corrupt: corrupt}, nil
}

// trackResult is the decoder's count for one track and whether decoding
// stopped at a corrupt or oversized structure.
type trackResult struct {
	bitmapsub.Stats
	corrupt bool
}

// recognizeOne admits one picture through the rate limiter and the CPU
// budget, then recognizes it. A refused picture yields empty text.
func (s *Service) recognizeOne(ctx context.Context, picture *image.Gray, languages []string) (string, error) {
	if err := s.limiter.Wait(ctx); err != nil {
		return "", err
	}
	if s.config.Budget != nil {
		release, err := s.config.Budget.Acquire(ctx, app.WorkCPU)
		if err != nil {
			return "", err
		}
		defer release()
	}
	text, err := s.recognizer.Recognize(ctx, picture, languages)
	if errors.Is(err, ErrPictureRejected) {
		s.rejected.Add(1)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s.recognized.Add(1)
	return cleanText(text), nil
}

func pictureKey(picture *image.Gray) [32]byte {
	hash := sha256.New()
	var size [16]byte
	binary.BigEndian.PutUint64(size[:8], uint64(picture.Rect.Dx())) //nolint:gosec // G115: dimensions are small and non-negative
	binary.BigEndian.PutUint64(size[8:], uint64(picture.Rect.Dy())) //nolint:gosec // G115: dimensions are small and non-negative
	_, _ = hash.Write(size[:])
	_, _ = hash.Write(picture.Pix)
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}

// decode streams one extracted track through its decoder.
func (s *Service) decode(work string, track matroska.BitmapTrack, visit func(bitmapsub.Event) error) (bitmapsub.Stats, error) {
	root, err := os.OpenRoot(work)
	if err != nil {
		return bitmapsub.Stats{}, ErrUnavailable
	}
	defer func() { _ = root.Close() }()
	open := func(name string) (*os.File, int64, error) {
		if !slices.Contains(track.Files, name) {
			return nil, 0, ErrUnavailable
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, 0, ErrUnavailable
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, 0, ErrUnavailable
		}
		return file, info.Size(), nil
	}
	name := "t" + strconv.Itoa(track.ID)
	if track.Format == matroska.BitmapVobSub {
		index, _, err := open(name + ".idx")
		if err != nil {
			return bitmapsub.Stats{}, err
		}
		defer func() { _ = index.Close() }()
		sub, size, err := open(name + ".sub")
		if err != nil {
			return bitmapsub.Stats{}, err
		}
		defer func() { _ = sub.Close() }()
		return bitmapsub.DecodeVobSub(index, sub, size, visit)
	}
	file, _, err := open(name + ".sup")
	if err != nil {
		return bitmapsub.Stats{}, err
	}
	defer func() { _ = file.Close() }()
	return bitmapsub.DecodePGS(readerOnly{file}, visit)
}

// readerOnly hides other methods so the decoder reads sequentially.
type readerOnly struct{ io.Reader }

// languageGroups maps a track's ISO 639 code to the Tesseract languages
// that fit it. A track in another or no language uses every configured
// language. English is added as a second language to CJK tracks when it is
// configured, as their subtitles often contain Latin words.
var languageGroups = map[string][]string{
	"eng": {"eng"}, "en": {"eng"},
	"chi": {"chi_tra", "chi_sim"}, "zho": {"chi_tra", "chi_sim"}, "zh": {"chi_tra", "chi_sim"},
	"jpn": {"jpn"}, "ja": {"jpn"},
}

func languagesFor(track string, configured []string) []string {
	var selected []string
	for _, language := range configured {
		if slices.Contains(languageGroups[track], language) {
			selected = append(selected, language)
		}
	}
	if len(selected) == 0 {
		return slices.Clone(configured)
	}
	if selected[0] != "eng" && slices.Contains(configured, "eng") {
		selected = append(selected, "eng")
	}
	return selected
}
