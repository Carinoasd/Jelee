package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

var (
	fetchPNG  = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 200)...)
	fetchJPEG = append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{1}, 100)...)
)

const (
	fetchTMDBURL = "https://image.tmdb.org/t/p/original/abcDEF_123.png"
	fetchNFOURL  = "https://img.example/art/poster.png?token=secret-token"
)

// fetchCall is what the fake transport saw for one Stream call.
type fetchCall struct {
	url     string
	options outbound.StreamOptions
	at      time.Time
}

// fakeFetchTransport stands in for *outbound.Client; respond builds each
// response and may block on ctx like a real slow peer.
type fakeFetchTransport struct {
	mu      sync.Mutex
	calls   []fetchCall
	active  map[string]int
	peak    map[string]int
	total   int
	peakAll int
	respond func(ctx context.Context, call int, target string) (*outbound.StreamResponse, error)
}

func newFakeFetchTransport(respond func(context.Context, int, string) (*outbound.StreamResponse, error)) *fakeFetchTransport {
	return &fakeFetchTransport{respond: respond, active: map[string]int{}, peak: map[string]int{}}
}

func (t *fakeFetchTransport) Stream(ctx context.Context, target string, o outbound.StreamOptions) (*outbound.StreamResponse, error) {
	host := strings.SplitN(strings.TrimPrefix(target, "https://"), "/", 2)[0]
	t.mu.Lock()
	t.calls = append(t.calls, fetchCall{target, o, time.Now()})
	call := len(t.calls)
	t.active[host]++
	t.total++
	t.peak[host] = max(t.peak[host], t.active[host])
	t.peakAll = max(t.peakAll, t.total)
	t.mu.Unlock()
	r, err := t.respond(ctx, call, target)
	if err != nil || r == nil {
		t.leave(host)
		return r, err
	}
	r.Body = &fakeFetchBody{r: r.Body, close: func() { t.leave(host) }}
	return r, nil
}

func (t *fakeFetchTransport) leave(host string) {
	t.mu.Lock()
	t.active[host]--
	t.total--
	t.mu.Unlock()
}

