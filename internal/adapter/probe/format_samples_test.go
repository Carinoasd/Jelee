package probe

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestEmbeddedAudioFormatsAreReported (G16.3; W10-14) muxes every audio
// sample into a Matroska file, probes it with the production argument list
// and checks the playback projection of the embedded track: codec, channel
// count and sample rate. A sample the local ffmpeg cannot make or cannot
// copy into Matroska is skipped with the reason. Without ffmpeg and ffprobe
// it skips unless JELEE_REQUIRE_MEDIA_TOOL_TESTS=true.
func TestEmbeddedAudioFormatsAreReported(t *testing.T) {
	ffmpeg, errMpeg := exec.LookPath("ffmpeg")
	ffprobe, errProbe := exec.LookPath("ffprobe")
	if errMpeg != nil || errProbe != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatal("ffmpeg and ffprobe are required")
		}
		t.Skip("ffmpeg/ffprobe not available")
	}
	made, skipped, err := formatsamples.EncodeAll(ffmpeg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for file, reason := range skipped {
		t.Logf("SKIP %s: %s", file, reason)
	}
	dir := t.TempDir()
	seen := map[string]bool{}
	for _, sample := range made {
		if seen[sample.Spec.Codec] {
			continue // one embedded sample per codec is enough
		}
		out := filepath.Join(dir, sample.Spec.Codec+".mkv")
		mux := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-i", sample.Path, "-map", "0:a:0", "-c", "copy", out)
		if output, err := mux.CombinedOutput(); err != nil {
			t.Logf("SKIP %s: cannot copy into Matroska: %s", sample.Spec.File, strings.Join(strings.Fields(string(output)), " "))
			continue
		}
		seen[sample.Spec.Codec] = true
		file, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(ffprobe, FFprobeFDArguments()...)
		command.Stdin = file
		var stdout bytes.Buffer
		command.Stdout = &stdout
		runErr := command.Run()
		_ = file.Close()
		if runErr != nil {
			t.Errorf("%s: %v", sample.Spec.Codec, runErr)
			continue
		}
		meta, err := ParseJSON(stdout.Bytes())
		if err != nil {
			t.Errorf("%s: %v", sample.Spec.Codec, err)
			continue
		}
		source := domain.BuildPlaybackSource(domain.PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", Metadata: &meta})
		if len(source.Audio) != 1 {
			t.Errorf("%s: %d embedded audio tracks", sample.Spec.Codec, len(source.Audio))
			continue
		}
		track := source.Audio[0]
		if track.Codec != sample.Spec.Codec || track.Channels == nil || *track.Channels != int64(sample.Spec.Channels) || track.SampleRate == nil || *track.SampleRate <= 0 || track.Atmos {
			t.Errorf("%s: embedded track %+v", sample.Spec.Codec, track)
		}
	}
	if !seen["mp3"] {
		t.Error("the hand-made MP3 was not checked embedded")
	}
	if len(seen) < 8 {
		t.Errorf("only %d codecs reached the embedded check", len(seen))
	}
	t.Logf("embedded codecs checked: %d", len(seen))
}
