//go:build linux || windows

package postgres

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

// ffprobeSidecarProber runs the system ffprobe with the sealed sidecar
// argument list in place of the sandboxed runtime.
type ffprobeSidecarProber struct{ root, ffprobe string }

func (p ffprobeSidecarProber) ProbeSidecar(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error) {
	path := filepath.Join(p.root, filepath.FromSlash(source.RelativePath))
	info, err := os.Stat(path)
	if err != nil {
		return domain.ProbeObservation{}, domain.ErrProbeInputUnavailable
	}
	raw, err := formatsamples.ProbeJSON(p.ffprobe, path)
	if err != nil {
		return domain.ProbeObservation{}, domain.ErrProbeFailed
	}
	meta, err := probe.ParseJSON(raw)
	if err != nil {
		return domain.ProbeObservation{}, domain.ErrProbeFailed
	}
	return domain.ProbeObservation{Metadata: meta, Stamp: domain.ProbeStamp{Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}}, ctx.Err()
}

// sampleProfiles are the codec profiles ffprobe reports for the samples;
// the other codecs report none. A mono WAV reports no channel layout.
var sampleProfiles = map[string]string{"aac": "LC", "dts": "DTS"}

// TestFormatSamplesThroughCatalogSync (G15, G16; W10-14) is the acceptance
// path on a real database: every sample is written beside a video, a catalog
// sync names, inspects, pairs and probes it with the real ffprobe, playback
// information lists it with the facts, and the direct route returns the
// original bytes under the format's content type. Audio the local ffmpeg
// cannot encode is skipped with the reason; without ffprobe the test skips.
func TestFormatSamplesThroughCatalogSync(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatal("ffprobe is required")
		}
		t.Skip("ffprobe not available")
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	made, skipped, err := formatsamples.EncodeAll(ffmpeg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for file, reason := range skipped {
		t.Logf("SKIP %s: %s", file, reason)
	}
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	root := syncRootPath(t, f)
	const video = "Movie (2001)/Movie (2001)"
	writeSidecarFile(t, root, video+".mkv", []byte("video"))
	type expected struct {
		path, kind, format, charset, subtitleKind, codec string
		data                                             []byte
		channels                                         int
		probed                                           bool
	}
	var samples []expected
	for _, s := range formatsamples.Subtitles() {
		path := "Movie (2001)/" + s.Name("Movie (2001)")
		writeSidecarFile(t, root, path, s.Data)
		samples = append(samples, expected{path: path, kind: domain.SidecarKindSubtitle, format: s.Format, charset: s.Charset, subtitleKind: s.SubtitleKind, codec: s.Codec, data: s.Data, probed: s.ProbeCodec != ""})
	}
	for _, a := range made {
		path := "Movie (2001)/Movie (2001)." + a.Spec.File
		writeSidecarFile(t, root, path, a.Data)
		samples = append(samples, expected{path: path, kind: domain.SidecarKindAudio, format: a.Spec.Format, codec: a.Spec.Codec, data: a.Data, channels: a.Spec.Channels, probed: true})
	}
	enableAutoSync(t, f)
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval, opts.ScanConcurrency = 1, 100*time.Millisecond, 2
	opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s, Sidecars: f.s, Inspector: scan.SidecarInspector{},
		SidecarProbes: &jobs.SidecarProbeOptions{Repository: f.s, Prober: ffprobeSidecarProber{root: root, ffprobe: ffprobe}}}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	f.submit(t, "format-samples-1")
	waitJobsIdle(t, f)

	stored := probedTracks(t, f)
	if len(stored) != len(samples) {
		t.Fatalf("stored %d tracks for %d samples", len(stored), len(samples))
	}
	reader := trackPrincipal(t, f, "format-samples-reader", access.ClientNative, true)
	actor := domain.Actor{UserID: reader.UserID, SessionID: reader.SessionID}
	source := sourceOf(t, f, video+".mkv")
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&item); err != nil {
		t.Fatal(err)
	}
	records, err := f.s.ListPlaybackSources(f.ctx, actor, item)
	if err != nil || len(records) != 1 {
		t.Fatal("playback sources", err)
	}
	external := map[string]domain.PlaybackExternalTrack{}
	for _, track := range domain.BuildPlaybackSource(records[0]).External {
		external[track.ID] = track
	}
	var paired, hidden int
	for _, want := range samples {
		got := stored[want.path]
		if got.id == "" {
			t.Errorf("%s was not stored", want.path)
			continue
		}
		vobsubData := want.format == "sub" && want.subtitleKind == domain.SidecarSubtitleVobSub
		if got.kind != want.subtitleKind || got.charset != want.charset {
			t.Errorf("%s: stored kind %q charset %q, want %q %q", want.path, got.kind, got.charset, want.subtitleKind, want.charset)
		}
		switch {
		case want.probed:
			if got.state != domain.SidecarProbeReady || got.codec == "" {
				t.Errorf("%s: probe state %q codec %q", want.path, got.state, got.codec)
			}
			if want.kind == domain.SidecarKindAudio && got.channels != want.channels {
				t.Errorf("%s: %d channels, want %d", want.path, got.channels, want.channels)
			}
		case want.format == "idx":
			if got.state != domain.SidecarProbeNotApplicable || got.paired == "" {
				t.Errorf("%s: .idx state %q paired %q", want.path, got.state, got.paired)
			}
			paired++
		default:
			if got.state != domain.SidecarProbeNotApplicable {
				t.Errorf("%s: text subtitle probe state %q", want.path, got.state)
			}
		}
		track, listed := external[got.id]
		if vobsubData {
			if listed {
				t.Errorf("%s: paired data half is listed", want.path)
			}
			hidden++
		} else if !listed || track.Codec != want.codec || track.Charset != want.charset || track.SizeBytes != int64(len(want.data)) {
			t.Errorf("%s: playback track %+v (listed %v), want codec %q", want.path, track, listed, want.codec)
		}
		if want.kind == domain.SidecarKindAudio && listed && (track.Channels != want.channels || track.SampleRate == 0 || (track.ChannelLayout == "" && want.format != "wav") || track.Profile != sampleProfiles[want.codec]) {
			t.Errorf("%s: audio facts %+v", want.path, track)
		}
		w := sidecarRoute(t, f, reader, source, media.TrackKind(want.kind), got.id)
		wantType := media.TrackContentType(media.TrackKind(want.kind), want.format, want.charset, want.subtitleKind)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want.data) || w.Header().Get("Content-Type") != wantType {
			t.Errorf("%s: route %d, %d of %d bytes, type %q want %q", want.path, w.Code, w.Body.Len(), len(want.data), w.Header().Get("Content-Type"), wantType)
		}
		if after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(want.path))); err != nil || !bytes.Equal(after, want.data) {
			t.Errorf("%s: the original changed", want.path)
		}
	}
	if paired != 1 || hidden != 1 {
		t.Errorf("paired %d idx rows and %d hidden data halves", paired, hidden)
	}
}