func (t *fakeFetchTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

type fakeFetchBody struct {
	r     io.ReadCloser
	once  sync.Once
	close func()
}

func (b *fakeFetchBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *fakeFetchBody) Close() error {
	b.once.Do(func() { b.r.Close(); b.close() })
	return nil
}

func okResponse(contentType string, body []byte) *outbound.StreamResponse {
	return &outbound.StreamResponse{Status: 200, ContentType: contentType, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}
}

func statusResponse(status int, retryAfter string) *outbound.StreamResponse {
	return &outbound.StreamResponse{Status: status, RetryAfter: retryAfter, ContentType: "text/html", Body: io.NopCloser(strings.NewReader("<html>secret-body</html>"))}
}

// blockingReader yields a valid image prefix and then blocks until ctx ends,
// failing like the outbound body does on cancellation.
type blockingReader struct {
	ctx  context.Context
	head []byte
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// memorySink is a FetchSink test double: it reads to EOF and commits only
// complete bodies.
type memorySink struct {
	mu      sync.Mutex
	puts    int
	stored  map[string][]byte
	fail    error
	partial bool
}

func (s *memorySink) Put(ctx context.Context, body io.Reader) (string, int64, error) {
	s.mu.Lock()
	s.puts++
	s.mu.Unlock()
	if s.fail != nil {
		return "", 0, s.fail
	}
	if s.partial {
		buf := make([]byte, 8)
		n, _ := io.ReadFull(body, buf)
		return "partial", int64(n), nil
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if s.stored == nil {
		s.stored = map[string][]byte{}
	}
	s.stored[digest] = data
	s.mu.Unlock()
	return digest, int64(len(data)), nil
}

func (s *memorySink) count() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts, len(s.stored)
}

func fastFetchOptions() FetchOptions {
	return FetchOptions{HostInterval: time.Millisecond, BackoffBase: 5 * time.Millisecond, Timeout: 10 * time.Second, AttemptTimeout: 5 * time.Second}
}

func newTestFetcher(t *testing.T, transport FetchTransport, o FetchOptions) *RemoteFetcher {
	t.Helper()
	f, err := NewRemoteFetcher(transport, o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// assertSafeError checks the error has the expected reason and carries no URL
// parts or response bytes.
func assertSafeError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error=%v want %v", err, want)
	}
	text := err.Error()
	for _, leak := range []string{"img.example", "image.tmdb.org", "secret", "abcDEF", "poster", "https", "html"} {
		if strings.Contains(text, leak) {
			t.Fatalf("error leaked %q: %s", leak, text)
		}
	}
	if FetchReason(err) != want.Error() {
		t.Fatalf("reason=%q", FetchReason(err))
	}
}

func TestRemoteFetchSuccessForBothSources(t *testing.T) {
	transport := newFakeFetchTransport(func(_ context.Context, _ int, target string) (*outbound.StreamResponse, error) {
		if strings.HasSuffix(target, ".png") {
			return okResponse("image/png", fetchPNG), nil
		}
		return okResponse("image/jpeg; charset=binary", fetchJPEG), nil
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	sink := &memorySink{}
	r, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceTMDB, URL: fetchTMDBURL}, sink)
	if err != nil || r.Source != FetchSourceTMDB || r.Format != "png" || r.Size != int64(len(fetchPNG)) || r.Attempts != 1 || r.FetchedAt.IsZero() || !bytes.Equal(sink.stored[r.Digest], fetchPNG) {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	r, err = f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: "https://IMG.example:443/a/b.jpg?x=1#frag"}, sink)
	if err != nil || r.Format != "jpeg" || r.Source != FetchSourceNFO {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	calls := transport.calls
	if len(calls) != 2 || calls[0].url != fetchTMDBURL || calls[0].options.MaxRedirects != 0 || calls[1].options.MaxRedirects != 3 ||
		calls[1].url != "https://img.example:443/a/b.jpg?x=1" || calls[0].options.MaxBytes != 20<<20 || !strings.HasPrefix(calls[0].options.Accept, "image/") {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestRemoteFetchURLPolicyRejectsBeforeNetwork(t *testing.T) {
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		t.Error("network reached")
		return nil, outbound.ErrDenied
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	for _, tc := range []FetchRequest{
		{FetchSourceNFO, ""},
		{FetchSourceNFO, "http://img.example/poster.png"},
		{FetchSourceNFO, "ftp://img.example/poster.png"},
		{FetchSourceNFO, "https://user:secret@img.example/poster.png"},
		{FetchSourceNFO, "https://img.example:8443/poster.png"},
		{FetchSourceNFO, "https://img.example./poster.png"},
		{FetchSourceNFO, "//img.example/poster.png"},
		{FetchSourceNFO, "https://img.example/" + strings.Repeat("a", maxFetchURLBytes)},
		{FetchSourceNFO, "https://img.example/a\nb.png"},
		{"other", "https://img.example/poster.png"},
		{FetchSourceTMDB, "https://img.example/t/p/original/abcDEF_123.png"},
		{FetchSourceTMDB, "http://image.tmdb.org/t/p/original/abcDEF_123.png"},
		{FetchSourceTMDB, "https://image.tmdb.org:443/t/p/original/abcDEF_123.png"},
		{FetchSourceTMDB, "https://IMAGE.tmdb.org/t/p/original/abcDEF_123.png"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/abcDEF_123.png?secret=1"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/abcDEF_123.png#secret"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/w9999/abcDEF_123.png"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/../secret.png"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/a/b.png"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/abc%2Fdef.png"},
		{FetchSourceTMDB, "https://image.tmdb.org/t/p/original/abcDEF_123.svg"},
		{FetchSourceTMDB, "https://image.tmdb.org/abcDEF_123.png"},
		{FetchSourceTMDB, "https://evil.example/https://image.tmdb.org/t/p/original/a.png"},
	} {
		_, err := f.Fetch(context.Background(), tc, &memorySink{})
		assertSafeError(t, err, ErrFetchInvalidURL)
	}
	if transport.count() != 0 {
		t.Fatal("rejected URL reached transport")
	}
	if u, err := TMDBImageURL("w500", "/abcDEF_123.jpg"); err != nil || u != "https://image.tmdb.org/t/p/w500/abcDEF_123.jpg" {
		t.Fatalf("url=%q err=%v", u, err)
	}
	if _, err := TMDBImageURL("w500", "//evil.example/a.jpg"); !errors.Is(err, ErrFetchInvalidURL) {
		t.Fatal("host injection accepted")
	}
}

func TestRemoteFetchLocalOnlySendsNothing(t *testing.T) {
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		t.Error("network reached")
		return okResponse("image/png", fetchPNG), nil
	})
	o := fastFetchOptions()
	o.LocalOnly = true
	sink := &memorySink{}
	for _, tr := range []FetchTransport{transport, nil} {
		f := newTestFetcher(t, tr, o)
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceTMDB, URL: fetchTMDBURL}, sink)
		assertSafeError(t, err, ErrFetchLocalOnly)
	}
	if puts, _ := sink.count(); transport.count() != 0 || puts != 0 {
		t.Fatal("local-only mode issued work")
	}
	if _, err := NewRemoteFetcher(nil, fastFetchOptions()); err == nil {
		t.Fatal("remote fetcher without transport accepted")
	}
}

