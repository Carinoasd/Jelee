package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
)

// TestSidecarArgumentsRealFFprobe runs the exact sealed sidecar argv (B4)
// with a real ffprobe, unsandboxed, on generated external tracks fed through
// stdin as the runner does. It checks that every format the whitelist names
// is actually opened and reports the expected codec, and that a demuxer
// outside the whitelist is refused. The sandbox itself is covered by the
// helper integration tests; this pins the argv against real demuxers.
// Without ffmpeg and ffprobe on PATH it skips, unless
// JELEE_REQUIRE_MEDIA_TOOL_TESTS=true.
func TestSidecarArgumentsRealFFprobe(t *testing.T) {
	ffmpeg, errMpeg := exec.LookPath("ffmpeg")
	ffprobe, errProbe := exec.LookPath("ffprobe")
	if errMpeg != nil || errProbe != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatal("ffmpeg and ffprobe are required")
		}
		t.Skip("ffmpeg/ffprobe not available")
	}
	dir := t.TempDir()
	tone := []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=1:sample_rate=48000"}
	generate := func(name string, args ...string) bool {
		command := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, tone...), append(args, filepath.Join(dir, name))...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Logf("encoder for %s unavailable: %v %s", name, err, output)
			return false
		}
		return true
	}
	cases := []struct {
		name, codec string
		args        []string
	}{
		{"a.dts", "dts", []string{"-ac", "6", "-c:a", "dca", "-strict", "-2"}},
		{"a.ac3", "ac3", []string{"-ac", "6", "-c:a", "ac3"}},
		{"a.eac3", "eac3", []string{"-ac", "6", "-c:a", "eac3"}},
		{"a.thd", "truehd", []string{"-ac", "2", "-c:a", "truehd", "-strict", "-2"}},
		{"a.mlp", "mlp", []string{"-ac", "2", "-c:a", "mlp", "-strict", "-2"}},
		{"a.flac", "flac", []string{"-c:a", "flac"}},
		{"a.aac", "aac", []string{"-c:a", "aac", "-f", "adts"}},
		{"a.m4a", "alac", []string{"-c:a", "alac"}},
		{"a.ogg", "vorbis", []string{"-ac", "2", "-c:a", "vorbis", "-strict", "-2"}},
		{"a.opus", "opus", []string{"-ac", "2", "-c:a", "opus", "-strict", "-2", "-f", "ogg"}},
		{"a.mp3", "mp3", []string{"-c:a", "libmp3lame"}},
		{"a.wav", "pcm_s16le", []string{"-c:a", "pcm_s16le"}},
		{"a.mka", "ac3", []string{"-c:a", "ac3"}},
	}
	type stream struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Channels  int    `json:"channels"`
	}
	run := func(name string) ([]stream, error) {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		command := exec.Command(ffprobe, sidecarArguments()...)
		command.Stdin = file
		var stdout bytes.Buffer
		command.Stdout = &stdout
		if err := command.Run(); err != nil {
			return nil, err
		}
		var document struct {
			Streams []stream `json:"streams"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
			t.Fatal(name, err)
		}
		return document.Streams, nil
	}
	probed := 0
	for _, tc := range cases {
		if !generate(tc.name, tc.args...) {
			continue
		}
		streams, err := run(tc.name)
		if err != nil || len(streams) == 0 || streams[0].CodecType != "audio" || streams[0].CodecName != tc.codec {
			t.Errorf("%s: %v %+v", tc.name, err, streams)
			continue
		}
		probed++
	}
	if probed < 8 {
		t.Fatalf("only %d sidecar audio formats could be generated and probed", probed)
	}

	// HDMV PGS ".sup" and VobSub ".sub" data, written by the synthetic
	// bitmap subtitle encoders.
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	var sup bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&sup, 64, 32, []bitmapsubtest.Cue{{Start: time.Second, End: 2 * time.Second, Image: img}}); err != nil {
		t.Fatal(err)
	}
	spu, err := bitmapsubtest.SPU{Pixels: []uint8{1, 2, 3, 0}, Width: 2, Height: 2, Palette: [4]uint8{0, 1, 2, 3}, Alpha: [4]uint8{0, 15, 15, 15}, Stop: time.Second}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(spu, uint16(len(spu)))
	var sub bytes.Buffer
	if _, err := bitmapsubtest.WritePS(&sub, time.Second, 0, spu); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"a.sup": sup.Bytes(), "a.sub": sub.Bytes(),
		"m.sub": []byte("{1}{1}23.976\r\n{0}{48}Hello there.|Second line\r\n{50}{96}Bye.\r\n"),
		"a.srt": []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello.\r\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, codec := range map[string]string{"a.sup": "hdmv_pgs_subtitle", "a.sub": "dvd_subtitle", "m.sub": "microdvd"} {
		streams, err := run(name)
		if err != nil || len(streams) == 0 || streams[0].CodecType != "subtitle" || streams[0].CodecName != codec {
			t.Errorf("%s: %v %+v", name, err, streams)
		}
	}
	// SubRip is not on the sidecar whitelist: its extension already names it.
	if streams, err := run("a.srt"); err == nil {
		t.Fatalf("demuxer outside the whitelist opened: %+v", streams)
	}
}
