package outbound_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

var imageWirePNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{9}, 4096)...)

func imageWireCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spec := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "image test"},
		DNSNames:  []string{"img.example", "other.example", "internal.example", "image.tmdb.org"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, spec, spec, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// imageWire is one TLS server reachable only through the pinned public
// address 93.184.216.34:443; "internal.example" resolves to a private range.
type imageWire struct {
	srv      *httptest.Server
	client   *outbound.Client
	dials    atomic.Int32
	mu       sync.Mutex
	requests map[string]int
	referers []string
	aborted  chan struct{}
}

func newImageWire(t *testing.T, budget *resources.Budget) *imageWire {
	t.Helper()
	w := &imageWire{requests: map[string]int{}, aborted: make(chan struct{}, 4)}
	cert, roots := imageWireCertificate(t)
	w.srv = httptest.NewUnstartedServer(http.HandlerFunc(w.serve))
	w.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	w.srv.Config.ErrorLog = nil
	w.srv.StartTLS()
	t.Cleanup(w.srv.Close)
	lookup := func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		if host == "internal.example" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Errorf("unpinned dial %s", address)
			return nil, errors.New("unexpected dial")
		}
		w.dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, w.srv.Listener.Addr().String())
	}
	var err error
	if budget != nil {
		w.client, err = outbound.NewMappedStreamTestClientWithBudget(lookup, dial, roots, budget)
	} else {
		w.client, err = outbound.NewMappedStreamTestClient(lookup, dial, roots)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.client.CloseIdleConnections)
	return w
}

func (w *imageWire) count(key string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requests[key]
}

func (w *imageWire) serve(rw http.ResponseWriter, r *http.Request) {
	key := r.Host + r.URL.Path
	w.mu.Lock()
	w.requests[key]++
	if ref := r.Header.Get("Referer"); ref != "" {
		w.referers = append(w.referers, ref)
	}
	w.mu.Unlock()
	switch {
	case r.URL.Path == "/ok.png" || r.URL.Path == "/t/p/original/abcDEF.png":
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(imageWirePNG)
	case r.URL.Path == "/stream-big.png":
		// No Content-Length: the bound must stop the streamed read.
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(imageWirePNG)
		chunk := bytes.Repeat([]byte{1}, 32<<10)
		for i := 0; i < 1024; i++ {
			if _, err := rw.Write(chunk); err != nil {
				w.aborted <- struct{}{}
				return
			}
			rw.(http.Flusher).Flush()
		}
	case r.URL.Path == "/declared-big.png":
		rw.Header().Set("Content-Type", "image/png")
		rw.Header().Set("Content-Length", strconv.Itoa(8<<20))
		rw.WriteHeader(200)
		rw.Write(imageWirePNG)
	case r.URL.Path == "/slow.png":
		rw.Header().Set("Content-Type", "image/png")
		rw.Write(imageWirePNG[:16])
		rw.(http.Flusher).Flush()
		<-r.Context().Done()
		w.aborted <- struct{}{}
	case r.URL.Path == "/html":
		rw.Header().Set("Content-Type", "image/png")
		rw.Write([]byte("<html>secret-body</html>"))
	case r.URL.Path == "/redirect":
		http.Redirect(rw, r, r.URL.Query().Get("to"), http.StatusFound)
	case r.URL.Path == "/t/p/original/moved.png":
		http.Redirect(rw, r, "/t/p/original/abcDEF.png", http.StatusMovedPermanently)
	case strings.HasPrefix(r.URL.Path, "/chain/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/chain/"))
		if n <= 0 {
			rw.Header().Set("Content-Type", "image/png")
			rw.Write(imageWirePNG)
			return
		}
		http.Redirect(rw, r, "https://other.example/chain/"+strconv.Itoa(n-1)+"?secret=token", http.StatusFound)
	default:
		rw.WriteHeader(404)
	}
}

