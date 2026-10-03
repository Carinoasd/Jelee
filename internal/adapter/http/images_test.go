package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

const httpImageETag = `"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`

type httpImageRepository func(context.Context, domain.Actor, string) (domain.LocalImageSource, error)

func (f httpImageRepository) ResolveImageSource(ctx context.Context, actor domain.Actor, id string) (domain.LocalImageSource, error) {
	return f(ctx, actor, id)
}

type httpImageRenderer func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error)

func (f httpImageRenderer) Render(ctx context.Context, source domain.LocalImageSource, request domain.ImageRequest) (app.ImageResult, error) {
	return f(ctx, source, request)
}

type httpImageBody struct {
	*bytes.Reader
	closed  atomic.Int32
	onClose func()
}

func (b *httpImageBody) Close() error {
	b.closed.Add(1)
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

func httpImageResult() (app.ImageResult, *httpImageBody) {
	body := &httpImageBody{Reader: bytes.NewReader([]byte("bounded-jpeg-fixture"))}
	return app.ImageResult{Body: body, ContentType: "image/jpeg", ETag: httpImageETag, Size: body.Size(), Width: 320, Height: 180}, body
}

func httpImagesConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := validConfig()
	cfg.EnableImages, cfg.EnableAccounts, cfg.EnableCatalog = true, true, true
	cfg.Accounts, cfg.Images = config.DefaultAccountsConfig(), config.DefaultImagesConfig()
	cfg.Images.TempRoot = filepath.Clean(t.TempDir())
	cfg.RequestTimeoutSeconds = 15
	return cfg
}

func imageRouter(t *testing.T, cfg config.Config, backend Backend, repo httpImageRepository, renderer httpImageRenderer) http.Handler {
	t.Helper()
	if repo == nil {
		repo = func(ctx context.Context, actor domain.Actor, id string) (domain.LocalImageSource, error) {
			if actor.UserID != userID || actor.SessionID != sessionID || id != itemID {
				t.Error("image actor or item was not derived from authenticated request")
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("unbounded image source lookup")
			}
			return domain.LocalImageSource{ItemID: id, LibraryID: libraryID, SourceID: sourceID, RootPath: "/private/media", MediaPath: "private.mkv"}, nil
		}
	}
	service, err := app.NewImages(repo, renderer)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewWithImages(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t), nil, nil, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func imageHTTPRequest(method, query, token string) *http.Request {
	r := httptest.NewRequest(method, "http://localhost/images/Primary/"+itemID+query, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat(token, 43))
	}
	return r
}

func TestImagesHTTPRepresentationAndConditionalAuthorization(t *testing.T) {
	for _, test := range []struct {
		method, conditional string
		status              int
		body                bool
	}{
		{"GET", "", 200, true}, {"HEAD", "", 200, false},
		{"GET", httpImageETag, 304, false}, {"HEAD", "W/" + httpImageETag, 304, false},
		{"GET", `"other", W/` + httpImageETag, 304, false}, {"GET", "*", 304, false},
		{"GET", `"other"`, 200, true}, {"GET", `invalid, ` + httpImageETag, 200, true},
	} {
		t.Run(test.method+test.conditional, func(t *testing.T) {
			result, body := httpImageResult()
			lookups := 0
			repo := httpImageRepository(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
				lookups++
				return domain.LocalImageSource{ItemID: itemID}, nil
			})
			h := imageRouter(t, httpImagesConfig(t), &metricsHTTPBackend{}, repo, func(_ context.Context, _ domain.LocalImageSource, q domain.ImageRequest) (app.ImageResult, error) {
				if q != (domain.ImageRequest{Type: "Primary", Format: "jpeg", Width: 640, Height: 640}) {
					t.Errorf("default request: %+v", q)
				}
				return result, nil
			})
			r := imageHTTPRequest(test.method, "", "u")
			r.Header.Set("If-None-Match", test.conditional)
			r.Header.Set("Range", "bytes=0-1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status || (w.Body.Len() > 0) != test.body || body.closed.Load() != 1 || lookups != 2 {
				t.Fatalf("response=%d body=%q close=%d lookups=%d", w.Code, w.Body.String(), body.closed.Load(), lookups)
			}
			if w.Header().Get("ETag") != httpImageETag || w.Header().Get("Cache-Control") != "private, no-cache, must-revalidate" || w.Header().Get("Vary") != "Authorization" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing private cache or HTTP boundary")
			}
			if test.status == 200 && (w.Header().Get("Content-Length") != "20" || w.Header().Get("Content-Type") != "image/jpeg") {
				t.Fatal("representation headers differ")
			}
		})
	}
}

