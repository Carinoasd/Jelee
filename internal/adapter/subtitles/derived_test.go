//go:build !windows

package subtitles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	derivedSource = "11111111-1111-4111-8111-111111111111"
	derivedTrack  = "22222222-2222-4222-8222-222222222222"
	derivedOther  = "33333333-3333-4333-8333-333333333333"
)

type derivedFixture struct {
	cache   *DerivedCache
	cacheAt string
	library string
}

func newDerivedFixture(t *testing.T, maxBytes int64) derivedFixture {
	t.Helper()
	cacheAt := filepath.Clean(t.TempDir())
	if err := os.Chmod(cacheAt, 0o700); err != nil {
		t.Fatal(err)
	}
	cache, err := NewDerivedCache(cacheAt, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return derivedFixture{cache: cache, cacheAt: cacheAt, library: filepath.Clean(t.TempDir())}
}

func (f derivedFixture) write(t *testing.T, name string, data []byte) DerivedRequest {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.library, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return DerivedRequest{SourceID: derivedSource, TrackID: derivedTrack, Root: f.library, RelativePath: name, Format: strings.TrimPrefix(filepath.Ext(name), ".")}
}

func (f derivedFixture) read(t *testing.T, relative string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.cacheAt, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digest(t *testing.T, name string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// Every legacy charset the scan records becomes the exact UTF-8 text, the
// original keeps its bytes and a second request is a cache hit.
func TestDerivedCacheConvertsLegacyCharsets(t *testing.T) {
	f := newDerivedFixture(t, 0)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cs   Charset
		lang string
		bom  bool
	}{
		{"gbk.srt", GB18030, "zh-Hans", false},
		{"big5.ass", Big5, "zh-Hant", false},
		{"sjis.srt", ShiftJIS, "ja", false},
		{"utf16.srt", UTF16LE, "ko", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := srtText(pick(tc.lang, 0, 40))
			if strings.HasSuffix(tc.name, ".ass") {
				text = assText(pick(tc.lang, 0, 40))
			}
			data := encodeWith(t, tc.cs, text)
			if tc.bom {
				data = withBOM(tc.cs, data)
			}
			req := f.write(t, tc.name, data)
			req.TrackID = derivedTrackFor(tc.name)
			req.Charset = string(tc.cs)
			before := digest(t, filepath.Join(f.library, tc.name))
			relative, err := f.cache.Locate(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.read(t, relative); !bytes.Equal(got, []byte(text)) {
				t.Fatalf("copy differs: %q", got[:min(len(got), 80)])
			}
			if digest(t, filepath.Join(f.library, tc.name)) != before {
				t.Fatal("original changed")
			}
			builds := f.cache.builds.Load()
			again, err := f.cache.Locate(ctx, req)
			if err != nil || again != relative || f.cache.builds.Load() != builds {
				t.Fatalf("second request %q %v builds %d", again, err, f.cache.builds.Load()-builds)
			}
			if !strings.HasPrefix(relative, DerivedDirectory+"/"+derivedSource+"/"+req.TrackID+"-") || !strings.HasSuffix(relative, "."+req.Format) {
				t.Fatalf("relative %q", relative)
			}
		})
	}
}

func derivedTrackFor(name string) string {
	sum := sha256.Sum256([]byte(name))
	hex := []byte("0123456789abcdef")
	id := make([]byte, 0, 36)
	for i, b := range sum[:16] {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			id = append(id, '-')
		}
		id = append(id, hex[b>>4], hex[b&15])
	}
	id[14], id[19] = '4', '8'
	return string(id)
}

// Concurrent first requests share one conversion.
func TestDerivedCacheConvertsOnce(t *testing.T) {
	f := newDerivedFixture(t, 0)
	req := f.write(t, "once.srt", encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 400))))
	req.Charset = string(GB18030)
	// The first request is held between conversion and publication until
	// every later one waits on it, so the sharing path is exercised, not
	// a cache hit after a fast conversion.
	const waiters = 15
	converting, release := make(chan struct{}), make(chan struct{})
	waiting := make(chan struct{}, waiters)
	derivedAfterConvert = func(string) {
		close(converting)
		<-release
	}
	derivedBeforeWait = func(string) { waiting <- struct{}{} }
	t.Cleanup(func() { derivedAfterConvert, derivedBeforeWait = func(string) {}, func(string) {} })
	var wg sync.WaitGroup
	results := make([]string, waiters+1)
	errs := make([]error, waiters+1)
	locate := func(i int) {
		defer wg.Done()
		results[i], errs[i] = f.cache.Locate(context.Background(), req)
	}
	wg.Add(1)
	go locate(0)
	<-converting
	for i := 1; i <= waiters; i++ {
		wg.Add(1)
		go locate(i)
	}
	for i := 0; i < waiters; i++ {
		<-waiting
	}
	close(release)
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatalf("request %d: %q %v", i, results[i], errs[i])
		}
	}
	if n := f.cache.builds.Load(); n != 1 {
		t.Fatalf("converted %d times", n)
	}
}

