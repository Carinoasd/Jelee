package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
)

// The shared sample probe (formatsamples.ProbeJSON) must be the sealed
// sidecar argv, or the acceptance tests would probe something production
// never runs.
func TestFormatSamplesUseTheSealedSidecarArguments(t *testing.T) {
	if got, want := formatsamples.SidecarProbeArguments(), sidecarArguments(); !slices.Equal(got, want) {
		t.Fatalf("sample probe argv drifted from the sealed one:\n%q\n%q", got, want)
	}
	if formatsamples.SidecarDemuxers != SidecarFormatWhitelist {
		t.Fatal("sample demuxer list drifted from SidecarFormatWhitelist")
	}
}

// TestSidecarFormatSamplesRealFFprobe (W10-14) probes every sample format
// with the sealed argv: each audio sample must report its codec and channel
// count, each probed subtitle sample its codec. A format the local ffmpeg
// cannot encode is skipped with the reason, never counted as passed. Without
// ffmpeg and ffprobe it skips unless JELEE_REQUIRE_MEDIA_TOOL_TESTS=true.
func TestSidecarFormatSamplesRealFFprobe(t *testing.T) {
	ffprobe, errProbe := exec.LookPath("ffprobe")
	ffmpeg, errMpeg := exec.LookPath("ffmpeg")
	handmade := false
	if errProbe != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatal("ffprobe is required")
		}
		t.Skip("ffprobe not available")
	}
	type stream struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Channels  int    `json:"channels"`
	}
	probe := func(path string) []stream {
		raw, err := formatsamples.ProbeJSON(ffprobe, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var document struct {
			Streams []stream `json:"streams"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		return document.Streams
	}
	dir := t.TempDir()
	probed, skipped := 0, 0
	for _, spec := range formatsamples.AudioSpecs() {
		if !spec.Handmade && errMpeg != nil {
			if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
				t.Fatal("ffmpeg is required")
			}
			t.Logf("SKIP %s: ffmpeg not available", spec.File)
			skipped++
			continue
		}
		path, err := formatsamples.Encode(ffmpeg, dir, spec)
		if errors.Is(err, formatsamples.ErrNoEncoder) {
			t.Logf("SKIP %s: the local ffmpeg cannot encode it: %s", spec.File, strings.ReplaceAll(err.Error(), "\n", " | "))
			skipped++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		handmade = handmade || spec.Handmade
		streams := probe(path)
		if len(streams) != 1 || streams[0].CodecType != "audio" || streams[0].CodecName != spec.Codec || streams[0].Channels != spec.Channels {
			t.Errorf("%s: want %s x%d, got %+v", spec.File, spec.Codec, spec.Channels, streams)
			continue
		}
		probed++
	}
	for _, sample := range formatsamples.Subtitles() {
		if sample.ProbeCodec == "" {
			continue
		}
		path := dir + "/" + sample.File
		if err := os.WriteFile(path, sample.Data, 0o600); err != nil {
			t.Fatal(err)
		}
		streams := probe(path)
		if len(streams) == 0 || streams[0].CodecType != "subtitle" || streams[0].CodecName != sample.ProbeCodec {
			t.Errorf("%s: want subtitle %s, got %+v", sample.File, sample.ProbeCodec, streams)
			continue
		}
		probed++
	}
	t.Logf("probed %d sample files, skipped %d for lack of an encoder; formats with no sample: %v", probed, skipped, formatsamples.MissingAudio)
	if !handmade {
		t.Error("the hand-made MP3 must always be probed")
	}
}