func TestImagesHTTPStrictQueryAndAuthentication(t *testing.T) {
	var calls atomic.Int32
	h := imageRouter(t, httpImagesConfig(t), &metricsHTTPBackend{}, nil, func(_ context.Context, _ domain.LocalImageSource, q domain.ImageRequest) (app.ImageResult, error) {
		calls.Add(1)
		if q != (domain.ImageRequest{Type: "Primary", Format: "jpeg", Width: 320, Height: 180, Quality: 73}) {
			t.Errorf("query projection: %+v", q)
		}
		v, _ := httpImageResult()
		return v, nil
	})
	for _, query := range []string{"?width=", "?width=0", "?height=0", "?width=01", "?width=+1", "?width=-1", "?width=2049", "?height=999999999999999999999", "?quality=0", "?quality=101", "?quality=-1", "?format=", "?format=png", "?format=JPEG", "?Width=100", "?width=100&width=100", "?file=/private/media", "?tag=private", "?width=%xx", "?width=1;height=2"} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, imageHTTPRequest("GET", query, "u"))
			if w.Code != 400 || calls.Load() != 0 || strings.Contains(w.Body.String(), "/private") {
				t.Fatalf("unsafe query response %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, token := range []string{"", "x"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, imageHTTPRequest("GET", "", token))
		if w.Code != 401 || calls.Load() != 0 {
			t.Fatal("unauthenticated image rendered")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, imageHTTPRequest("GET", "?width=320&height=180&quality=73&format=jpeg", "u"))
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("valid query failed %d", w.Code)
	}
}

func TestImagesHTTPClosesResultsOnRevocationAndErrors(t *testing.T) {
	for _, mode := range []string{"revoked", "binding-changed", "renderer-error", "oversized", "bad-etag", "bad-type", "bad-dimension", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			result, body := httpImageResult()
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo := httpImageRepository(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
				calls++
				source := domain.LocalImageSource{ItemID: itemID, MediaPath: "private.mkv"}
				if calls == 2 && mode == "revoked" {
					return domain.LocalImageSource{}, domain.ErrForbidden
				}
				if calls == 2 && mode == "binding-changed" {
					source.SourceID = sourceID
				}
				return source, nil
			})
			h := imageRouter(t, httpImagesConfig(t), &metricsHTTPBackend{}, repo, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
				switch mode {
				case "renderer-error":
					return result, errors.New("/private/media token=private")
				case "oversized":
					result.Size = 3 << 20
				case "bad-etag":
					result.ETag = "private\r\nInjected: yes"
				case "bad-type":
					result.ContentType = "text/html"
				case "bad-dimension":
					result.Width = 4096
				case "cancelled":
					cancel()
				}
				return result, nil
			})
			r := imageHTTPRequest("GET", "", "u").WithContext(ctx)
			r.Header.Set("If-None-Match", httpImageETag)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 404
			switch mode {
			case "revoked":
				want = 403
			case "renderer-error":
				want = 500
			case "cancelled":
				want = 408
			}
			if w.Code != want || body.closed.Load() != 1 || strings.Contains(w.Body.String(), "private") || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d close=%d response=%s", w.Code, body.closed.Load(), w.Body.String())
			}
		})
	}
}

func TestImagesHTTPAdmissionIncludesAuthenticationAndCancellation(t *testing.T) {
	cfg := httpImagesConfig(t)
	entered := make(chan struct{}, 2)
	backend := &metricsHTTPBackend{auth: func(ctx context.Context, _ string) (access.Principal, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return access.Principal{}, ctx.Err()
	}}
	h := imageRouter(t, cfg, backend, nil, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
		t.Error("cancelled authentication rendered")
		return app.ImageResult{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 2)
	for range 2 {
		go func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, imageHTTPRequest("GET", "", "u").WithContext(ctx))
			done <- w.Code
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("authentication did not enter")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, imageHTTPRequest("GET", "", "u"))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || backend.authCalls.Load() != 2 {
		t.Fatal("saturated authentication queued")
	}
	cancel()
	for range 2 {
		select {
		case status := <-done:
			if status != 408 {
				t.Fatal(status)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel did not join")
		}
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, imageHTTPRequest("GET", "", "u").WithContext(cancelled))
	if w.Code != 408 || backend.authCalls.Load() != 2 {
		t.Fatal("cancelled request entered authentication")
	}
}

func TestImagesHTTPAdmissionHeldUntilBodyClose(t *testing.T) {
	cfg := httpImagesConfig(t)
	cfg.Images.MaxConcurrent = 1
	closing, release := make(chan struct{}), make(chan struct{})
	result, body := httpImageResult()
	body.onClose = func() { close(closing); <-release }
	backend := &metricsHTTPBackend{}
	h := imageRouter(t, cfg, backend, nil, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
		return result, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), imageHTTPRequest("HEAD", "", "u")) }()
	select {
	case <-closing:
	case <-time.After(3 * time.Second):
		t.Fatal("body did not close")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, imageHTTPRequest("GET", "", "u"))
	if w.Code != 503 || backend.authCalls.Load() != 1 {
		t.Fatal("body close released admission early")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not join")
	}
}