// What is never converted, and why.
func TestDerivedCacheRefusals(t *testing.T) {
	f := newDerivedFixture(t, 0)
	ctx := context.Background()
	gbk := encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 40)))
	for _, tc := range []struct {
		name, charset, reason string
		data                  []byte
	}{
		{"unknown.srt", "", ReasonCharsetUnknown, gbk},
		{"label.srt", "KOI8-R", ReasonCharsetUnknown, gbk},
		{"plain.srt", "UTF-8", ReasonAlreadyUTF8, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n")},
		{"doc.ttml", "GB18030", ReasonFormat, gbk},
		{"mismatch.srt", "Big5", ReasonCharsetMismatch, gbk},
		{"binary.srt", "GB18030", ReasonNotText, append(append([]byte(nil), gbk...), 0, 1, 2, 3)},
	} {
		req := f.write(t, tc.name, tc.data)
		req.Charset = tc.charset
		req.TrackID = derivedTrackFor(tc.name)
		_, err := f.cache.Locate(ctx, req)
		if got := DerivedReason(err); got != tc.reason {
			t.Errorf("%s: reason %q (%v), want %q", tc.name, got, err, tc.reason)
		}
	}
	// Stored rejections are answered without converting again.
	req := DerivedRequest{SourceID: derivedSource, TrackID: derivedTrackFor("mismatch.srt"), Root: f.library, RelativePath: "mismatch.srt", Format: "srt", Charset: "Big5"}
	builds := f.cache.builds.Load()
	if _, err := f.cache.Locate(ctx, req); DerivedReason(err) != ReasonCharsetMismatch || f.cache.builds.Load() != builds {
		t.Fatalf("stored rejection: %v, builds %d", err, f.cache.builds.Load()-builds)
	}
	if _, err := f.cache.Locate(ctx, DerivedRequest{SourceID: "x", TrackID: derivedTrack, Root: f.library, RelativePath: "a.srt", Charset: "GB18030", Format: "srt"}); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatalf("invalid source id: %v", err)
	}
	if _, err := f.cache.Locate(ctx, DerivedRequest{SourceID: derivedSource, TrackID: derivedTrack, Root: f.library, RelativePath: "../a.srt", Charset: "GB18030", Format: "srt"}); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatalf("escaping path: %v", err)
	}
	if _, err := f.cache.Locate(ctx, DerivedRequest{SourceID: derivedSource, TrackID: derivedTrack, Root: f.library, RelativePath: "missing.srt", Charset: "GB18030", Format: "srt"}); DerivedReason(err) != ReasonSourceUnavailable {
		t.Fatalf("missing original: %v", err)
	}
}

