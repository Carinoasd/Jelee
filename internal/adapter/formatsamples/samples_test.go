package formatsamples

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Every audio extension the scan recognises has a sample or a recorded
// reason why it has none.
var sidecarAudioExtensions = []string{"mka", "aac", "m4a", "ac3", "eac3", "ec3", "dts", "dtshd", "thd", "truehd", "mlp", "flac", "alac", "opus", "ogg", "oga", "mp3", "wav"}

var sidecarSubtitleExtensions = []string{"srt", "ass", "ssa", "vtt", "webvtt", "ttml", "dfxp", "smi", "sami", "sub", "idx", "sup"}

func TestSampleTablesCoverEveryRecognisedFormat(t *testing.T) {
	audio := map[string]bool{}
	files := map[string]bool{}
	for _, spec := range AudioSpecs() {
		audio[spec.Format] = true
		if files[spec.File] || !strings.HasSuffix(spec.File, "."+spec.Format) {
			t.Errorf("audio sample %s: duplicate name or extension differs from %s", spec.File, spec.Format)
		}
		files[spec.File] = true
		if spec.Codec == "" || spec.Channels < 1 || (!spec.Handmade && len(spec.Args) == 0) {
			t.Errorf("audio sample %s is incomplete", spec.File)
		}
		if _, ok := domain.ParseSidecarName("v", "v."+spec.File, ""); !ok {
			t.Errorf("audio sample %s is not recognised by name", spec.File)
		}
	}
	for _, format := range sidecarAudioExtensions {
		_, missing := MissingAudio[format]
		if audio[format] == missing {
			t.Errorf("audio format %s must have exactly one of a sample or a missing reason", format)
		}
	}
	subtitles := map[string]bool{}
	names := map[string]bool{}
	for _, s := range Subtitles() {
		subtitles[s.Format] = true
		name := strings.ToLower(s.Name("v"))
		if names[name] {
			t.Errorf("duplicate subtitle sample name %s", name)
		}
		names[name] = true
		if len(s.Data) == 0 || len(s.Data) > 4096 {
			t.Errorf("%s: %d bytes", s.File, len(s.Data))
		}
		if s.Charset != "" && !slices.Contains(domain.SidecarCharsets, s.Charset) {
			t.Errorf("%s: charset %s is not recordable", s.File, s.Charset)
		}
		if (s.ProbeCodec != "") != domain.SidecarProbeable(domain.SidecarKindSubtitle, s.Format) {
			t.Errorf("%s: probe codec %q does not match the format's probe rule", s.File, s.ProbeCodec)
		}
		if _, ok := domain.ParseSidecarName("v", s.Name("v"), ""); !ok {
			t.Errorf("%s is not recognised by name", s.File)
		}
	}
	for _, format := range sidecarSubtitleExtensions {
		if !subtitles[format] {
			t.Errorf("subtitle format %s has no sample", format)
		}
	}
}

func TestMp3IsAWholeNumberOfFrames(t *testing.T) {
	data := Mp3()
	if len(data)%104 != 0 || len(data) < 2000 {
		t.Fatalf("length %d", len(data))
	}
	for offset := 0; offset < len(data); offset += 104 {
		if !bytes.Equal(data[offset:offset+4], []byte{0xFF, 0xFB, 0x10, 0xC0}) {
			t.Fatalf("frame at %d starts with % x", offset, data[offset:offset+4])
		}
	}
}

func TestEncodeAndProbeFailuresAreExplicit(t *testing.T) {
	dir := t.TempDir()
	var encoded AudioSpec
	for _, spec := range AudioSpecs() {
		if !spec.Handmade {
			encoded = spec
			break
		}
	}
	if _, err := Encode(filepath.Join(dir, "no-such-ffmpeg"), dir, encoded); !errors.Is(err, ErrNoEncoder) {
		t.Fatalf("missing tool: %v", err)
	}
	made, skipped, err := EncodeAll("", dir)
	if err != nil || len(made) != 1 || !made[0].Spec.Handmade || len(skipped) != len(AudioSpecs())-1 {
		t.Fatalf("without a tool only the hand-made sample is written: %v %d %d", err, len(made), len(skipped))
	}
	if data, err := os.ReadFile(made[0].Path); err != nil || !bytes.Equal(data, Mp3()) {
		t.Fatal("hand-made sample not written as is")
	}
	if _, err := ProbeJSON("true", filepath.Join(dir, "missing")); err == nil {
		t.Fatal("probing a missing file succeeded")
	}
	for _, spec := range AudioSpecs() {
		if got := spec.Experimental(); got != strings.Contains(strings.Join(spec.Args, " "), "-strict -2") {
			t.Errorf("%s: Experimental()=%v", spec.File, got)
		}
		if len(spec.Input()) != 4 {
			t.Errorf("%s: input %v", spec.File, spec.Input())
		}
	}
}