func TestRemoteFetchRejectsContentTypeAndMagic(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		want              error
	}{
		{"html", "text/html", fetchPNG, ErrFetchContentType},
		{"missing", "", fetchPNG, ErrFetchContentType},
		{"svg", "image/svg+xml", []byte("<svg/>"), ErrFetchContentType},
		{"octet", "application/octet-stream", fetchPNG, ErrFetchContentType},
		{"bad_param", "image/png; =", fetchPNG, ErrFetchContentType},
		{"html_body", "image/png", []byte("<html><body>secret</body></html>"), ErrFetchFormat},
		{"mismatch", "image/png", fetchJPEG, ErrFetchFormat},
		{"empty", "image/png", nil, ErrFetchFormat},
		{"short", "image/png", []byte{0x89, 'P'}, ErrFetchFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
				return okResponse(tc.contentType, tc.body), nil
			})
			f := newTestFetcher(t, transport, fastFetchOptions())
			sink := &memorySink{}
			_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
			assertSafeError(t, err, tc.want)
			if puts, _ := sink.count(); puts != 0 || transport.count() != 1 {
				t.Fatal("rejected content reached sink or was retried")
			}
		})
	}
}

func TestRemoteFetchSniffsSupportedFormats(t *testing.T) {
	avif := []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf")
	avifCompat := []byte("\x00\x00\x00\x1cftypmif1\x00\x00\x00\x00mif1avifmiaf")
	heic := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic")
	for head, want := range map[string]string{
		string(fetchJPEG): "jpeg", string(fetchPNG): "png", "GIF89a....": "gif", "GIF87a....": "gif",
		"RIFF\x10\x00\x00\x00WEBPVP8 ": "webp", "II*\x00\x08\x00\x00\x00": "tiff", "MM\x00*\x00\x00\x00\x08": "tiff",
		"BM\x00\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00": "bmp", string(avif): "avif", string(avifCompat): "avif",
		string(heic): "", "RIFF\x10\x00\x00\x00WAVEfmt ": "", "<svg": "", "%PDF-1.7": "", "BM": "", "": "",
	} {
		if got := sniffImageFormat([]byte(head)); got != want {
			t.Errorf("sniff %q = %q want %q", head, got, want)
		}
	}
	for ct, want := range map[string]string{"image/JPEG": "jpeg", "image/pjpeg": "jpeg", "IMAGE/PNG; q=1": "png", "image/x-ms-bmp": "bmp", "image/avif": "avif", "image/tiff": "tiff", "image/webp": "webp", "image/gif": "gif", "image/heic": "", "text/plain": ""} {
		if got := declaredImageFormat(ct); got != want {
			t.Errorf("content type %q = %q want %q", ct, got, want)
		}
	}
}