// Detection looks at the head of a file; conversion reads all of it. A
// tail that does not decode beyond 1% of the input proves the charset
// wrong for the file, and the copy is withheld.
func TestDerivedCacheReplacementThreshold(t *testing.T) {
	f := newDerivedFixture(t, 0)
	head := encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 3000)))
	cs, confidence, err := DetectCharset(context.Background(), bytes.NewReader(head), 0)
	if err != nil || cs != GB18030 || confidence.Low {
		t.Fatalf("fixture head detects as %s %+v %v", cs, confidence, err)
	}
	garbage := bytes.Repeat([]byte{0xff, '\n'}, len(head)/40)
	req := f.write(t, "tail.srt", append(append([]byte(nil), head...), garbage...))
	req.Charset = string(GB18030)
	if cs, confidence, err := DetectCharset(context.Background(), bytes.NewReader(append(append([]byte(nil), head...), garbage...)), 0); err != nil || cs != GB18030 || confidence.Low {
		t.Fatalf("whole fixture detects as %s %+v %v", cs, confidence, err)
	}
	if _, err := f.cache.Locate(context.Background(), req); DerivedReason(err) != ReasonReplacementRatio {
		t.Fatalf("garbage tail: %v", err)
	}
	// A handful of bad bytes stays below the threshold.
	req = f.write(t, "few.srt", append(append([]byte(nil), head...), 0xff, '\n'))
	req.Charset, req.TrackID = string(GB18030), derivedOther
	relative, err := f.cache.Locate(context.Background(), req)
	if err != nil || !bytes.Contains(f.read(t, relative), []byte("�")) {
		t.Fatalf("few bad bytes: %q %v", relative, err)
	}
}

// A changed original is a new copy; the old one of the track goes.
func TestDerivedCacheFollowsChangedOriginal(t *testing.T) {
	f := newDerivedFixture(t, 0)
	ctx := context.Background()
	req := f.write(t, "change.srt", encodeWith(t, ShiftJIS, srtText(pick("ja", 0, 30))))
	req.Charset = string(ShiftJIS)
	first, err := f.cache.Locate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	text := srtText(pick("ja", 5, 31))
	f.write(t, "change.srt", encodeWith(t, ShiftJIS, text))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(f.library, "change.srt"), later, later); err != nil {
		t.Fatal(err)
	}
	second, err := f.cache.Locate(ctx, req)
	if err != nil || second == first || !bytes.Equal(f.read(t, second), []byte(text)) {
		t.Fatalf("changed original: %q %v", second, err)
	}
	if _, err := os.Stat(filepath.Join(f.cacheAt, filepath.FromSlash(first))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old revision kept: %v", err)
	}
}

func TestNewDerivedCacheRefusesUnsafeRoots(t *testing.T) {
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"relative", open + "/../" + filepath.Base(open), open} {
		if _, err := NewDerivedCache(root, 0); !errors.Is(err, ErrDerivedUnavailable) {
			t.Errorf("%q: %v", root, err)
		}
	}
	private := filepath.Clean(t.TempDir())
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDerivedCache(private, MinDerivedMaxBytes-1); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatalf("tiny bound: %v", err)
	}
	if _, err := NewDerivedCache(private, 0); err != nil {
		t.Fatalf("private root: %v", err)
	}
	if err := os.Chmod(filepath.Join(private, DerivedDirectory), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDerivedCache(private, 0); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatalf("shared derived directory: %v", err)
	}
}

