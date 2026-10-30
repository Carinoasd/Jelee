package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func refreshTestRefresher(t *testing.T, transport FetchTransport, o FetchOptions) (*ItemImageRefresher, *Store) {
	t.Helper()
	processor, store, _, _ := uploadTestProcessor(t)
	if o.MaxBytes == 0 {
		o.MaxBytes = processor.ItemImageLimit()
	}
	r, err := NewItemImageRefresher(newTestFetcher(t, transport, o), processor)
	if err != nil {
		t.Fatal(err)
	}
	r.busyWait = time.Millisecond
	return r, store
}

// TestItemImageRefresherStoresVerifiedImages: a fetched image is decoded
// like an upload, kept in the store and described with its real size.
func TestItemImageRefresherStoresVerifiedImages(t *testing.T) {
	picture := uploadTestPNG(t, 30, 20)
	transport := newFakeFetchTransport(func(_ context.Context, _ int, _ string) (*outbound.StreamResponse, error) {
		return okResponse("image/png", picture), nil
	})
	r, store := refreshTestRefresher(t, transport, fastFetchOptions())
	content, err := r.FetchItemImage(context.Background(), "https://img.example/poster.png")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(picture)
	if !bytes.Equal(content.SHA256, sum[:]) || content.Width != 30 || content.Height != 20 || content.Format != "png" || content.Bytes != int64(len(picture)) || content.FetchedAt.IsZero() {
		t.Fatalf("content: %+v", content)
	}
	if !store.HasOriginal(sum) {
		t.Fatal("fetched image is not in the store")
	}
	object, err := store.OpenOriginal(context.Background(), sum, true)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(object)
	_ = object.Close()
	if !bytes.Equal(stored, picture) {
		t.Fatal("stored bytes differ from the response")
	}
	// TMDB artwork keeps the strict canonical rule: no redirects allowed.
	if _, err := r.FetchItemImage(context.Background(), "https://image.tmdb.org/t/p/w500/poster.png"); err != nil {
		t.Fatal("tmdb artwork", err)
	}
	calls := transport.calls
	if len(calls) != 2 || calls[0].options.MaxRedirects == 0 || calls[1].options.MaxRedirects != 0 || calls[1].options.MaxBytes != r.limit {
		t.Fatalf("fetch options: %+v", calls)
	}
}

// TestItemImageRefresherClassifiesFailures maps every failure onto a stable
// reason without URL text, and tells permanent from transient ones.
func TestItemImageRefresherClassifiesFailures(t *testing.T) {
	picture := uploadTestPNG(t, 8, 8)
	corrupt := append(append([]byte{}, picture[:40]...), bytes.Repeat([]byte{7}, 200)...)
	avif := append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0, 0, 0, 0, 'm', 'i', 'f', '1', 'a', 'v', 'i', 'f'}, make([]byte, 64)...)
	cases := []struct {
		name      string
		url       string
		response  func() (*outbound.StreamResponse, error)
		reason    string
		status    int
		permanent bool
	}{
		{"not found", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return statusResponse(404, ""), nil }, domain.ImageFetchNotFound, 404, true},
		{"forbidden", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return statusResponse(403, ""), nil }, domain.ImageFetchStatusRejected, 403, true},
		{"outage", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return statusResponse(502, ""), nil }, domain.ImageFetchUnavailable, 502, false},
		{"rate limited", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return statusResponse(429, "3600"), nil }, domain.ImageFetchRateLimited, 429, false},
		{"html", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return okResponse("text/html", []byte("<html>")), nil }, domain.ImageFetchContentTypeRejected, 200, true},
		{"mislabelled", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return okResponse("image/jpeg", picture), nil }, domain.ImageFetchFormatRejected, 200, true},
		{"undecodable", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return okResponse("image/png", corrupt), nil }, domain.ImageFetchUnsupported, 200, true},
		{"avif", "https://img.example/a.avif", func() (*outbound.StreamResponse, error) { return okResponse("image/avif", avif), nil }, domain.ImageFetchUnsupported, 200, true},
		{"denied", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return nil, outbound.ErrDenied }, domain.ImageFetchTargetDenied, 0, true},
		{"too large", "https://img.example/a.png", func() (*outbound.StreamResponse, error) { return nil, outbound.ErrTooLarge }, domain.ImageFetchTooLarge, 0, true},
		{"bad url", "https://img.example:8443/a.png", nil, domain.ImageFetchURLRejected, 0, true},
		{"bad tmdb url", "https://image.tmdb.org/t/p/w500/a.png?x=1", nil, domain.ImageFetchURLRejected, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
				if c.response == nil {
					t.Fatal("rejected URL reached the transport")
				}
				return c.response()
			})
			o := fastFetchOptions()
			o.MaxAttempts = 1
			r, _ := refreshTestRefresher(t, transport, o)
			_, err := r.FetchItemImage(context.Background(), c.url)
			var failure *domain.ImageFetchFailure
			if !errors.As(err, &failure) || failure.Reason != c.reason || failure.Status != c.status || failure.Permanent != c.permanent {
				t.Fatalf("failure: %v", err)
			}
			if strings.Contains(err.Error(), "img.example") || strings.Contains(err.Error(), "secret") {
				t.Fatal("failure text carries URL or response data")
			}
		})
	}
}

