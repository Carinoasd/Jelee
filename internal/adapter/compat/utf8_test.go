package compat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// fakeUTF8 has a copy of the ASS track only; any other track has none.
type fakeUTF8 struct {
	root  string
	calls []string
}

func (f *fakeUTF8) ResolveUTF8Track(_ context.Context, _ access.Principal, _, trackID string) (media.Source, error) {
	f.calls = append(f.calls, trackID)
	if trackID == testAssTrackID {
		return media.Source{Root: f.root, RelativePath: "copy.ass"}, nil
	}
	return media.Source{}, media.ErrNotDerived
}

// A text subtitle in another charset is the UTF-8 copy at the same
// upstream URL (G15.4, design point 4); an undetermined one, or one whose
// copy was withheld, is the original; a UTF-8 one never asks for a copy.
// The listing is unchanged: same Codec, same DeliveryUrl.
func TestSubtitleStreamDeliversUTF8Copies(t *testing.T) {
	root := t.TempDir()
	copyBytes := []byte("[Script Info]\nTitle: 標誌\n")
	for name, data := range map[string][]byte{"movie.zho.ass": testAssBytes, "movie.eng.srt": testSrtBytes, "copy.ass": copyBytes} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &fakeResolver{root: root}
	delivery, err := media.NewHandler(resolver, media.Options{MaxConcurrent: 4, WriteTimeout: 5 * time.Second, WriteError: func(w http.ResponseWriter, _ *http.Request, err error) {
		if errors.Is(err, media.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}})
	if err != nil {
		t.Fatal(err)
	}
	utf8 := &fakeUTF8{root: root}
	h := newLibraryHarnessConfig(t, http.StatusNotFound, func(o *LibraryOptions) { o.DirectPlay, o.Delivery, o.UTF8 = true, delivery, utf8 })
	sources, _ := h.catalog.PlaybackSources(context.Background(), domain.Actor{}, testMovieID)
	sources[0].External[0].CharsetState = domain.SidecarCharsetConvertible
	sources[0].External[1].CharsetState = domain.SidecarCharsetUTF8
	h.catalog.sources = sources
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	assURL := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/3/0/Stream.ass"
	srtURL := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/4/0/Stream.srt"
	if !bytes.Contains([]byte(info), []byte(`"Codec":"ass"`)) || !bytes.Contains([]byte(info), []byte(`"DeliveryUrl":"`+assURL+`"`)) {
		t.Fatalf("listing changed: %s", info)
	}
	if w := h.get("/compat"+assURL, nativeToken); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), copyBytes) || w.Header().Get("Content-Type") != "text/x-ssa; charset=UTF-8" {
		t.Fatalf("convertible: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
	if w := h.get("/compat"+srtURL, nativeToken); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), testSrtBytes) || len(utf8.calls) != 1 {
		t.Fatalf("utf8 track: %d %q calls %v", w.Code, w.Body, utf8.calls)
	}
	sources[0].External[1].CharsetState = domain.SidecarCharsetUndetermined
	if w := h.get("/compat"+srtURL, nativeToken); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), testSrtBytes) || len(utf8.calls) != 2 {
		t.Fatalf("undetermined falls back: %d %q calls %v", w.Code, w.Body, utf8.calls)
	}
	if w := h.get("/compat"+assURL, webToken); w.Code == http.StatusOK {
		t.Fatal("web session received the copy")
	}
}
