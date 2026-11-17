package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// sampleResolver serves whichever sample the test selected, as the store's
// resolver does for a stored sidecar row.
type sampleResolver struct {
	root, name, charset, subtitleKind string
}

func (r *sampleResolver) Resolve(context.Context, access.Principal, string) (Source, error) {
	return Source{}, ErrNotFound
}

func (r *sampleResolver) ResolveTrack(_ context.Context, _ access.Principal, _ string, _ TrackKind, _ string) (Source, error) {
	return Source{Root: r.root, RelativePath: r.name, Charset: r.charset, SubtitleKind: r.subtitleKind, ContentType: "text/html"}, nil
}

// The expected types are written out here, apart from the table in tracks.go
// they check.
var sampleSubtitleTypes = map[string]string{
	"srt": "application/x-subrip", "ass": "text/x-ssa", "ssa": "text/x-ssa", "vtt": "text/vtt", "webvtt": "text/vtt",
	"ttml": "application/ttml+xml", "dfxp": "application/ttml+xml", "smi": "application/x-sami", "sami": "application/x-sami",
	"idx": "text/plain", "sup": "application/x-pgs",
}

var sampleAudioTypes = map[string]string{
	"mka": "audio/x-matroska", "aac": "audio/aac", "m4a": "audio/mp4", "ac3": "audio/ac3", "eac3": "audio/eac3", "ec3": "audio/eac3",
	"dts": "audio/vnd.dts", "dtshd": "audio/vnd.dts.hd", "thd": "audio/vnd.dolby.mlp", "truehd": "audio/vnd.dolby.mlp", "mlp": "audio/vnd.dolby.mlp",
	"flac": "audio/flac", "opus": "audio/opus", "ogg": "audio/ogg", "oga": "audio/ogg", "mp3": "audio/mpeg", "wav": "audio/wav",
}

func serveSample(t *testing.T, handler *Handler, kind TrackKind, method, ranges string) *httptest.ResponseRecorder {
	t.Helper()
	r := nativeRequest(method, "/track")
	if ranges != "" {
		r.Header.Set("Range", ranges)
	}
	w := httptest.NewRecorder()
	handler.ServeTrack(w, r, "source", kind, "track")
	return w
}

// TestFormatSamplesDeliveredAsTheyAre (G10.9, G15.1, G16.1; W10-14): every
// sample format comes back byte for byte (full, ranged and HEAD), with the
// content type its format names and, for text, the detected charset as a
// parameter only.
func TestFormatSamplesDeliveredAsTheyAre(t *testing.T) {
	root := t.TempDir()
	type delivery struct {
		name, charset, subtitleKind, wantType string
		kind                                  TrackKind
		data                                  []byte
	}
	var cases []delivery
	for _, s := range formatsamples.Subtitles() {
		want := sampleSubtitleTypes[s.Format]
		switch {
		case s.Format == "sub" && s.SubtitleKind == domain.SidecarSubtitleVobSub:
			want = "application/octet-stream"
		case s.Format == "sub":
			want = "text/x-microdvd"
		}
		if s.Charset != "" {
			want += "; charset=" + s.Charset
		}
		cases = append(cases, delivery{s.File + "." + s.Format, s.Charset, s.SubtitleKind, want, TrackSubtitle, s.Data})
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	made, skipped, err := formatsamples.EncodeAll(ffmpeg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for file, reason := range skipped {
		t.Logf("SKIP %s: %s", file, reason)
	}
	for _, a := range made {
		cases = append(cases, delivery{a.Spec.File, "", "", sampleAudioTypes[a.Spec.Format], TrackAudio, a.Data})
	}
	handmade := false
	for _, c := range cases {
		handmade = handmade || c.name == "handmade.mp3"
	}
	if !handmade {
		t.Fatal("the hand-made MP3 must always be delivered")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.wantType == "" || (c.wantType == "application/octet-stream" && c.kind == TrackAudio) {
				t.Fatalf("no expected type for %s", c.name)
			}
			// Written under the name the route sees: the sample's file name
			// already ends in its format extension for audio, and gains it here
			// for subtitles whose file name is only a label.
			name := c.name
			if err := os.WriteFile(filepath.Join(root, name), c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(c.data)
			handler := testHandler(t, &sampleResolver{root: root, name: name, charset: c.charset, subtitleKind: c.subtitleKind}, 2)
			full := serveSample(t, handler, c.kind, "GET", "")
			if full.Code != 200 || !bytes.Equal(full.Body.Bytes(), c.data) {
				t.Fatalf("full: %d, %d of %d bytes", full.Code, full.Body.Len(), len(c.data))
			}
			if got := full.Header().Get("Content-Type"); got != c.wantType {
				t.Fatalf("content type %q, want %q", got, c.wantType)
			}
			if full.Header().Get("X-Content-Type-Options") != "nosniff" || full.Header().Get("Content-Disposition") != "" {
				t.Fatalf("headers %v", full.Header())
			}
			if len(c.data) > 8 {
				part := serveSample(t, handler, c.kind, "GET", "bytes=2-7")
				if part.Code != 206 || !bytes.Equal(part.Body.Bytes(), c.data[2:8]) {
					t.Fatalf("range: %d %q", part.Code, part.Body.Bytes())
				}
			}
			head := serveSample(t, handler, c.kind, "HEAD", "")
			if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(c.data)) {
				t.Fatalf("head: %d %v", head.Code, head.Header())
			}
			if after, err := os.ReadFile(filepath.Join(root, name)); err != nil || sha256.Sum256(after) != before {
				t.Fatal("delivery changed the original")
			}
		})
	}
}

// The formats with no sample still get their content type, so a user's
// DTS-HD or ALAC file is delivered with the right (or an opaque) type.
func TestFormatsWithoutSamplesKeepTheirContentTypes(t *testing.T) {
	for format := range formatsamples.MissingAudio {
		want, known := sampleAudioTypes[format]
		if !known {
			want = "application/octet-stream" // ".alac" names no container
		}
		if got := TrackContentType(TrackAudio, format, "", ""); got != want {
			t.Errorf(".%s: %q, want %q", format, got, want)
		}
	}
	for _, unknown := range []string{"", "xyz", "m4b", "wma"} {
		if got := TrackContentType(TrackAudio, unknown, "", ""); got != "application/octet-stream" {
			t.Errorf(".%s: %q", unknown, got)
		}
	}
}