// TestItemImageRefresherWaitsForBusyProcessor: a processor whose slots are
// all taken by viewers makes the fetch wait, not fail; cancellation of the
// caller is returned as such.
func TestItemImageRefresherWaitsForBusyProcessor(t *testing.T) {
	picture := uploadTestPNG(t, 4, 4)
	transport := newFakeFetchTransport(func(context.Context, int, string) (*outbound.StreamResponse, error) {
		return okResponse("image/png", picture), nil
	})
	o := fastFetchOptions()
	o.MaxAttempts, o.AttemptTimeout = 1, 300*time.Millisecond
	r, _ := refreshTestRefresher(t, transport, o)
	for range r.processor.options.MaxConcurrent {
		if err := r.processor.admit(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.FetchItemImage(context.Background(), "https://img.example/a.png")
	var failure *domain.ImageFetchFailure
	if !errors.As(err, &failure) || failure.Reason != domain.ImageFetchBusy || failure.Permanent {
		t.Fatalf("busy processor: %v", err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := r.FetchItemImage(ctx, "https://img.example/b.png")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled fetch", err)
	}
	for range r.processor.options.MaxConcurrent {
		r.processor.release()
	}
	if _, err := r.FetchItemImage(context.Background(), "https://img.example/c.png"); err != nil {
		t.Fatal("fetch after the processor is free", err)
	}
}

func TestItemImageRefresherNeedsStoreAndFetcher(t *testing.T) {
	if _, err := NewItemImageRefresher(nil, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	processor := processorTestNew(t, processorTestOptions(t.TempDir()))
	fetcher := newTestFetcher(t, newFakeFetchTransport(nil), fastFetchOptions())
	if _, err := NewItemImageRefresher(fetcher, processor); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("refresher without a store", err)
	}
	if _, err := processor.IngestFetchedItemImage(context.Background(), []byte{1}); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("ingest without a store", err)
	}
	local, err := NewRemoteFetcher(nil, FetchOptions{LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	withStore, _, _, _ := uploadTestProcessor(t)
	r, err := NewItemImageRefresher(local, withStore)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.FetchItemImage(context.Background(), "https://img.example/a.png")
	var failure *domain.ImageFetchFailure
	if !errors.As(err, &failure) || failure.Reason != domain.ImageFetchLocalOnly {
		t.Fatal("local-only mode", err)
	}
	if _, err := withStore.IngestFetchedItemImage(context.Background(), nil); !errors.Is(err, domain.ErrImageUnsupported) {
		t.Fatal("empty body", err)
	}
	if _, err := withStore.IngestFetchedItemImage(context.Background(), make([]byte, withStore.ItemImageLimit()+1)); !errors.Is(err, domain.ErrImageTooLarge) {
		t.Fatal("oversized body", err)
	}
}

func TestFetchFailureMapping(t *testing.T) {
	for _, c := range []struct {
		err, sink error
		reason    string
	}{
		{&FetchError{Reason: ErrFetchStore}, domain.ErrImageTooLarge, domain.ImageFetchTooLarge},
		{&FetchError{Reason: ErrFetchStore}, domain.ErrImageUnavailable, domain.ImageFetchStoreFailed},
		{&FetchError{Reason: ErrFetchTimeout}, domain.ErrImageBusy, domain.ImageFetchBusy},
		{&FetchError{Reason: ErrFetchTimeout}, nil, domain.ImageFetchTimeout},
		{&FetchError{Reason: ErrFetchTimeout}, context.DeadlineExceeded, domain.ImageFetchTimeout},
		{&FetchError{Reason: ErrFetchRedirect, Status: 999}, nil, domain.ImageFetchRedirectDenied},
		{errors.New("other"), nil, domain.ImageFetchUnavailable},
	} {
		got := fetchFailure(c.err, c.sink)
		if got.Reason != c.reason || got.Status != 0 || !domain.ValidImageFetchReason(got.Reason) {
			t.Fatalf("%v/%v: %+v", c.err, c.sink, got)
		}
	}
}
