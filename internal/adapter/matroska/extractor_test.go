package matroska

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

const testSource = "00000000-0000-4000-8000-000000000001"

const identifyJSON = `{"attachments":[{"content_type":"font/ttf","file_name":"Sans.ttf","id":1,"size":5},{"content_type":"image/png","file_name":"cover.png","id":2,"size":4},{"content_type":"application/octet-stream","file_name":"Serif.OTF","id":3,"size":6}],
"container":{"recognized":true,"supported":true,"type":"Matroska"},
"tracks":[{"id":0,"type":"video","properties":{"codec_id":"V_MPEG4/ISO/AVC"}},{"id":1,"type":"subtitles","properties":{"codec_id":"S_TEXT/UTF8"}},{"id":2,"type":"subtitles","properties":{"codec_id":"S_TEXT/ASS"}},{"id":3,"type":"subtitles","properties":{"codec_id":"S_HDMV/PGS"}},{"id":4,"type":"subtitles","properties":{"codec_id":"D_WEBVTT/SUBTITLES"}}]}`

type fakeTool struct {
	mu       sync.Mutex
	calls    atomic.Int64
	output   string
	err      error
	plans    []sandbox.Extraction
	files    map[string]string
	block    chan struct{}
	onRun    func()
	skipFile string
	// extra names are written in addition to t<id>/a<id> (VobSub pairs).
	extra []string
}

func (f *fakeTool) Run(ctx context.Context, request process.ToolRequest) (process.Result, error) {
	f.calls.Add(1)
	if request.Stdin == nil {
		return process.Result{}, process.ErrInvalid
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return process.Result{}, process.ErrCancelled
		}
	}
	if f.onRun != nil {
		f.onRun()
	}
	if f.err != nil {
		return process.Result{}, f.err
	}
	if request.Collect == nil {
		return process.Result{Stdout: []byte(f.output)}, nil
	}
	f.mu.Lock()
	f.plans = append(f.plans, request.Extraction)
	f.mu.Unlock()
	directory, err := os.MkdirTemp("", "fake-extract-")
	if err != nil {
		return process.Result{}, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	write := func(name string) {
		if name == f.skipFile {
			return
		}
		content := f.files[name]
		if content == "" {
			content = "content of " + name
		}
		_ = os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600)
	}
	for _, id := range request.Extraction.Tracks {
		write(sandbox.ExtractTrackName(id))
	}
	for _, id := range request.Extraction.Attachments {
		write(sandbox.ExtractAttachmentName(id))
	}
	for _, name := range f.extra {
		write(name)
	}
	if err := request.Collect(directory); err != nil {
		return process.Result{}, err
	}
	return process.Result{}, nil
}

type testEnv struct {
	extractor        *Extractor
	identify, output *fakeTool
	cache            string
	source           domain.ProbeSource
	media            string
}

