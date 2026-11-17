package sandbox

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestCoverDescriptorsAreSealedPerStream(t *testing.T) {
	l := &Launcher{executable: "trusted", descriptor: "fixed", ffprobePath: "/usr/lib/jelee/ffprobe"}
	for stream := 0; stream <= CoverMaxVideoIndex; stream++ {
		args := l.CoverHelperArguments(stream)
		if len(args) != 2 || args[0] != HelperCommand {
			t.Fatal("cover helper framing", stream)
		}
		request, err := decodeDescriptor(args[1:])
		if err != nil || request.profile.FFprobePath != "/usr/lib/jelee/ffprobe" || !slices.Equal(request.arguments, coverArguments(stream)) {
			t.Fatal("cover descriptor did not round trip", stream, err)
		}
	}
	// The production helper compares this exact canonical text.
	raw, err := base64.RawURLEncoding.DecodeString(l.CoverHelperArguments(15)[1])
	if err != nil || string(raw) != `{"version":1,"mode":"cover","ffprobePath":"/usr/lib/jelee/ffprobe","stream":15}` {
		t.Fatal("cover descriptor text changed", string(raw))
	}
	for _, stream := range []int{-1, CoverMaxVideoIndex + 1} {
		if l.CoverHelperArguments(stream) != nil {
			t.Fatal("out-of-range cover stream sealed", stream)
		}
	}
	if (*Launcher)(nil).CoverHelperArguments(0) != nil {
		t.Fatal("nil launcher sealed a cover read")
	}
	metadata, err := decodeDescriptor([]string{base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe"}`))})
	if err != nil || !slices.Equal(metadata.arguments, metadataArguments()) {
		t.Fatal("metadata descriptor changed", err)
	}
	for name, data := range map[string]string{
		"metadata_stream": `{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe","stream":0}`,
		"cover_no_stream": `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffprobe"}`,
		"cover_negative":  `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffprobe","stream":-1}`,
		"cover_range":     `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffprobe","stream":16}`,
		"cover_text":      `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffprobe","stream":"0"}`,
		"cover_ffmpeg":    `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffmpeg","stream":0}`,
		"cover_spacing":   `{"version":1,"mode":"cover","ffprobePath":"/tmp/ffprobe","stream": 0}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDescriptor([]string{base64.RawURLEncoding.EncodeToString([]byte(data))}); err != ErrInvalid {
				t.Fatal("unsafe cover descriptor accepted")
			}
		})
	}
}

// The cover read copies one packet: it must keep every input restriction of
// the metadata probe and never name an encoder, output file or filter.
func TestCoverArgumentsCopyOnly(t *testing.T) {
	cover := coverArguments(3)
	metadata := metadataArguments()
	if !slices.Equal(cover[:slices.Index(cover, "-select_streams")], metadata[:slices.Index(metadata, "-show_format")]) {
		t.Fatal("cover read lost a metadata input restriction")
	}
	joined := strings.Join(cover, " ")
	for _, want := range []string{"-select_streams v:3", "-read_intervals %+#1", "-show_data", "-show_data_hash SHA256", "stream_disposition=attached_pic", "-i fd:"} {
		if !strings.Contains(joined, want) {
			t.Fatal("cover read lacks", want)
		}
	}
	for _, forbidden := range []string{"-c", "-codec", "-vf", "-filter", "-f", "-y", "-map", "-o"} {
		if slices.Contains(cover, forbidden) {
			t.Fatal("cover read can transcode or write", forbidden)
		}
	}
	cover[0] = "mutated"
	if coverArguments(3)[0] != "-hide_banner" {
		t.Fatal("cover arguments shared caller memory")
	}
}

func TestChapterDescriptorsAreSealedPerStream(t *testing.T) {
	l := &Launcher{executable: "trusted", descriptor: "fixed", ffprobePath: "/usr/lib/jelee/ffprobe"}
	for stream := 0; stream <= CoverMaxVideoIndex; stream++ {
		args := l.ChapterHelperArguments(stream)
		if len(args) != 2 || args[0] != HelperCommand {
			t.Fatal("chapter helper framing", stream)
		}
		request, err := decodeDescriptor(args[1:])
		if err != nil || request.profile.FFprobePath != "/usr/lib/jelee/ffprobe" || !slices.Equal(request.arguments, chapterArguments(stream)) {
			t.Fatal("chapter descriptor did not round trip", stream, err)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(l.ChapterHelperArguments(15)[1])
	if err != nil || string(raw) != `{"version":1,"mode":"chapters","ffprobePath":"/usr/lib/jelee/ffprobe","stream":15}` {
		t.Fatal("chapter descriptor text changed", string(raw))
	}
	for _, stream := range []int{-1, CoverMaxVideoIndex + 1} {
		if l.ChapterHelperArguments(stream) != nil {
			t.Fatal("out-of-range chapter stream sealed", stream)
		}
	}
	if (*Launcher)(nil).ChapterHelperArguments(0) != nil || (&Launcher{}).ChapterHelperArguments(0) != nil {
		t.Fatal("unverified launcher sealed a chapter read")
	}
	for name, data := range map[string]string{
		"no_stream": `{"version":1,"mode":"chapters","ffprobePath":"/tmp/ffprobe"}`,
		"negative":  `{"version":1,"mode":"chapters","ffprobePath":"/tmp/ffprobe","stream":-1}`,
		"range":     `{"version":1,"mode":"chapters","ffprobePath":"/tmp/ffprobe","stream":16}`,
		"ffmpeg":    `{"version":1,"mode":"chapters","ffprobePath":"/tmp/ffmpeg","stream":0}`,
		"frames":    `{"version":1,"mode":"frames","ffprobePath":"/tmp/ffprobe","stream":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDescriptor([]string{base64.RawURLEncoding.EncodeToString([]byte(data))}); err != ErrInvalid {
				t.Fatal("unsafe chapter descriptor accepted")
			}
		})
	}
}

// The chapter read copies stored packets: it keeps every input restriction
// of the metadata probe, is bounded in packets, and never names an encoder,
// decoder output, filter or output file (ADR 0007).
func TestChapterArgumentsCopyOnly(t *testing.T) {
	chapters := chapterArguments(2)
	metadata := metadataArguments()
	if !slices.Equal(chapters[:slices.Index(chapters, "-select_streams")], metadata[:slices.Index(metadata, "-show_format")]) {
		t.Fatal("chapter read lost a metadata input restriction")
	}
	joined := strings.Join(chapters, " ")
	for _, want := range []string{"-select_streams v:2", "-read_intervals %+#65", "-show_data", "-show_data_hash SHA256", "timed_thumbnails", "chapter=start_time,end_time", "-i fd:"} {
		if !strings.Contains(joined, want) {
			t.Fatal("chapter read lacks", want)
		}
	}
	for _, forbidden := range []string{"-c", "-codec", "-vf", "-filter", "-f", "-y", "-map", "-o", "-show_frames", "-count_frames"} {
		if slices.Contains(chapters, forbidden) {
			t.Fatal("chapter read can decode, transcode or write", forbidden)
		}
	}
	chapters[0] = "mutated"
	if chapterArguments(2)[0] != "-hide_banner" {
		t.Fatal("chapter arguments shared caller memory")
	}
}
