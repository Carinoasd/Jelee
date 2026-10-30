package compat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// sampleTracks resolves a stored sample the way the store's resolver does:
// the path, the recorded charset and the content kind of a ".sub".
type sampleTracks struct {
	root   string
	tracks map[string]domain.SidecarTrackRecord
}

func (r *sampleTracks) Resolve(context.Context, access.Principal, string) (media.Source, error) {
	return media.Source{}, media.ErrNotFound
}

func (r *sampleTracks) ResolveTrack(_ context.Context, _ access.Principal, sourceID string, _ media.TrackKind, trackID string) (media.Source, error) {
	track, ok := r.tracks[trackID]
	if !ok || sourceID != testSourceID {
		return media.Source{}, media.ErrNotFound
	}
	return media.Source{Root: r.root, RelativePath: track.RelativePath, Charset: track.Charset, SubtitleKind: track.SubtitleKind}, nil
}

// TestCompatListsAndDeliversEveryFormatSample (G15, G16, G24.2; W10-14): each
// sample goes through the scan's inspection, the sandbox argv probe (when
// ffprobe is on the machine), the playback projection and the compatibility
// layer's PlaybackInfo, and the DeliveryUrl it lists returns the original
// bytes with the content type of the format. Probed facts, when present,
// appear in the listed stream.
func TestCompatListsAndDeliversEveryFormatSample(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	ffprobe, probeErr := exec.LookPath("ffprobe")
	ffmpeg, _ := exec.LookPath("ffmpeg")
	made, skipped, err := formatsamples.EncodeAll(ffmpeg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for file, reason := range skipped {
		t.Logf("SKIP %s: %s", file, reason)
	}
	if probeErr != nil {
		t.Logf("ffprobe not available: probe-dependent facts are not asserted")
	}

	var samples []*formatSample
	sequence := 0
	newID := func() string {
		sequence++
		return fmt.Sprintf("f2000000-0000-4000-8000-%012x", sequence)
	}
	stage := func(name string, data []byte, kind string) domain.SidecarTrackRecord {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		track, ok := domain.ParseSidecarName("movie", name, "")
		if !ok || track.Kind != kind {
			t.Fatalf("%s is not named as a %s track", name, kind)
		}
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		record := domain.SidecarTrackRecord{ID: newID(), SourceID: testSourceID, RelativePath: name, Track: track, Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano(),
			Probe: domain.SidecarProbeRecord{State: domain.InitialSidecarProbeState(track.Kind, track.Format)}}
		inspection, err := scan.SidecarInspector{}.InspectSidecar(ctx, domain.SidecarInspectionTarget{ID: record.ID, Kind: kind, Format: track.Format, RootPath: root, RelativePath: name, Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		record.Charset, record.SubtitleKind, record.Fingerprint = inspection.Charset, inspection.SubtitleKind, inspection.Fingerprint
		if record.Probe.State == domain.SidecarProbePending && probeErr == nil {
			raw, err := formatsamples.ProbeJSON(ffprobe, filepath.Join(root, name))
			if err != nil {
				t.Fatalf("%s: probe: %v", name, err)
			}
			meta, err := probe.ParseJSON(raw)
			if err != nil {
				t.Fatalf("%s: parse: %v", name, err)
			}
			facts, ok := domain.SidecarProbeFactsFrom(kind, meta)
			if !ok {
				t.Fatalf("%s: probe found no stream", name)
			}
			record.Probe = domain.SidecarProbeRecord{State: domain.SidecarProbeReady, Facts: facts}
		}
		return record
	}
	for _, s := range formatsamples.Subtitles() {
		record := stage(s.Name("movie"), s.Data, domain.SidecarKindSubtitle)
		samples = append(samples, &formatSample{record: record, data: s.Data, codec: s.Codec, kind: domain.SidecarKindSubtitle, charset: s.Charset, probed: s.ProbeCodec != ""})
	}
	for _, a := range made {
		record := stage("movie."+a.Spec.File, a.Data, domain.SidecarKindAudio)
		samples = append(samples, &formatSample{record: record, data: a.Data, codec: a.Spec.Codec, kind: domain.SidecarKindAudio, probed: true, channels: a.Spec.Channels, noDuration: a.Spec.NoDuration})
	}
	// The ".idx" names the data half it is stored with; the store pairs them
	// the same way (same directory, same name, ".sub").
	for _, idx := range samples {
		if idx.record.Track.Format != "idx" {
			continue
		}
		for _, sub := range samples {
			if sub.record.Track.Format == "sub" && sub.record.SubtitleKind == domain.SidecarSubtitleVobSub && strings.TrimSuffix(sub.record.RelativePath, ".sub") == strings.TrimSuffix(idx.record.RelativePath, ".idx") {
				idx.record.PairedTrackID, idx.record.Probe.State = sub.record.ID, domain.SidecarProbeNotApplicable
			}
		}
		if idx.record.PairedTrackID == "" {
			t.Fatal("the VobSub index has no data half")
		}
	}

	records := make([]domain.SidecarTrackRecord, len(samples))
	byID := map[string]domain.SidecarTrackRecord{}
	for i, s := range samples {
		records[i] = s.record
		byID[s.record.ID] = s.record
	}
	source := domain.BuildPlaybackSource(domain.PlaybackSourceRecord{ID: testSourceID, ContentType: "video/x-matroska", FileName: "movie.mkv", Sidecars: records})
	source.Container, source.Probed = "mkv", true

	// Own API model first: the track list names every sample and hides the
	// paired data half.
	external := map[string]domain.PlaybackExternalTrack{}
	for _, e := range source.External {
		external[e.ID] = e
	}
	for _, s := range samples {
		got, listed := external[s.record.ID]
		if s.record.Track.Format == "sub" && s.record.SubtitleKind == domain.SidecarSubtitleVobSub {
			if listed {
				t.Errorf("%s: the paired data half is listed", s.record.RelativePath)
			}
			continue
		}
		if !listed {
			t.Errorf("%s: not listed", s.record.RelativePath)
			continue
		}
		wantCodec := s.codec
		if s.kind == domain.SidecarKindAudio && probeErr != nil {
			wantCodec = extensionCodec[s.record.Track.Format] // empty for mka, m4a, ogg, oga and wav until probed
		}
		if got.Codec != wantCodec || got.Charset != s.charset || got.Kind != s.kind || got.SizeBytes != int64(len(s.data)) {
			t.Errorf("%s: playback track %+v, want codec %q charset %q", s.record.RelativePath, got, wantCodec, s.charset)
		}
		if s.kind == domain.SidecarKindAudio && probeErr == nil && (got.ProbeState != domain.SidecarProbeReady || got.Channels != s.channels || got.SampleRate == 0 || (!s.noDuration && got.DurationMicros <= 0)) {
			t.Errorf("%s: probed facts missing: %+v", s.record.RelativePath, got)
		}
	}

	resolver := &sampleTracks{root: root, tracks: byID}
	delivery, err := media.NewHandler(resolver, media.Options{MaxConcurrent: 4, WriteTimeout: 5 * time.Second, WriteError: func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.WriteHeader(http.StatusInternalServerError)
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := newLibraryHarnessWith(t, http.StatusNotFound, true, delivery)
	h.catalog.sources = []domain.PlaybackSource{source}
	w := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken)
	if w.Code != http.StatusOK {
		t.Fatalf("PlaybackInfo %d", w.Code)
	}
	var raw struct {
		MediaSources []struct {
			MediaStreams []struct {
				Type                   string
				Codec                  string
				Index                  int
				IsExternal             bool
				IsTextSubtitleStream   bool
				SupportsExternalStream bool
				Channels               int
				SampleRate             int
				DeliveryURL            string `json:"DeliveryUrl"`
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.MediaSources) != 1 {
		t.Fatalf("PlaybackInfo body: %v", err)
	}
	listed := map[string]int{}
	for _, stream := range raw.MediaSources[0].MediaStreams {
		if stream.IsExternal {
			listed[stream.DeliveryURL]++
		}
	}
	if len(listed) != len(source.External) {
		t.Fatalf("compat lists %d external streams for %d tracks", len(listed), len(source.External))
	}
	delivered := 0
	for _, e := range domain.UpstreamExternalTracks(source) {
		s := findSample(samples, e.Track.ID)
		var url string
		for candidate := range listed {
			if e.Track.Kind == domain.SidecarKindAudio && strings.Contains(candidate, fmt.Sprintf("/Audio/%d/Stream.", e.Index)) ||
				e.Track.Kind == domain.SidecarKindSubtitle && strings.Contains(candidate, fmt.Sprintf("/Subtitles/%d/0/Stream.", e.Index)) {
				url = candidate
			}
		}
		if url == "" {
			t.Errorf("%s: no DeliveryUrl listed", s.record.RelativePath)
			continue
		}
		for _, stream := range raw.MediaSources[0].MediaStreams {
			if stream.Index != e.Index || !stream.IsExternal || (stream.Type == "Audio") != (e.Track.Kind == domain.SidecarKindAudio) {
				continue
			}
			if !stream.SupportsExternalStream || stream.Codec == "" {
				t.Errorf("%s: stream %+v", s.record.RelativePath, stream)
			}
			if e.Track.Kind == domain.SidecarKindSubtitle && stream.IsTextSubtitleStream != compatTextSubtitles[s.codec] {
				t.Errorf("%s: IsTextSubtitleStream=%v for %s", s.record.RelativePath, stream.IsTextSubtitleStream, s.codec)
			}
		}
		if !strings.HasSuffix(url, "."+s.record.Track.Format) {
			t.Errorf("%s: DeliveryUrl %s does not end in the file's own format", s.record.RelativePath, url)
		}
		before := sha256.Sum256(s.data)
		got := h.get("/compat"+url, nativeToken)
		if got.Code != http.StatusOK || sha256.Sum256(got.Body.Bytes()) != before {
			t.Errorf("%s: delivery %d, %d of %d bytes", s.record.RelativePath, got.Code, got.Body.Len(), len(s.data))
			continue
		}
		wantType := media.TrackContentType(media.TrackKind(s.kind), s.record.Track.Format, s.record.Charset, s.record.SubtitleKind)
		if got.Header().Get("Content-Type") != wantType || strings.HasPrefix(wantType, "application/octet-stream") && s.record.SubtitleKind != domain.SidecarSubtitleVobSub {
			t.Errorf("%s: content type %q", s.record.RelativePath, got.Header().Get("Content-Type"))
		}
		delivered++
	}
	if delivered != len(source.External) {
		t.Fatalf("delivered %d of %d", delivered, len(source.External))
	}
	// Probed audio facts reach the compatibility stream.
	if probeErr == nil {
		seen := 0
		for _, stream := range raw.MediaSources[0].MediaStreams {
			if stream.IsExternal && stream.Type == "Audio" {
				if stream.Channels <= 0 || stream.SampleRate <= 0 || stream.Codec == "" {
					t.Errorf("external audio stream without facts: %+v", stream)
				}
				seen++
			}
		}
		if seen != len(made) {
			t.Errorf("%d external audio streams listed for %d audio samples", seen, len(made))
		}
	}
}

func findSample(samples []*formatSample, id string) *formatSample {
	for _, s := range samples {
		if s.record.ID == id {
			return s
		}
	}
	return nil
}

// formatSample is one staged sample and what the stages must report.
type formatSample struct {
	record domain.SidecarTrackRecord
	data   []byte
	// Expectations.
	codec, kind, charset string
	probed               bool
	channels             int
	noDuration           bool
}

// compatTextSubtitles says which subtitle codecs the compatibility layer
// reports as text streams. MicroDVD is text on disk but is reported as a
// non-text stream today (recorded in docs/fixtures.md).
var compatTextSubtitles = map[string]bool{"srt": true, "ass": true, "ssa": true, "webvtt": true, "ttml": true, "sami": true, "microdvd": false, "vobsub": false, "pgs": false}

// extensionCodec is what the extension alone names for audio, used when the
// sample is not probed (no ffprobe on the machine).
var extensionCodec = map[string]string{"aac": "aac", "ac3": "ac3", "eac3": "eac3", "ec3": "eac3", "dts": "dts", "thd": "truehd", "truehd": "truehd", "mlp": "mlp", "flac": "flac", "opus": "opus", "mp3": "mp3"}