// The cleanup job: idle entries and stale staging of live sources, whole
// directories of deleted ones, then the size bound in least recently used
// order. Names the cache did not write stay.
func TestDerivedCacheSweep(t *testing.T) {
	f := newDerivedFixture(t, MinDerivedMaxBytes)
	ctx := context.Background()
	base := filepath.Join(f.cacheAt, DerivedDirectory)
	now := time.Now()
	entry := func(source, track string, size int, used time.Time) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(base, source), 0o700); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(base, source, track+"-"+strings.Repeat("a", 64)+".srt")
		if err := os.WriteFile(name, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, used, used); err != nil {
			t.Fatal(err)
		}
		return name
	}
	idle := entry(derivedSource, derivedTrack, 10, now.Add(-48*time.Hour))
	fresh := entry(derivedSource, derivedOther, 10, now)
	staging := filepath.Join(base, derivedSource, derivedStaging+"x")
	if err := os.WriteFile(staging, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staging, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, derivedSource, "keep.txt")
	if err := os.WriteFile(foreign, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := entry(derivedOther, derivedTrack, 10, now)
	if n, err := f.cache.CountSources(ctx); err != nil || n != 2 {
		t.Fatalf("count %d %v", n, err)
	}
	if names, err := f.cache.ListSources(ctx, derivedSource, 10); err != nil || len(names) != 1 || names[0] != derivedOther {
		t.Fatalf("list after %v %v", names, err)
	}
	if !f.cache.HasSource(derivedSource) || f.cache.HasSource(derivedTrack) {
		t.Fatal("HasSource")
	}
	live, err := f.cache.SweepSource(ctx, derivedSource, true, now.Add(-24*time.Hour))
	if err != nil || live.Revisions != 2 || live.Failed != 0 || live.Bytes != 10 {
		t.Fatalf("live sweep %+v %v", live, err)
	}
	for name, want := range map[string]bool{idle: false, staging: false, fresh: true, foreign: true} {
		if _, err := os.Stat(name); (err == nil) != want {
			t.Errorf("%s exists=%v", filepath.Base(name), err == nil)
		}
	}
	gone, err := f.cache.SweepSource(ctx, derivedOther, false, now)
	if err != nil || gone.Orphaned != 1 || gone.Bytes != 10 {
		t.Fatalf("orphan sweep %+v %v", gone, err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan kept")
	}
	if again, err := f.cache.SweepSource(ctx, derivedOther, false, now); err != nil || again.Orphaned != 0 || again.Bytes != 0 {
		t.Fatalf("repeated orphan sweep %+v %v", again, err)
	}
	// Capacity: three 400 KiB entries in a 1 MiB bound lose the oldest.
	_ = os.Remove(fresh)
	var names []string
	for i, track := range []string{derivedTrackFor("a"), derivedTrackFor("b"), derivedTrackFor("c")} {
		names = append(names, entry(derivedSource, track, 400<<10, now.Add(time.Duration(i-10)*time.Minute)))
	}
	capacity, err := f.cache.EnforceCapacity(ctx)
	if err != nil || capacity.Capacity != 1 || capacity.Bytes != 400<<10 {
		t.Fatalf("capacity %+v %v", capacity, err)
	}
	if _, err := os.Stat(names[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("least recently used entry kept")
	}
	if _, err := os.Stat(names[2]); err != nil {
		t.Fatal("recent entry removed")
	}
}

func TestDerivedCacheEdges(t *testing.T) {
	f := newDerivedFixture(t, 0)
	ctx := context.Background()
	if f.cache.CacheRoot() != f.cacheAt {
		t.Fatal("cache root")
	}
	req := f.write(t, "big.srt", nil)
	req.Charset = string(GB18030)
	if err := os.Truncate(filepath.Join(f.library, "big.srt"), DefaultConvertLimit+1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cache.Locate(ctx, req); DerivedReason(err) != ReasonTooLarge || !strings.Contains(err.Error(), ReasonTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	if text := req.String() + " " + req.GoString(); strings.Contains(text, f.library) || strings.Contains(text, "big.srt") {
		t.Fatalf("request diagnostics expose paths: %s", text)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	req = f.write(t, "cancel.srt", encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 40))))
	req.Charset = string(GB18030)
	if _, err := f.cache.Locate(cancelled, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if _, err := f.cache.Locate(ctx, req); err != nil {
		t.Fatalf("after a cancelled request: %v", err)
	}
	if _, err := f.cache.CountSources(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("count ignores cancellation")
	}
	if _, err := f.cache.ListSources(cancelled, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatal("list ignores cancellation")
	}
	if _, err := f.cache.SweepSource(cancelled, derivedSource, true, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal("sweep ignores cancellation")
	}
	if _, err := f.cache.EnforceCapacity(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("capacity ignores cancellation")
	}
	if _, err := f.cache.ListSources(ctx, "x", 1); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatal("list accepts an invalid cursor")
	}
	if _, err := f.cache.ListSources(ctx, "", 0); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatal("list accepts a zero limit")
	}
	if _, err := f.cache.SweepSource(ctx, "x", true, time.Now()); !errors.Is(err, ErrDerivedInvalid) {
		t.Fatal("sweep accepts an invalid source")
	}
	if f.cache.HasSource("x") {
		t.Fatal("HasSource accepts an invalid source")
	}
	for _, name := range []string{"short", derivedTrack + "-" + strings.Repeat("a", 64) + ".exe", derivedTrack + "-" + strings.Repeat("g", 64) + ".srt",
		"not-a-uuid-not-a-uuid-not-a-uuid-xxx-" + strings.Repeat("a", 64) + ".srt", derivedTrack + "_" + strings.Repeat("a", 64) + ".srt"} {
		if _, _, ok := entryName(name); ok {
			t.Errorf("%q taken for an entry", name)
		}
	}
	// A cache root that went away answers as unavailable.
	if err := os.RemoveAll(filepath.Join(f.cacheAt, DerivedDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cache.CountSources(ctx); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatal("count without a root")
	}
	if _, err := f.cache.ListSources(ctx, "", 1); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatal("list without a root")
	}
	if _, err := f.cache.SweepSource(ctx, derivedSource, true, time.Now()); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatal("sweep without a root")
	}
	if _, err := f.cache.EnforceCapacity(ctx); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatal("capacity without a root")
	}
	if f.cache.HasSource(derivedSource) {
		t.Fatal("HasSource without a root")
	}
	req.TrackID = derivedOther
	if _, err := f.cache.Locate(ctx, req); !errors.Is(err, ErrDerivedUnavailable) {
		t.Fatalf("locate without a root: %v", err)
	}
}