func TestRemoteFetchOversizedBodyAborts(t *testing.T) {
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		// outbound fails the read that crosses MaxBytes.
		return &outbound.StreamResponse{Status: 200, ContentType: "image/png", ContentLength: -1,
			Body: io.NopCloser(io.MultiReader(bytes.NewReader(fetchPNG), &errorReader{outbound.ErrTooLarge}))}, nil
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	sink := &memorySink{}
	_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
	assertSafeError(t, err, ErrFetchTooLarge)
	if puts, stored := sink.count(); puts != 1 || stored != 0 || transport.count() != 1 {
		t.Fatalf("puts=%d stored=%d calls=%d", puts, stored, transport.count())
	}
	transport = newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		return nil, outbound.ErrTooLarge // declared Content-Length over the bound
	})
	f = newTestFetcher(t, transport, fastFetchOptions())
	_, err = f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
	assertSafeError(t, err, ErrFetchTooLarge)
	if transport.count() != 1 {
		t.Fatal("size failure retried")
	}
}

type errorReader struct{ err error }

func (r *errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestRemoteFetchSecurityFailuresAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{{outbound.ErrDenied, ErrFetchDenied}, {outbound.ErrRedirect, ErrFetchRedirect}} {
		transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) { return nil, tc.err })
		f := newTestFetcher(t, transport, fastFetchOptions())
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{})
		assertSafeError(t, err, tc.want)
		if transport.count() != 1 {
			t.Fatal("security failure retried")
		}
	}
	for status, want := range map[int]error{404: ErrFetchNotFound, 410: ErrFetchNotFound, 403: ErrFetchRejected, 302: ErrFetchRedirect, 204: ErrFetchRejected, 501: ErrFetchRejected} {
		transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
			return statusResponse(status, ""), nil
		})
		f := newTestFetcher(t, transport, fastFetchOptions())
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{})
		assertSafeError(t, err, want)
		var fe *FetchError
		if !errors.As(err, &fe) || fe.Status != status || fe.Attempts != 1 || transport.count() != 1 {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestRemoteFetch503RetriesThenSucceeds(t *testing.T) {
	transport := newFakeFetchTransport(func(_ context.Context, call int, _ string) (*outbound.StreamResponse, error) {
		switch call {
		case 1:
			return statusResponse(503, ""), nil
		case 2:
			return nil, outbound.ErrUnavailable // connection reset
		}
		return okResponse("image/png", fetchPNG), nil
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	r, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceTMDB, URL: fetchTMDBURL}, &memorySink{})
	if err != nil || r.Attempts != 3 || transport.count() != 3 {
		t.Fatalf("result=%+v error=%v calls=%d", r, err, transport.count())
	}
	// Exponential backoff: 5ms then 10ms base (plus jitter, plus 1ms spacing).
	c := transport.calls
	if d1, d2 := c[1].at.Sub(c[0].at), c[2].at.Sub(c[1].at); d1 < 5*time.Millisecond || d2 < 10*time.Millisecond {
		t.Fatalf("backoff too short: %v %v", d1, d2)
	}
}

func TestRemoteFetchRetryLimit(t *testing.T) {
	for _, attempts := range []int{1, 3, 5} {
		transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
			return statusResponse(502, ""), nil
		})
		o := fastFetchOptions()
		o.MaxAttempts = attempts
		f := newTestFetcher(t, transport, o)
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{})
		assertSafeError(t, err, ErrFetchUnavailable)
		var fe *FetchError
		if !errors.As(err, &fe) || fe.Attempts != attempts || fe.Status != 502 || transport.count() != attempts {
			t.Fatalf("attempts=%d error=%v calls=%d", attempts, err, transport.count())
		}
	}
	o := fastFetchOptions()
	o.MaxAttempts = 6
	if _, err := NewRemoteFetcher(newFakeFetchTransport(nil), o); err == nil {
		t.Fatal("retry cap exceeded")
	}
}

