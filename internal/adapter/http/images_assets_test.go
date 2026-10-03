package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type httpAssetResolver func(context.Context, domain.Actor, string, string, int) ([]domain.ItemImage, error)

func (f httpAssetResolver) ResolveItemImageSources(ctx context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error) {
	return f(ctx, actor, item, imageType, index)
}

type httpAssetRenderer func(context.Context, domain.ItemImage, domain.ImageRequest) (app.ImageResult, error)

func (f httpAssetRenderer) RenderItemImage(ctx context.Context, value domain.ItemImage, request domain.ImageRequest) (app.ImageResult, error) {
	return f(ctx, value, request)
}

var httpImageContent = sha256.Sum256([]byte("original artwork"))

func assetImageRouter(t *testing.T, seen *[]domain.ImageRequest) http.Handler {
	t.Helper()
	legacy, err := app.NewImages(httpImageRepository(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		return domain.LocalImageSource{ItemID: itemID}, nil
	}), httpImageRenderer(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
		result, _ := httpImageResult()
		return result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	service, err := legacy.WithAssets(httpAssetResolver(func(_ context.Context, _ domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error) {
		return []domain.ItemImage{{ID: "row", ItemID: item, Type: imageType, Index: index, SourceKind: domain.ImageSourceLocal}}, nil
	}), httpAssetRenderer(func(_ context.Context, _ domain.ItemImage, request domain.ImageRequest) (app.ImageResult, error) {
		*seen = append(*seen, request)
		result, _ := httpImageResult()
		result.ContentSHA256 = httpImageContent
		return result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewWithImages(httpImagesConfig(t), &metricsHTTPBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t), nil, nil, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assetImageRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, "http://localhost"+path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("u", 43))
	return r
}

func TestImagesHTTPTypesAndIndex(t *testing.T) {
	var seen []domain.ImageRequest
	h := assetImageRouter(t, &seen)
	for _, imageType := range []string{"Primary", "Backdrop", "Logo", "ClearLogo", "Banner", "ClearArt", "Art", "Disc", "Thumb", "Landscape", "Chapter", "Box", "BoxRear", "Menu", "Profile"} {
		seen = seen[:0]
		w := httptest.NewRecorder()
		h.ServeHTTP(w, assetImageRequest("GET", "/images/"+imageType+"/"+itemID))
		if w.Code != 200 || len(seen) != 1 || seen[0].Type != imageType || seen[0].Index != 0 {
			t.Fatalf("type %s status=%d seen=%+v", imageType, w.Code, seen)
		}
	}
	for path, want := range map[string]domain.ImageRequest{
		"/images/Backdrop/" + itemID + "?index=3":                 {Type: "Backdrop", Index: 3},
		"/images/Fanart/" + itemID + "?index=9999&width=100":      {Type: "Backdrop", Index: 9999},
		"/images/Chapter/" + itemID + "?index=12":                 {Type: "Chapter", Index: 12},
		"/images/Primary/" + itemID + "?index=0":                  {Type: "Primary"},
		"/images/Backdrop/" + itemID + "?index=0&tag=" + "abcdef": {Type: "Backdrop"},
	} {
		seen = seen[:0]
		w := httptest.NewRecorder()
		h.ServeHTTP(w, assetImageRequest("GET", path))
		if w.Code != 200 || len(seen) != 1 || seen[0].Type != want.Type || seen[0].Index != want.Index {
			t.Fatalf("%s status=%d seen=%+v", path, w.Code, seen)
		}
	}
	seen = seen[:0]
	for _, path := range []string{
		"/images/Backdrop/" + itemID + "?index=-1", "/images/Backdrop/" + itemID + "?index=01", "/images/Backdrop/" + itemID + "?index=10000",
		"/images/Backdrop/" + itemID + "?index=x", "/images/Backdrop/" + itemID + "?index=", "/images/Backdrop/" + itemID + "?index=1&index=2",
		"/images/Logo/" + itemID + "?index=1", "/images/Primary/" + itemID + "?index=2", "/images/Poster/" + itemID,
		"/images/Primary/" + itemID + "?tag=", "/images/Primary/" + itemID + "?tag=xyz", "/images/Primary/" + itemID + "?tag=" + strings.Repeat("a", 129),
		"/images/Primary/" + itemID + "?tag=a&tag=b", "/images/Primary/" + itemID + "?format=webp",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, assetImageRequest("GET", path))
		if w.Code != 400 {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
	if len(seen) != 0 {
		t.Fatal("invalid request reached the renderer")
	}
}

func TestImagesHTTPContentTagSelectsImmutableCaching(t *testing.T) {
	var seen []domain.ImageRequest
	h := assetImageRouter(t, &seen)
	tag := hex.EncodeToString(httpImageContent[:])
	const immutable, private = "public, max-age=31536000, immutable", "private, no-cache, must-revalidate"
	for _, test := range []struct {
		method, query, conditional, cache string
		status                            int
	}{
		{"GET", "?tag=" + tag, "", immutable, 200},
		{"GET", "?tag=" + strings.ToUpper(tag), "", immutable, 200},
		{"HEAD", "?tag=" + tag, "", immutable, 200},
		{"GET", "?tag=" + tag, httpImageETag, immutable, 304},
		{"HEAD", "?tag=" + tag + "&width=10", "W/" + httpImageETag, immutable, 304},
		{"GET", "?tag=" + strings.Repeat("f", 64), "", private, 200},
		{"GET", "?tag=" + tag[:32], "", private, 200},
		{"GET", "", "", private, 200},
		{"GET", "", httpImageETag, private, 304},
	} {
		r := assetImageRequest(test.method, "/images/Backdrop/"+itemID+test.query)
		if test.conditional != "" {
			r.Header.Set("If-None-Match", test.conditional)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		bodyWanted := test.status == 200 && test.method == "GET"
		if w.Code != test.status || w.Header().Get("Cache-Control") != test.cache || w.Header().Get("ETag") != httpImageETag ||
			w.Header().Get("Vary") != "Authorization" || (w.Body.Len() > 0) != bodyWanted {
			t.Fatalf("%s %s: status=%d cache=%q", test.method, test.query, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	// The local poster fallback has no recorded content digest here; a tag
	// can never upgrade it to immutable caching.
	legacy := imageRouter(t, httpImagesConfig(t), &metricsHTTPBackend{}, nil, func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
		result, _ := httpImageResult()
		return result, nil
	})
	w := httptest.NewRecorder()
	legacy.ServeHTTP(w, imageHTTPRequest("GET", "?tag="+strings.Repeat("0", 64), "u"))
	if w.Code != 200 || w.Header().Get("Cache-Control") != private {
		t.Fatal("zero digest matched a tag")
	}
}
