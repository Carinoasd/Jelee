package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

type extractedFunc func(context.Context, access.Principal, string, ExtractedKind, int) (Source, error)

func (f extractedFunc) ResolveExtracted(ctx context.Context, p access.Principal, id string, kind ExtractedKind, index int) (Source, error) {
	return f(ctx, p, id, kind, index)
}

func TestExtractedContentTypesComeFromFixedTables(t *testing.T) {
	for _, tc := range []struct {
		kind      ExtractedKind
		extension string
		want      string
	}{
		{ExtractedSubtitle, ".srt", "application/x-subrip; charset=UTF-8"},
		{ExtractedSubtitle, "ass", "text/x-ssa; charset=UTF-8"},
		{ExtractedSubtitle, ".VTT", "text/vtt; charset=UTF-8"},
		{ExtractedOCRSubtitle, ".srt", "application/x-subrip; charset=UTF-8"},
		{ExtractedAttachment, ".ttf", "font/ttf"},
		{ExtractedAttachmentStream, ".woff2", "font/woff2"},
		{ExtractedAttachment, ".otc", "font/collection"},
		{ExtractedAttachment, ".html", "application/octet-stream"},
	} {
		if got := ExtractedContentType(tc.kind, tc.extension); got != tc.want {
			t.Errorf("%s %s = %q", tc.kind, tc.extension, got)
		}
	}
}

func TestServeExtractedUsesTheLongLookupAndRefusesInvalidCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a1.ttf"), []byte("font"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return Source{}, ErrNotFound }), Options{MaxConcurrent: 1, WriteTimeout: time.Second, LookupTimeout: time.Millisecond, ExtractTimeout: time.Second, WriteError: testWriteError})
	if err != nil {
		t.Fatal(err)
	}
	slow := extractedFunc(func(ctx context.Context, _ access.Principal, _ string, kind ExtractedKind, index int) (Source, error) {
		// Longer than the ordinary lookup bound, within the extraction one.
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return Source{}, ctx.Err()
		}
		if kind != ExtractedAttachment || index != 1 {
			return Source{}, ErrNotFound
		}
		return Source{Root: root, RelativePath: "a1.ttf", ContentType: "text/html"}, nil
	})
	w := httptest.NewRecorder()
	handler.ServeExtracted(w, nativeRequest(http.MethodGet, "/x"), slow, "source", ExtractedAttachment, 1)
	if w.Code != 200 || w.Body.String() != "font" || w.Header().Get("Content-Type") != "font/ttf" {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	for _, call := range []struct {
		resolver ExtractedResolver
		kind     ExtractedKind
		index    int
	}{{nil, ExtractedAttachment, 1}, {slow, "other", 1}, {slow, ExtractedAttachment, -1}, {slow, ExtractedSubtitle, 1}} {
		w := httptest.NewRecorder()
		handler.ServeExtracted(w, nativeRequest(http.MethodGet, "/x"), call.resolver, "source", call.kind, call.index)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%+v: %d", call, w.Code)
		}
	}
}