type imageWireSink struct {
	mu     sync.Mutex
	stored map[string][]byte
}

func (s *imageWireSink) Put(_ context.Context, body io.Reader) (string, int64, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stored == nil {
		s.stored = map[string][]byte{}
	}
	s.stored[digest] = data
	return digest, int64(len(data)), nil
}

func imageWireFetcher(t *testing.T, w *imageWire, o images.FetchOptions) *images.RemoteFetcher {
	t.Helper()
	if o.HostInterval == 0 {
		o.HostInterval = time.Millisecond
	}
	if o.BackoffBase == 0 {
		o.BackoffBase = 5 * time.Millisecond
	}
	f, err := images.NewRemoteFetcher(w.client, o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func assertImageWireError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error=%v want %v", err, want)
	}
	for _, leak := range []string{"example", "tmdb", "secret", "127.0.0.1", "10.0.0.5", "169.254", "https", "93.184"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaked %q: %v", leak, err)
		}
	}
}

func TestImageFetchThroughGuardedTLS(t *testing.T) {
	w := newImageWire(t, nil)
	f := imageWireFetcher(t, w, images.FetchOptions{})
	sink := &imageWireSink{}
	for _, request := range []images.FetchRequest{
		{Source: images.FetchSourceNFO, URL: "https://img.example/ok.png?secret=token"},
		{Source: images.FetchSourceTMDB, URL: "https://image.tmdb.org/t/p/original/abcDEF.png"},
		{Source: images.FetchSourceNFO, URL: "https://img.example/chain/3"},
	} {
		r, err := f.Fetch(context.Background(), request, sink)
		if err != nil || r.Format != "png" || r.Size != int64(len(imageWirePNG)) || !bytes.Equal(sink.stored[r.Digest], imageWirePNG) {
			t.Fatalf("result=%+v error=%v", r, err)
		}
	}
	w.mu.Lock()
	referers := len(w.referers)
	w.mu.Unlock()
	if referers != 0 || w.count("other.example/chain/0") != 1 {
		t.Fatalf("referers=%d redirect chain incomplete", referers)
	}
}

func TestImageFetchRedirectToPrivateOrDowngradeDenied(t *testing.T) {
	for _, target := range []string{"https://127.0.0.1/x?secret=token", "https://169.254.169.254/latest/meta-data", "https://[::1]/x", "https://10.0.0.5/x",
		"https://internal.example/x", "http://img.example/ok.png", "file:///etc/passwd", "https://user:secret@img.example/ok.png"} {
		t.Run(target, func(t *testing.T) {
			w := newImageWire(t, nil)
			f := imageWireFetcher(t, w, images.FetchOptions{})
			_, err := f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/redirect?to=" + target}, &imageWireSink{})
			if !(errors.Is(err, images.ErrFetchDenied) || errors.Is(err, images.ErrFetchRedirect)) {
				t.Fatalf("error=%v", err)
			}
			assertImageWireError(t, err, err.(*images.FetchError).Reason)
			if w.count("img.example/redirect") != 1 || w.count("img.example/ok.png") != 0 || w.dials.Load() != 1 {
				t.Fatalf("second target contacted: dials=%d", w.dials.Load())
			}
		})
	}
}

func TestImageFetchRedirectLimits(t *testing.T) {
	w := newImageWire(t, nil)
	f := imageWireFetcher(t, w, images.FetchOptions{MaxRedirects: 2})
	_, err := f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/chain/3"}, &imageWireSink{})
	assertImageWireError(t, err, images.ErrFetchRedirect)
	if w.count("other.example/chain/0") != 0 || w.count("other.example/chain/1") != 1 {
		t.Fatal("redirect cap not enforced before request")
	}
	// TMDB artwork never follows redirects, even to the same official host.
	_, err = f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceTMDB, URL: "https://image.tmdb.org/t/p/original/moved.png"}, &imageWireSink{})
	assertImageWireError(t, err, images.ErrFetchRedirect)
	if w.count("image.tmdb.org/t/p/original/abcDEF.png") != 0 {
		t.Fatal("TMDB redirect followed")
	}
	none := imageWireFetcher(t, w, images.FetchOptions{MaxRedirects: -1})
	_, err = none.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/chain/1"}, &imageWireSink{})
	assertImageWireError(t, err, images.ErrFetchRedirect)
}

