package subtitleocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	testSource = "00000000-0000-4000-8000-000000000001"
	otherID    = "00000000-0000-4000-8000-000000000002"
)

var white = bitmapsubtest.Style{Fill: color.NRGBA{255, 255, 255, 255}, Outline: color.NRGBA{0, 0, 0, 255}, OutlineWidth: 2, Padding: 2}

func render(t *testing.T, text string) *image.NRGBA {
	t.Helper()
	img, err := bitmapsubtest.RenderText(nil, text, 36, white)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// samplePGS has three pictures; the first and third are identical.
func samplePGS(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	hello, world := render(t, "Hello"), render(t, "World")
	cues := []bitmapsubtest.Cue{
		{Start: time.Second, End: 2 * time.Second, Image: hello, X: 100, Y: 900},
		{Start: 3 * time.Second, End: 4 * time.Second, Image: world, X: 100, Y: 900},
		{Start: 5 * time.Second, End: 6500 * time.Millisecond, Image: hello, X: 100, Y: 900},
	}
	if err := bitmapsubtest.EncodePGS(&out, 1920, 1080, cues); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func sampleVobSub(t *testing.T) (index, sub []byte) {
	t.Helper()
	var idx, data bytes.Buffer
	cues := []bitmapsubtest.Cue{{Start: 1500 * time.Millisecond, End: 3 * time.Second, Image: render(t, "DVD"), X: 60, Y: 400}}
	if err := bitmapsubtest.EncodeVobSub(&idx, &data, 720, 480, cues); err != nil {
		t.Fatal(err)
	}
	return idx.Bytes(), data.Bytes()
}

type fakeExtractor struct {
	mu     sync.Mutex
	calls  atomic.Int64
	tracks []matroska.BitmapTrack
	files  map[string][]byte
	err    error
	errs   []error
	block  chan struct{}
}

func (f *fakeExtractor) ExtractBitmaps(ctx context.Context, _ domain.ProbeSource, input *probe.Input, directory string) ([]matroska.BitmapTrack, error) {
	f.calls.Add(1)
	if input == nil {
		return nil, matroska.ErrUnavailable
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	for name, data := range f.files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			return nil, err
		}
	}
	return slices.Clone(f.tracks), nil
}

// fakeRecognizer names each picture by its size and records concurrency.
type fakeRecognizer struct {
	calls     atomic.Int64
	active    atomic.Int64
	peak      atomic.Int64
	gate      chan struct{}
	err       error
	reject    bool
	languages [][]string
	mu        sync.Mutex
}

func (f *fakeRecognizer) Recognize(ctx context.Context, picture *image.Gray, languages []string) (string, error) {
	f.calls.Add(1)
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for peak := f.peak.Load(); active > peak; peak = f.peak.Load() {
		if f.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	f.mu.Lock()
	f.languages = append(f.languages, slices.Clone(languages))
	f.mu.Unlock()
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if f.err != nil {
		return "", f.err
	}
	if f.reject {
		return "", ErrPictureRejected
	}
	return fmt.Sprintf("  picture\t%dx%d \n\n", picture.Rect.Dx(), picture.Rect.Dy()), nil
}

type env struct {
	service    *Service
	extractor  *fakeExtractor
	recognizer *fakeRecognizer
	clock      *fakeClock
	cache      string
	work       string
	source     domain.ProbeSource
	media      string
}

func privateDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newEnv(t *testing.T, config Config) env {
	t.Helper()
	library := t.TempDir()
	media := filepath.Join(library, "Film.mkv")
	if err := os.WriteFile(media, []byte("synthetic matroska stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := env{extractor: &fakeExtractor{files: map[string][]byte{"t3.sup": samplePGS(t)}, tracks: []matroska.BitmapTrack{{ID: 3, Format: matroska.BitmapPGS, Language: "eng", Files: []string{"t3.sup"}}}},
		recognizer: &fakeRecognizer{}, clock: newFakeClock(), cache: privateDir(t), work: privateDir(t), source: domain.ProbeSource{RootPath: library, RelativePath: "Film.mkv"}, media: media}
	config.CacheRoot, config.WorkRoot, config.Clock = e.cache, e.work, e.clock
	if config.Identity == "" {
		config.Identity = "recognizer-a"
	}
	if config.Languages == nil {
		config.Languages = []string{"eng"}
	}
	if config.PicturesPerMinute == 0 {
		config.PicturesPerMinute = 600
	}
	if config.Concurrency == 0 {
		config.Concurrency = 1
	}
	if config.QueueSize == 0 {
		config.QueueSize = 4
	}
	service, err := New(e.extractor, e.recognizer, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	e.service = service
	return e
}

// locate polls until the OCR result exists or fails.
func (e env) locate(t *testing.T, index int) (Item, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		item, err := e.service.Locate(context.Background(), testSource, e.source, index)
		if !errors.Is(err, ErrPending) || time.Now().After(deadline) {
			return item, err
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func digest(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestServiceQueuesRecognizesAndServesAnSRT(t *testing.T) {
	e := newEnv(t, Config{})
	before := digest(t, e.media)
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatalf("first lookup: %v", err)
	}
	item, err := e.locate(t, 3)
	if err != nil || !strings.HasPrefix(item.RelativePath, testSource+"/") || !strings.HasSuffix(item.RelativePath, "/s3.srt") {
		t.Fatalf("%v %+v", err, item)
	}
	data, err := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) != 13 || lines[0] != "1" || lines[1] != "00:00:01,000 --> 00:00:02,000" || !strings.HasPrefix(lines[2], "picture ") ||
		lines[5] != "00:00:03,000 --> 00:00:04,000" || lines[9] != "00:00:05,000 --> 00:00:06,500" || lines[10] != lines[2] {
		t.Fatalf("srt:\n%s", data)
	}
	// The repeated picture was recognized once.
	stats := e.service.Stats()
	if e.recognizer.calls.Load() != 2 || stats.Reused != 1 || stats.Pictures != 3 || stats.Recognized != 2 || stats.Completed != 1 || stats.Queued != 0 {
		t.Fatalf("calls %d stats %+v", e.recognizer.calls.Load(), stats)
	}
	if digest(t, e.media) != before {
		t.Fatal("original changed")
	}
	// Other indices of a processed source are not found and not requeued.
	for _, index := range []int{0, 2, 4} {
		if _, err := e.service.Locate(context.Background(), testSource, e.source, index); err != ErrNotFound {
			t.Fatalf("index %d: %v", index, err)
		}
	}
	if e.extractor.calls.Load() != 1 {
		t.Fatal("source extracted again")
	}
	if entries, _ := os.ReadDir(e.work); len(entries) != 0 {
		t.Fatal("work directory not emptied")
	}
	// A modified source is a new revision; the old one is removed.
	if err := os.WriteFile(e.media, []byte("synthetic matroska stand-in, modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal("changed source served stale OCR")
	}
	second, err := e.locate(t, 3)
	if err != nil || second.RelativePath == item.RelativePath {
		t.Fatalf("%v %+v", err, second)
	}
	if revisions, _ := os.ReadDir(filepath.Join(e.cache, testSource)); len(revisions) != 1 {
		t.Fatal("old revision kept")
	}
	if err := e.service.Clear(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(e.cache); len(entries) != 0 {
		t.Fatal("clear left entries")
	}
}

func TestServiceRecognizesVobSubWithTrackLanguages(t *testing.T) {
	e := newEnv(t, Config{Languages: []string{"chi_tra", "eng", "jpn"}})
	index, sub := sampleVobSub(t)
	e.extractor.files = map[string][]byte{"t2.idx": index, "t2.sub": sub}
	e.extractor.tracks = []matroska.BitmapTrack{{ID: 2, Format: matroska.BitmapVobSub, Language: "jpn", Files: []string{"t2.idx", "t2.sub"}}}
	item, err := e.locate(t, 2)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	// VobSub stop times are multiples of 1024/90000 s, about 11.4 ms.
	if !strings.HasPrefix(string(data), "1\n00:00:01,500 --> 00:00:03,0") || !strings.Contains(string(data), "\npicture ") {
		t.Fatalf("srt %q", data)
	}
	if got := e.recognizer.languages[0]; !slices.Equal(got, []string{"jpn", "eng"}) {
		t.Fatalf("languages %q", got)
	}
	for track, want := range map[string][]string{"chi": {"chi_tra", "eng"}, "eng": {"eng"}, "fre": {"chi_tra", "eng", "jpn"}, "": {"chi_tra", "eng", "jpn"}} {
		if got := languagesFor(track, []string{"chi_tra", "eng", "jpn"}); !slices.Equal(got, want) {
			t.Errorf("%q: %q", track, got)
		}
	}
	if got := languagesFor("chi", []string{"chi_sim", "chi_tra"}); !slices.Equal(got, []string{"chi_sim", "chi_tra"}) {
		t.Fatalf("chinese without english %q", got)
	}
}

func TestServiceBoundsConcurrency(t *testing.T) {
	e := newEnv(t, Config{Concurrency: 2})
	var pictures []bitmapsubtest.Cue
	for i := range 8 {
		pictures = append(pictures, bitmapsubtest.Cue{Start: time.Duration(i*2) * time.Second, End: time.Duration(i*2+1) * time.Second, Image: render(t, strings.Repeat("W", i+1)), X: 10, Y: 900})
	}
	var out bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&out, 1920, 1080, pictures); err != nil {
		t.Fatal(err)
	}
	e.extractor.files["t3.sup"] = out.Bytes()
	e.recognizer.gate = make(chan struct{})
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.recognizer.active.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if e.recognizer.active.Load() != 2 {
		t.Fatal("more recognitions than the configured concurrency")
	}
	if stats := e.service.Stats(); stats.Running != 1 || stats.Queued != 0 {
		t.Fatalf("stats %+v", stats)
	}
	close(e.recognizer.gate)
	if _, err := e.locate(t, 3); err != nil {
		t.Fatal(err)
	}
	if e.recognizer.peak.Load() != 2 || e.recognizer.calls.Load() != 8 {
		t.Fatalf("peak %d calls %d", e.recognizer.peak.Load(), e.recognizer.calls.Load())
	}
}

func TestServiceRateLimitsPicturesWithTheClock(t *testing.T) {
	e := newEnv(t, Config{PicturesPerMinute: 1})
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	// One picture per minute: the second distinct picture waits for the clock.
	waitFor(t, func() bool { return e.recognizer.calls.Load() == 1 && e.clock.Pending() == 1 })
	time.Sleep(20 * time.Millisecond)
	if e.recognizer.calls.Load() != 1 {
		t.Fatal("second picture recognized inside the minute")
	}
	e.clock.Advance(time.Minute)
	if _, err := e.locate(t, 3); err != nil {
		t.Fatal(err)
	}
	if stats := e.service.Stats(); stats.Throttled != 1 || stats.Recognized != 2 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestServiceCloseCancelsTheRunningJobAndCleansUp(t *testing.T) {
	e := newEnv(t, Config{})
	e.recognizer.gate = make(chan struct{})
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.recognizer.active.Load() == 1 })
	staging, _ := filepath.Glob(filepath.Join(e.cache, testSource, stagingPrefix+"*"))
	if len(staging) != 1 {
		t.Fatal("no staging directory while running")
	}
	if err := e.service.Close(); err != nil {
		t.Fatal(err)
	}
	if staging, _ := filepath.Glob(filepath.Join(e.cache, testSource, stagingPrefix+"*")); len(staging) != 0 {
		t.Fatal("staging left after cancellation")
	}
	if entries, _ := os.ReadDir(e.work); len(entries) != 0 {
		t.Fatal("work files left after cancellation")
	}
	if stats := e.service.Stats(); stats.Completed != 0 || stats.Failed != 0 || stats.Running != 0 {
		t.Fatalf("stats %+v", stats)
	}
	// A closed service queues nothing.
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending || e.service.Stats().Queued != 0 {
		t.Fatal("closed service queued work")
	}
	var absent *Service
	if absent.Close() != nil || absent.Stats() != (Stats{}) {
		t.Fatal("nil service")
	}
	if _, err := absent.Locate(context.Background(), testSource, e.source, 3); err != ErrNotFound {
		t.Fatal("nil service located")
	}
}

func TestServiceQueueIsBoundedAndFailuresBackOff(t *testing.T) {
	e := newEnv(t, Config{QueueSize: 1})
	e.extractor.block = make(chan struct{})
	ctx := context.Background()
	if _, err := e.service.Locate(ctx, testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.extractor.calls.Load() == 1 })
	// The running job's source is not queued twice; one more fits; the
	// next is dropped.
	_, _ = e.service.Locate(ctx, testSource, e.source, 3)
	library := t.TempDir()
	for i, name := range []string{"a.mkv", "b.mkv"} {
		if err := os.WriteFile(filepath.Join(library, name), []byte{byte(i)}, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = e.service.Locate(ctx, []string{otherID, "00000000-0000-4000-8000-000000000003"}[i], domain.ProbeSource{RootPath: library, RelativePath: name}, 0)
	}
	if stats := e.service.Stats(); stats.Dropped != 1 || stats.Queued != 1 || stats.Running != 1 {
		t.Fatalf("stats %+v", stats)
	}
	close(e.extractor.block)
	waitFor(t, func() bool { s := e.service.Stats(); return s.Queued == 0 && s.Running == 0 })

	// A failing extraction is not retried before the back-off ends.
	failing := newEnv(t, Config{})
	failing.extractor.err = matroska.ErrTooLarge
	if _, err := failing.service.Locate(ctx, testSource, failing.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return failing.service.Stats().Failed == 1 })
	_, _ = failing.service.Locate(ctx, testSource, failing.source, 3)
	time.Sleep(20 * time.Millisecond)
	if failing.extractor.calls.Load() != 1 {
		t.Fatal("failed source retried immediately")
	}
	failing.clock.Advance(retryAfter)
	failing.extractor.mu.Lock()
	failing.extractor.err = nil
	failing.extractor.mu.Unlock()
	if _, err := failing.locate(t, 3); err != nil {
		t.Fatalf("retry after back-off: %v", err)
	}
	if errorCode(matroska.ErrTooLarge) != "subtitle_ocr_too_large" || errorCode(matroska.ErrChanged) != "subtitle_ocr_source_changed" || errorCode(ErrRecognizerBusy) != "subtitle_ocr_busy" || errorCode(errors.New("x")) != "subtitle_ocr_unavailable" {
		t.Fatal("error codes")
	}
}

func TestServiceRetriesABusyExtractorAndSkipsRejectedPictures(t *testing.T) {
	e := newEnv(t, Config{})
	e.extractor.errs = []error{matroska.ErrBusy}
	e.recognizer.reject = true
	if _, err := e.service.Locate(context.Background(), testSource, e.source, 3); err != ErrPending {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.clock.Pending() == 1 })
	e.clock.Advance(2 * time.Second)
	// Every picture was refused: nothing to serve, and no requeue.
	if _, err := e.locate(t, 3); err != ErrNotFound {
		t.Fatalf("%v", err)
	}
	if stats := e.service.Stats(); stats.Rejected != 2 || stats.Completed != 1 || e.extractor.calls.Load() != 2 {
		t.Fatalf("stats %+v calls %d", stats, e.extractor.calls.Load())
	}
}

func TestServiceKeepsCuesBeforeACorruptStructure(t *testing.T) {
	e := newEnv(t, Config{})
	data := samplePGS(t)
	// Cut the stream inside the last display set's segments.
	e.extractor.files["t3.sup"] = data[:len(data)-40]
	item, err := e.locate(t, 3)
	if err != nil {
		t.Fatal(err)
	}
	srt, _ := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	if !strings.HasPrefix(string(srt), "1\n00:00:01,000") {
		t.Fatalf("srt %q", srt)
	}
	root, _ := os.OpenRoot(e.cache)
	defer func() { _ = root.Close() }()
	value, err := readEntry(root, strings.TrimSuffix(item.RelativePath, "/s3.srt")+"/"+entryFile)
	if err != nil || len(value.Tracks) != 1 || !value.Tracks[0].Truncated {
		t.Fatalf("%v %+v", err, value)
	}
	// A recognizer outage fails the job instead.
	broken := newEnv(t, Config{})
	broken.recognizer.err = ErrUnavailable
	_, _ = broken.service.Locate(context.Background(), testSource, broken.source, 3)
	waitFor(t, func() bool { return broken.service.Stats().Failed == 1 })
}

type countingBudget struct{ acquired atomic.Int64 }

func (b *countingBudget) Acquire(_ context.Context, class app.WorkClass) (func(), error) {
	if class != app.WorkCPU {
		return nil, errors.New("unexpected class")
	}
	b.acquired.Add(1)
	return func() {}, nil
}

func TestServiceConfigurationAndBudget(t *testing.T) {
	budget := &countingBudget{}
	e := newEnv(t, Config{Budget: budget})
	if _, err := e.locate(t, 3); err != nil {
		t.Fatal(err)
	}
	if budget.acquired.Load() != 2 {
		t.Fatalf("budget acquired %d times", budget.acquired.Load())
	}
	valid := Config{CacheRoot: privateDir(t), WorkRoot: privateDir(t), Languages: []string{"eng"}, PicturesPerMinute: 10, Concurrency: 1, QueueSize: 1, Identity: "x"}
	for name, change := range map[string]func(*Config){
		"cache":       func(c *Config) { c.CacheRoot = "relative" },
		"work":        func(c *Config) { c.WorkRoot = t.TempDir() + "/missing" },
		"identity":    func(c *Config) { c.Identity = "" },
		"languages":   func(c *Config) { c.Languages = nil },
		"concurrency": func(c *Config) { c.Concurrency = MaxConcurrency + 1 },
		"queue":       func(c *Config) { c.QueueSize = 0 },
		"rate":        func(c *Config) { c.PicturesPerMinute = 0 },
		"bytes":       func(c *Config) { c.MaxCacheBytes = 1 },
	} {
		config := valid
		change(&config)
		if _, err := New(&fakeExtractor{}, &fakeRecognizer{}, config); err != ErrUnavailable {
			t.Errorf("%s accepted", name)
		}
	}
	public := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	config := valid
	config.CacheRoot = public
	if _, err := New(&fakeExtractor{}, &fakeRecognizer{}, config); err != ErrUnavailable {
		t.Fatal("group-readable cache accepted")
	}
	if _, err := New(nil, &fakeRecognizer{}, valid); err != ErrUnavailable {
		t.Fatal("nil extractor")
	}
	service, err := New(&fakeExtractor{}, &fakeRecognizer{}, valid)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Close()
}