func TestRemoteFetch429HonoursRetryAfterAndCoolsHost(t *testing.T) {
	transport := newFakeFetchTransport(func(_ context.Context, call int, target string) (*outbound.StreamResponse, error) {
		if call == 1 {
			return statusResponse(429, "1"), nil
		}
		return okResponse("image/png", fetchPNG), nil
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	start := time.Now()
	first := make(chan error, 1)
	go func() {
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceTMDB, URL: fetchTMDBURL}, &memorySink{})
		first <- err
	}()
	for transport.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	// Another caller for the same host must also wait out the cooldown.
	other, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceTMDB, URL: "https://image.tmdb.org/t/p/w500/other.png"}, &memorySink{})
	if err != nil || other.Attempts != 1 {
		t.Fatalf("other=%+v error=%v", other, err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	c := transport.calls
	if len(c) != 3 {
		t.Fatalf("calls=%d", len(c))
	}
	for _, call := range c[1:] {
		if call.at.Sub(start) < time.Second {
			t.Fatalf("request inside Retry-After cooldown: %v", call.at.Sub(start))
		}
	}
	// A different host is not cooled down.
	before := time.Now()
	if _, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{}); err != nil || time.Since(before) > 500*time.Millisecond {
		t.Fatalf("unrelated host delayed: %v", err)
	}
}

func TestRemoteFetchRetryAfterBeyondPolicyFailsButCools(t *testing.T) {
	for _, header := range []string{"120", "not-a-date", "-1"} {
		t.Run(header, func(t *testing.T) {
			transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
				return statusResponse(429, header), nil
			})
			o := fastFetchOptions()
			o.InvalidRetryCooldown = time.Minute
			f := newTestFetcher(t, transport, o)
			_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{})
			assertSafeError(t, err, ErrFetchRateLimited)
			if transport.count() != 1 {
				t.Fatal("retried beyond server or policy limit")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if _, err := f.Fetch(ctx, FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{}); !errors.Is(err, context.DeadlineExceeded) || transport.count() != 1 {
				t.Fatalf("cooldown bypassed: %v calls=%d", err, transport.count())
			}
		})
	}
}

func TestRemoteFetchTimeoutAndCancellation(t *testing.T) {
	slow := func(ctx context.Context, _ int, _ string) (*outbound.StreamResponse, error) {
		return &outbound.StreamResponse{Status: 200, ContentType: "image/png", ContentLength: -1, Body: io.NopCloser(&blockingReader{ctx: ctx, head: fetchPNG})}, nil
	}
	transport := newFakeFetchTransport(slow)
	o := fastFetchOptions()
	o.Timeout, o.AttemptTimeout = 300*time.Millisecond, 100*time.Millisecond
	f := newTestFetcher(t, transport, o)
	sink := &memorySink{}
	start := time.Now()
	_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
	assertSafeError(t, err, ErrFetchTimeout)
	if elapsed := time.Since(start); elapsed > 2*time.Second || transport.count() < 2 {
		t.Fatalf("elapsed=%v calls=%d", elapsed, transport.count())
	}
	if _, stored := sink.count(); stored != 0 {
		t.Fatal("partial body stored")
	}

	transport = newFakeFetchTransport(slow)
	f = newTestFetcher(t, transport, fastFetchOptions())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.Fetch(ctx, FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
		done <- err
	}()
	for transport.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || transport.count() != 1 {
			t.Fatalf("error=%v calls=%d", err, transport.count())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation ignored")
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.total != 0 {
		t.Fatal("body not closed after cancellation")
	}
}

func TestRemoteFetchConcurrencyLimits(t *testing.T) {
	gate := make(chan struct{})
	transport := newFakeFetchTransport(func(ctx context.Context, _ int, _ string) (*outbound.StreamResponse, error) {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return okResponse("image/png", fetchPNG), nil
	})
	o := fastFetchOptions()
	o.MaxConcurrent, o.MaxPerHost = 3, 2
	f := newTestFetcher(t, transport, o)
	var wg sync.WaitGroup
	var failures atomic.Int32
	hosts := []string{"a.example", "b.example", "c.example", "d.example"}
	for _, host := range hosts {
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: "https://" + host + "/p.png"}, &memorySink{}); err != nil {
					failures.Add(1)
				}
			}()
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for transport.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := transport.count(); n != 3 {
		t.Fatalf("in flight=%d want 3", n)
	}
	close(gate)
	wg.Wait()
	if failures.Load() != 0 || transport.count() != 12 {
		t.Fatalf("failures=%d calls=%d", failures.Load(), transport.count())
	}
	if transport.peakAll > 3 {
		t.Fatalf("global peak=%d", transport.peakAll)
	}
	for _, host := range hosts {
		if transport.peak[host] > 2 {
			t.Fatalf("host %s peak=%d", host, transport.peak[host])
		}
	}
	if len(f.global) != 0 {
		t.Fatal("global slot leaked")
	}
	for _, h := range f.hosts {
		if len(h.slots) != 0 || h.users != 0 {
			t.Fatal("host slot leaked")
		}
	}
}