func TestImageFetchStreamingBoundsAndContent(t *testing.T) {
	w := newImageWire(t, nil)
	f := imageWireFetcher(t, w, images.FetchOptions{MaxBytes: 64 << 10})
	sink := &imageWireSink{}
	_, err := f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/stream-big.png"}, sink)
	assertImageWireError(t, err, images.ErrFetchTooLarge)
	select {
	case <-w.aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("oversized stream was not aborted")
	}
	_, err = f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/declared-big.png"}, sink)
	assertImageWireError(t, err, images.ErrFetchTooLarge)
	_, err = f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/html"}, sink)
	assertImageWireError(t, err, images.ErrFetchFormat)
	if len(sink.stored) != 0 || w.count("img.example/stream-big.png") != 1 {
		t.Fatal("rejected body stored or retried")
	}
}

func TestImageFetchWireTimeoutReleasesBudget(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 1})
	if err != nil {
		t.Fatal(err)
	}
	w := newImageWire(t, b)
	f := imageWireFetcher(t, w, images.FetchOptions{Timeout: 400 * time.Millisecond, AttemptTimeout: 150 * time.Millisecond, MaxAttempts: 1})
	start := time.Now()
	_, err = f.Fetch(context.Background(), images.FetchRequest{Source: images.FetchSourceNFO, URL: "https://img.example/slow.png"}, &imageWireSink{})
	assertImageWireError(t, err, images.ErrFetchTimeout)
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not enforced")
	}
	select {
	case <-w.aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("server request not cancelled")
	}
	if s := b.Stats(); s != (resources.Stats{}) {
		t.Fatalf("budget leaked: %+v", s)
	}
	// Body reads hold the shared I/O permit until Close.
	r, err := w.client.Stream(context.Background(), "https://img.example/ok.png", outbound.StreamOptions{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if s := b.Stats(); s.IO != 1 {
		t.Fatalf("stream body without permit: %+v", s)
	}
	r.Body.Close()
	if s := b.Stats(); s != (resources.Stats{}) {
		t.Fatalf("close leaked permit: %+v", s)
	}
}

func TestStreamRejectsUnsafeRequestsBeforeNetwork(t *testing.T) {
	w := newImageWire(t, nil)
	for _, tc := range []struct {
		url string
		o   outbound.StreamOptions
	}{
		{"http://img.example/ok.png", outbound.StreamOptions{MaxBytes: 1}},
		{"https://127.0.0.1/ok.png", outbound.StreamOptions{MaxBytes: 1}},
		{"https://img.example/ok.png#secret", outbound.StreamOptions{MaxBytes: 1}},
		{"https://img.example/ok.png", outbound.StreamOptions{MaxBytes: 0}},
		{"https://img.example/ok.png", outbound.StreamOptions{MaxBytes: outbound.MaxStreamBytes + 1}},
		{"https://img.example/ok.png", outbound.StreamOptions{MaxBytes: 1, MaxRedirects: outbound.MaxStreamRedirects + 1}},
		{"https://img.example/ok.png", outbound.StreamOptions{MaxBytes: 1, Accept: "image/*\r\nX-Injected: 1"}},
	} {
		if _, err := w.client.Stream(context.Background(), tc.url, tc.o); !errors.Is(err, outbound.ErrDenied) {
			t.Fatalf("%s: %v", tc.url, err)
		}
	}
	if w.dials.Load() != 0 {
		t.Fatal("rejected stream reached network")
	}
}