func TestImagesHTTPDeadlineAndErrorMapping(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrImageBusy, 503, "image_busy"}, {domain.ErrImageUnavailable, 404, "image_unavailable"}, {domain.ErrImageTooLarge, 413, "image_too_large"}, {domain.ErrImageUnsupported, 415, "image_unsupported"},
	} {
		w := httptest.NewRecorder()
		WriteError(w, imageHTTPRequest("GET", "", "u"), test.err)
		if w.Code != test.status || !strings.Contains(w.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("mapping %d %s", w.Code, w.Body.String())
		}
	}
	var deadlines []time.Time
	backend := &metricsHTTPBackend{auth: func(ctx context.Context, _ string) (access.Principal, error) {
		d, _ := ctx.Deadline()
		deadlines = append(deadlines, d)
		return access.Principal{UserID: userID, SessionID: sessionID}, nil
	}}
	h := imageRouter(t, httpImagesConfig(t), backend, func(ctx context.Context, _ domain.Actor, _ string) (domain.LocalImageSource, error) {
		d, _ := ctx.Deadline()
		deadlines = append(deadlines, d)
		return domain.LocalImageSource{}, nil
	}, func(ctx context.Context, _ domain.LocalImageSource, _ domain.ImageRequest) (app.ImageResult, error) {
		d, _ := ctx.Deadline()
		deadlines = append(deadlines, d)
		r, _ := httpImageResult()
		return r, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := &metricsDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, imageHTTPRequest("HEAD", "", "u").WithContext(ctx))
	if w.Code != 200 || len(deadlines) != 4 {
		t.Fatal("image flow did not execute")
	}
	for _, deadline := range deadlines {
		if deadline.IsZero() || !deadline.Equal(w.deadline) {
			t.Fatal("image stages do not share parent deadline")
		}
	}
}

type imageFailedWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (w *imageFailedWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, errors.New("private socket detail")
}

func TestImagesHTTPWriteFailureAndBoundedCopyReleaseBody(t *testing.T) {
	for _, failedWrite := range []bool{false, true} {
		result, body := httpImageResult()
		result.Size = 5
		h := imageRouter(t, httpImagesConfig(t), &metricsHTTPBackend{}, nil, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
			return result, nil
		})
		if failedWrite {
			w := &imageFailedWriter{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, imageHTTPRequest("GET", "", "u"))
			if w.Code != 200 || w.writes != 1 || w.Body.Len() != 0 {
				t.Fatal("write failure appended an error body")
			}
		} else {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, imageHTTPRequest("GET", "", "u"))
			if w.Body.String() != "bound" || w.Header().Get("Content-Length") != "5" {
				t.Fatal("copy exceeded advertised bound")
			}
		}
		if body.closed.Load() != 1 {
			t.Fatal("failed or bounded copy leaked admission body")
		}
	}
}

func TestImagesHTTPRolloutAndOpenAPI(t *testing.T) {
	cfg := httpImagesConfig(t)
	_, err := NewWithTelemetry(cfg, &metricsHTTPBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t), nil, nil, nil)
	if err == nil {
		t.Fatal("enabled images accepted missing dependency")
	}
	for _, enabled := range []bool{false, true} {
		cfg.EnableImages = enabled
		var renders int
		h := imageRouter(t, cfg, &metricsHTTPBackend{}, nil, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
			renders++
			r, _ := httpImageResult()
			return r, nil
		})
		for _, test := range []struct {
			method, path  string
			enabledStatus int
		}{
			{"GET", "/images/Primary/" + itemID, 200}, {"GET", "/images/Backdrop/" + itemID, 404}, {"GET", "/images/Poster/" + itemID, 400},
			{"GET", "/images/Primary/" + itemID + "?index=1", 400},
			{"GET", "/images/Primary/not-a-uuid", 400}, {"POST", "/images/Primary/" + itemID, 405},
		} {
			request := httptest.NewRequest(test.method, "http://localhost"+test.path, nil)
			request.Header.Set("Authorization", "Bearer "+strings.Repeat("u", 43))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request)
			want := 404
			if enabled {
				want = test.enabledStatus
			}
			if w.Code != want {
				t.Fatalf("rollout=%v method=%s path=%s status=%d", enabled, test.method, test.path, w.Code)
			}
		}
		if enabled && renders != 1 || !enabled && renders != 0 {
			t.Fatal("invalid route reached image renderer")
		}
		spec := Specification(cfg)
		paths := spec["paths"].(map[string]any)
		route, exists := paths["/images/{type}/{id}"]
		if exists != enabled {
			t.Fatal("image OpenAPI rollout differs")
		}
		if enabled {
			methods := route.(map[string]any)
			for _, method := range []string{"get", "head"} {
				op := methods[method].(map[string]any)
				responses := op["responses"].(map[string]any)
				if op["security"] == nil || responses["304"] == nil {
					t.Fatal("conditional authorization missing")
				}
				_, hasContent := responses["200"].(map[string]any)["content"]
				if hasContent != (method == "get") {
					t.Fatal("HEAD documented with body")
				}
			}
		}
		if _, err := json.Marshal(spec); err != nil {
			t.Fatal(err)
		}
	}
}