// A rejection marker is read back with a fixed code whatever it holds, and
// stale staging files go with the size bound as well.
func TestDerivedCacheRejectionMarkers(t *testing.T) {
	f := newDerivedFixture(t, 0)
	ctx := context.Background()
	gbk := encodeWith(t, GB18030, srtText(pick("zh-Hans", 0, 40)))
	req := f.write(t, "marker.srt", gbk)
	req.Charset = "Big5"
	if _, err := f.cache.Locate(ctx, req); DerivedReason(err) != ReasonCharsetMismatch {
		t.Fatalf("first: %v", err)
	}
	markers, _ := filepath.Glob(filepath.Join(f.cacheAt, DerivedDirectory, derivedSource, "*."+derivedRejected))
	if len(markers) != 1 {
		t.Fatalf("markers %v", markers)
	}
	if err := os.WriteFile(markers[0], []byte("<script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cache.Locate(ctx, req); DerivedReason(err) != ReasonReplacementRatio {
		t.Fatalf("unknown marker: %v", err)
	}
	// A conversion after the rejection (a corrected charset) replaces it.
	req.Charset = string(GB18030)
	if _, err := f.cache.Locate(ctx, req); err != nil {
		t.Fatal(err)
	}
	if markers, _ := filepath.Glob(filepath.Join(f.cacheAt, DerivedDirectory, derivedSource, "*."+derivedRejected)); len(markers) != 0 {
		t.Fatalf("old rejection kept: %v", markers)
	}
}

// An original rewritten during the conversion is not cached under the key
// of the bytes it had when opened.
func TestDerivedCacheSkipsAnOriginalChangedWhileConverting(t *testing.T) {
	f := newDerivedFixture(t, 0)
	req := f.write(t, "moving.srt", encodeWith(t, EUCKR, srtText(pick("ko", 0, 40))))
	req.Charset = string(EUCKR)
	name := filepath.Join(f.library, "moving.srt")
	derivedAfterConvert = func(string) {
		later := time.Now().Add(time.Hour)
		_ = os.Chtimes(name, later, later)
	}
	t.Cleanup(func() { derivedAfterConvert = func(string) {} })
	if _, err := f.cache.Locate(context.Background(), req); DerivedReason(err) != ReasonSourceChanged {
		t.Fatalf("changed while converting: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.cacheAt, DerivedDirectory, derivedSource)); len(entries) != 0 {
		t.Fatalf("cached a copy of changing bytes: %v", entries)
	}
	derivedAfterConvert = func(string) {}
	if _, err := f.cache.Locate(context.Background(), req); err != nil {
		t.Fatalf("next request: %v", err)
	}
}