func newEnv(t *testing.T, config Config) testEnv {
	t.Helper()
	library := t.TempDir()
	media := filepath.Join(library, "Film.mkv")
	if err := os.WriteFile(media, []byte("synthetic matroska stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if config.CacheRoot == "" {
		config.CacheRoot = cache
	}
	identify, extract := &fakeTool{output: identifyJSON}, &fakeTool{files: map[string]string{}}
	extractor, err := New(identify, extract, config)
	if err != nil {
		t.Fatal(err)
	}
	return testEnv{extractor: extractor, identify: identify, output: extract, cache: config.CacheRoot, source: domain.ProbeSource{RootPath: library, RelativePath: "Film.mkv"}, media: media}
}

func (e testEnv) read(t *testing.T, item Item) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseIdentifyAcceptsOnlyPositionalMatroskaIdentification(t *testing.T) {
	container, err := ParseIdentify([]byte(identifyJSON))
	if err != nil || len(container.Tracks) != 5 || len(container.Attachments) != 3 {
		t.Fatalf("parse: %v %+v", err, container)
	}
	if format, ok := container.Tracks[1].TextFormat(); !ok || format != "srt" {
		t.Fatal("SubRip track not extractable")
	}
	for _, index := range []int{0, 3, 4} {
		if _, ok := container.Tracks[index].TextFormat(); ok {
			t.Fatalf("track %d offered as text", index)
		}
	}
	if !container.Attachments[0].Font() || container.Attachments[1].Font() || !container.Attachments[2].Font() {
		t.Fatal("font classification")
	}
	for name, document := range map[string]string{
		"empty":       "",
		"not json":    "{",
		"trailing":    identifyJSON + "{}",
		"mp4":         `{"container":{"recognized":true,"supported":true,"type":"QuickTime/MP4"},"tracks":[]}`,
		"unsupported": `{"container":{"recognized":true,"supported":false,"type":"Matroska"},"tracks":[]}`,
		"gap":         `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[{"id":1,"type":"video","properties":{}}]}`,
		"attachment":  `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[],"attachments":[{"id":2,"file_name":"a.ttf","size":1}]}`,
		"no size":     `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[],"attachments":[{"id":1,"file_name":"a.ttf"}]}`,
		"oversize":    strings.Repeat(" ", MaxIdentifyOutput+1),
	} {
		if _, err := ParseIdentify([]byte(document)); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	unsafe := Attachment{FileName: "../evil.ttf", ContentType: "font/ttf", Size: 1}
	if unsafe.Font() {
		t.Fatal("path-like attachment name accepted")
	}
}

func TestExtractorCachesTextSubtitlesAndFontsOnce(t *testing.T) {
	env := newEnv(t, Config{})
	env.output.files["t1"] = "1\n00:00:00,000 --> 00:00:01,000\nHello\n"
	before, _ := os.ReadFile(env.media)
	ctx := context.Background()
	item, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1)
	if err != nil || item.Format != "srt" || !strings.HasSuffix(item.RelativePath, "/t1.srt") || !strings.HasPrefix(item.RelativePath, testSource+"/") {
		t.Fatalf("locate: %v %+v", err, item)
	}
	if env.read(t, item) != env.output.files["t1"] {
		t.Fatal("cached subtitle differs from the extracted bytes")
	}
	plan := env.output.plans[0]
	if len(plan.Tracks) != 2 || plan.Tracks[0] != 1 || plan.Tracks[1] != 2 || len(plan.Attachments) != 2 || plan.Attachments[0] != 1 || plan.Attachments[1] != 3 {
		t.Fatalf("plan %+v: only text tracks and fonts are extracted", plan)
	}
	for _, query := range []struct {
		kind   Kind
		index  int
		format string
	}{{KindSubtitle, 2, "ass"}, {KindAttachment, 1, "ttf"}, {KindAttachment, 3, "otf"}, {KindAttachmentStream, 5, "ttf"}, {KindAttachmentStream, 7, "otf"}} {
		item, err := env.extractor.Locate(ctx, testSource, env.source, query.kind, query.index)
		if err != nil || item.Format != query.format {
			t.Fatalf("locate %+v: %v %+v", query, err, item)
		}
	}
	for _, query := range []struct {
		kind  Kind
		index int
	}{{KindSubtitle, 0}, {KindSubtitle, 3}, {KindSubtitle, 4}, {KindAttachment, 2}, {KindAttachmentStream, 6}, {KindAttachmentStream, 0}} {
		if _, err := env.extractor.Locate(ctx, testSource, env.source, query.kind, query.index); err != ErrNotFound {
			t.Fatalf("locate %+v: %v", query, err)
		}
	}
	if env.identify.calls.Load() != 1 || env.output.calls.Load() != 1 {
		t.Fatalf("identify %d extract %d: the cache was not reused", env.identify.calls.Load(), env.output.calls.Load())
	}
	after, _ := os.ReadFile(env.media)
	if string(after) != string(before) {
		t.Fatal("source bytes changed")
	}
	// A new revision replaces the old one.
	old := filepath.Join(env.cache, filepath.FromSlash(item.RelativePath))
	if err := os.WriteFile(env.media, []byte("a changed synthetic stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) || env.output.calls.Load() != 2 {
		t.Fatal("old revision not replaced")
	}
	if err := env.extractor.Clear(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(env.cache); len(entries) != 0 {
		t.Fatal("clear left cache entries")
	}
}

func TestExtractorRefusesBadInputsAndMapsToolErrors(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, Config{})
	for _, call := range []struct {
		id    string
		kind  Kind
		index int
	}{{"not-an-id", KindSubtitle, 1}, {testSource, 0, 1}, {testSource, KindSubtitle, -1}} {
		if _, err := env.extractor.Locate(ctx, call.id, env.source, call.kind, call.index); err != ErrNotFound {
			t.Fatalf("%+v accepted", call)
		}
	}
	missing := env.source
	missing.RelativePath = "missing.mkv"
	if _, err := env.extractor.Locate(ctx, testSource, missing, KindSubtitle, 1); err != ErrNotFound {
		t.Fatal("missing source")
	}
	for err, want := range map[error]error{process.ErrBusy: ErrBusy, process.ErrTimeout: context.DeadlineExceeded, process.ErrCancelled: context.Canceled, process.ErrExit: ErrNotFound, process.ErrSandboxUnavailable: ErrUnavailable} {
		env := newEnv(t, Config{})
		env.identify.err = err
		if _, got := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); !errors.Is(got, want) {
			t.Fatalf("%v mapped to %v", err, got)
		}
	}
	// A file that is not Matroska is cached as an empty entry.
	env = newEnv(t, Config{})
	env.identify.output = `{"container":{"recognized":true,"supported":true,"type":"QuickTime/MP4"},"tracks":[]}`
	for range 2 {
		if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != ErrNotFound {
			t.Fatal(err)
		}
	}
	if env.identify.calls.Load() != 1 || env.output.calls.Load() != 0 {
		t.Fatal("non-Matroska source identified again or extracted")
	}
	// A track that wrote nothing is dropped; the others stay.
	env = newEnv(t, Config{})
	env.output.skipFile = "t2"
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 2); err != ErrNotFound {
		t.Fatal("missing output offered")
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != nil {
		t.Fatal(err)
	}
}

func TestExtractorBoundsAndSourceChanges(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, Config{MaxEntryBytes: 1 << 20, MaxCacheBytes: 1 << 20})
	env.output.files["t1"] = strings.Repeat("x", 1<<20)
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != ErrTooLarge {
		t.Fatalf("oversized entry: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(env.cache, testSource)); len(entries) != 0 {
		t.Fatal("oversized entry left staging files")
	}
	env = newEnv(t, Config{})
	env.output.onRun = func() { _ = os.WriteFile(env.media, []byte("rewritten during extraction"), 0o600) }
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != ErrChanged {
		t.Fatalf("changed source: %v", err)
	}
	if _, err := New(nil, &fakeTool{}, Config{CacheRoot: t.TempDir()}); err != ErrUnavailable {
		t.Fatal("missing runner accepted")
	}
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, config := range []Config{{CacheRoot: "relative"}, {CacheRoot: open}, {CacheRoot: filepath.Join(open, "missing")}, {CacheRoot: t.TempDir(), MaxEntryBytes: 1}, {CacheRoot: t.TempDir(), MaxCacheBytes: 1 << 20, MaxEntryBytes: 2 << 20}} {
		if _, err := New(&fakeTool{}, &fakeTool{}, config); err != ErrUnavailable {
			t.Fatalf("config %+v accepted", config)
		}
	}
}

func TestExtractorEvictsLeastRecentlyUsedAndStaleStaging(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, Config{MaxEntryBytes: 1 << 20, MaxCacheBytes: 1 << 20})
	env.output.files["t1"] = strings.Repeat("a", 400<<10)
	sources := []string{"00000000-0000-4000-8000-00000000000a", "00000000-0000-4000-8000-00000000000b", "00000000-0000-4000-8000-00000000000c"}
	stale := filepath.Join(env.cache, sources[0], stagingPrefix+"crashed")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleStaging)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	for _, id := range sources {
		if _, err := env.extractor.Locate(ctx, id, env.source, KindSubtitle, 1); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale staging directory kept")
	}
	remaining := 0
	for _, id := range sources {
		if entries, _ := os.ReadDir(filepath.Join(env.cache, id)); len(entries) > 0 {
			remaining++
		}
	}
	if remaining != 2 {
		t.Fatalf("%d revisions remain; the least recently used one must go", remaining)
	}
	if entries, _ := os.ReadDir(filepath.Join(env.cache, sources[0])); len(entries) != 0 {
		t.Fatal("the oldest revision survived eviction")
	}
}

func TestExtractorSharesOneRunPerRevision(t *testing.T) {
	env := newEnv(t, Config{})
	env.output.block = make(chan struct{})
	var wait sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := env.extractor.Locate(context.Background(), testSource, env.source, KindSubtitle, 1)
			errs <- err
		}()
	}
	for env.output.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(env.output.block)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if env.identify.calls.Load() != 1 || env.output.calls.Load() != 1 {
		t.Fatalf("identify %d extract %d for concurrent callers", env.identify.calls.Load(), env.output.calls.Load())
	}
	// A cancelled caller does not poison the next one.
	env = newEnv(t, Config{})
	env.output.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1)
		done <- err
	}()
	for env.output.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled extraction: %v", err)
	}
	close(env.output.block)
	if _, err := env.extractor.Locate(context.Background(), testSource, env.source, KindSubtitle, 1); err != nil {
		t.Fatal(err)
	}
}