func TestRemoteFetchHostSpacingAndPrune(t *testing.T) {
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		return okResponse("image/png", fetchPNG), nil
	})
	o := fastFetchOptions()
	o.HostInterval = 40 * time.Millisecond
	f := newTestFetcher(t, transport, o)
	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, &memorySink{}); err != nil {
			t.Fatal(err)
		}
	}
	c := transport.calls
	for i := 1; i < len(c); i++ {
		if gap := c[i].at.Sub(c[i-1].at); gap < 40*time.Millisecond {
			t.Fatalf("host spacing=%v", gap)
		}
	}
	f.mu.Lock()
	for i := 0; i < fetchHostPruneLen+10; i++ {
		f.hosts[strings.Repeat("x", i+1)] = &fetchHost{slots: make(chan struct{}, 1)}
	}
	f.hosts["cooling"] = &fetchHost{slots: make(chan struct{}, 1), cooldown: time.Now().Add(time.Hour)}
	f.mu.Unlock()
	if _, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: "https://fresh.example/p.png"}, &memorySink{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hosts) > 3 || f.hosts["cooling"] == nil {
		t.Fatalf("hosts=%d", len(f.hosts))
	}
}

func TestRemoteFetchSinkFailures(t *testing.T) {
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		return okResponse("image/png", fetchPNG), nil
	})
	f := newTestFetcher(t, transport, fastFetchOptions())
	for _, sink := range []*memorySink{{fail: errors.New("disk full at /secret/path")}, {partial: true}} {
		_, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, sink)
		assertSafeError(t, err, ErrFetchStore)
	}
	if transport.count() != 2 {
		t.Fatal("storage failure retried")
	}
	if _, err := f.Fetch(context.Background(), FetchRequest{Source: FetchSourceNFO, URL: fetchNFOURL}, nil); !errors.Is(err, ErrFetchStore) {
		t.Fatal("nil sink accepted")
	}
}

func TestRemoteFetchOptionsAndRetryAfterParsing(t *testing.T) {
	for _, o := range []FetchOptions{{MaxConcurrent: 65}, {MaxConcurrent: 2, MaxPerHost: 3}, {MaxBytes: outbound.MaxStreamBytes + 1}, {MaxRedirects: 6},
		{Timeout: time.Second, AttemptTimeout: 2 * time.Second}, {HostInterval: -1}, {MaxAttempts: -1}, {MaxRetryAfter: 2 * time.Hour}} {
		if _, err := NewRemoteFetcher(newFakeFetchTransport(nil), o); err == nil {
			t.Errorf("options accepted: %+v", o)
		}
	}
	f, err := NewRemoteFetcher(newFakeFetchTransport(nil), FetchOptions{MaxRedirects: -1})
	if err != nil || f.options.MaxRedirects != 0 {
		t.Fatal("redirect disable failed")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for raw, want := range map[string]time.Duration{"": 0, "0": 0, " 2 ": 2 * time.Second, "99999999999999999999999": time.Hour, "Thu, 01 Jan 2026 00:00:05 GMT": 5 * time.Second, "Wed, 31 Dec 2025 00:00:00 GMT": 0} {
		if got, ok := parseRetryAfter(raw, now); !ok || got != want {
			t.Errorf("retry-after %q = %v,%v", raw, got, ok)
		}
	}
	for _, raw := range []string{"-1", "1.5", "soon", " ", strings.Repeat("1", 129)} {
		if _, ok := parseRetryAfter(raw, now); ok {
			t.Errorf("retry-after %q accepted", raw)
		}
	}
}
