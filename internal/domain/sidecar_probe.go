package domain

import (
	"bytes"
	"regexp"
)

// Probe states of an external track (G15.1, G16.1, G16.3). Text subtitles
// whose extension already names their format are not probed; audio files,
// ".sub" and ".sup" wait as pending for the sandboxed ffprobe pass, which
// records ready or failed for exactly the stamp it read.
const (
	SidecarProbePending       = "pending"
	SidecarProbeReady         = "ready"
	SidecarProbeFailed        = "failed"
	SidecarProbeNotApplicable = "not_applicable"
)

// Content kinds of a ".sub" file. The extension names two unrelated
// formats; only the content tells them apart.
const (
	SidecarSubtitleMicroDVD = "microdvd"
	SidecarSubtitleVobSub   = "vobsub"
)

const (
	// SidecarProbeBatch bounds one page of pending tracks.
	SidecarProbeBatch = 16
	// SidecarProbeMaxPerJob caps the probes one catalog sync starts, so a
	// library full of new audio files is worked through over several syncs
	// instead of one long job.
	SidecarProbeMaxPerJob = 512
	// SidecarSniffBytes is how much of a ".sub" the content check reads.
	SidecarSniffBytes = 4096
)

// SidecarProbeFacts is what the probe found in one external file: the codec
// of its first audio (or, for a subtitle, first subtitle) stream and, for
// audio, the stream layout. Zero values are unknown.
type SidecarProbeFacts struct {
	Codec          string `json:"codec,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Channels       int    `json:"channels,omitempty"`
	ChannelLayout  string `json:"channelLayout,omitempty"`
	SampleRate     int    `json:"sampleRate,omitempty"`
	BitRate        int64  `json:"bitRate,omitempty"`
	DurationMicros int64  `json:"durationMicros,omitempty"`
}

// SidecarProbeRecord is the stored probe state of a track.
type SidecarProbeRecord struct {
	State string
	Facts SidecarProbeFacts
}

// SidecarProbeResult is one probe outcome to store. State is ready or
// failed; Size and ModifiedUnixNano are the stamp that was read, and a row
// whose stamp changed since is left pending for the next pass.
type SidecarProbeResult struct {
	ID                     string
	Size, ModifiedUnixNano int64
	State                  string
	Facts                  SidecarProbeFacts
}

var (
	sidecarCodecName     = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	sidecarChannelLayout = regexp.MustCompile(`^[a-z0-9.()_-]{1,32}$`)
)

// SidecarProbeable reports whether a track of this kind and format is
// probed: every audio file, and the subtitle formats whose extension does
// not name one codec (".sub") or whose timing only the stream holds (".sup").
func SidecarProbeable(kind, format string) bool {
	return kind == SidecarKindAudio || kind == SidecarKindSubtitle && (format == "sub" || format == "sup")
}

// InitialSidecarProbeState is the state a newly observed or changed file
// starts in.
func InitialSidecarProbeState(kind, format string) string {
	if SidecarProbeable(kind, format) {
		return SidecarProbePending
	}
	return SidecarProbeNotApplicable
}

// ValidSidecarProbeResult mirrors the database checks for a stored result.
// The audio-only facts are accepted here and dropped by the store for a
// subtitle row.
func ValidSidecarProbeResult(v SidecarProbeResult) bool {
	if !ValidID(v.ID) || v.Size < 0 {
		return false
	}
	switch v.State {
	case SidecarProbeFailed:
		return v.Facts == SidecarProbeFacts{}
	case SidecarProbeReady:
		return validSidecarProbeFacts(SidecarKindAudio, v.Facts)
	}
	return false
}

func validSidecarProbeFacts(kind string, f SidecarProbeFacts) bool {
	if f.Codec != "" && !sidecarCodecName.MatchString(f.Codec) || f.BitRate < 0 || f.DurationMicros < 0 {
		return false
	}
	if kind != SidecarKindAudio {
		return f.Profile == "" && f.Channels == 0 && f.ChannelLayout == "" && f.SampleRate == 0
	}
	if len(f.Profile) > 64 || f.Profile != "" && !validSidecarTitle(f.Profile) {
		return false
	}
	return f.Channels >= 0 && f.Channels <= 256 && f.SampleRate >= 0 && f.SampleRate <= 768000 &&
		(f.ChannelLayout == "" || sidecarChannelLayout.MatchString(f.ChannelLayout))
}

// SidecarProbeFactsFrom reduces a probe of one external file to its facts.
// An audio file is described by its default audio stream, else its first
// one; a subtitle file by its first subtitle stream. A file without such a
// stream yields false: the probe read it but found nothing to describe.
// Values the database cannot hold are dropped, not repaired.
func SidecarProbeFactsFrom(kind string, meta MediaMetadata) (SidecarProbeFacts, bool) {
	want := "audio"
	if kind == SidecarKindSubtitle {
		want = "subtitle"
	}
	var chosen *MediaStream
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		if stream.Kind != want {
			continue
		}
		if chosen == nil || sidecarProbeFlag(stream.Default) && !sidecarProbeFlag(chosen.Default) {
			chosen = stream
		}
		if kind == SidecarKindSubtitle {
			break
		}
	}
	if chosen == nil {
		return SidecarProbeFacts{}, false
	}
	var f SidecarProbeFacts
	if c := sidecarProbeText(chosen.Codec); sidecarCodecName.MatchString(c) {
		f.Codec = c
	}
	duration := chosen.DurationMicros
	if duration == nil {
		duration = meta.Format.DurationMicros
	}
	if duration != nil && *duration >= 0 {
		f.DurationMicros = *duration
	}
	if kind == SidecarKindSubtitle {
		return f, true
	}
	if p := sidecarProbeText(chosen.Profile); len(p) <= 64 && validSidecarTitle(p) {
		f.Profile = p
	}
	bitRate := chosen.BitRate
	if bitRate == nil {
		bitRate = meta.Format.BitRate
	}
	if bitRate != nil && *bitRate > 0 {
		f.BitRate = *bitRate
	}
	if a := chosen.Audio; a != nil {
		if a.Channels != nil && *a.Channels >= 1 && *a.Channels <= 256 {
			f.Channels = int(*a.Channels)
		}
		if a.SampleRate != nil && *a.SampleRate >= 1 && *a.SampleRate <= 768000 {
			f.SampleRate = int(*a.SampleRate)
		}
		if l := sidecarProbeText(a.ChannelLayout); sidecarChannelLayout.MatchString(l) {
			f.ChannelLayout = l
		}
	}
	return f, true
}

func sidecarProbeFlag(v *bool) bool { return v != nil && *v }

func sidecarProbeText(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// SidecarAtmos applies the embedded track rule to probed external audio:
// Dolby Atmos is the E-AC-3 or TrueHD profile that says so.
func SidecarAtmos(codec, profile string) bool {
	return codec == "eac3" && profile == "Dolby Digital Plus + Dolby Atmos" || codec == "truehd" && profile == "Dolby TrueHD + Dolby Atmos"
}

var microDVDLine = regexp.MustCompile(`^\{[0-9]{1,10}\}\{[0-9]{0,10}\}`)

// SniffSubtitleSub tells the two formats sharing the ".sub" extension apart
// from the start of the file: VobSub data is an MPEG program stream and
// begins with a pack header (00 00 01 BA); MicroDVD is text whose first
// non-empty line starts with "{start}{end}" frame numbers. A UTF-8 or
// UTF-16 byte order mark is skipped. Anything else yields "".
func SniffSubtitleSub(head []byte) string {
	if len(head) > SidecarSniffBytes {
		head = head[:SidecarSniffBytes]
	}
	if bytes.HasPrefix(head, []byte{0, 0, 1, 0xBA}) {
		return SidecarSubtitleVobSub
	}
	switch {
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		head = head[3:]
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		head = narrowUTF16(head[2:], 0)
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		head = narrowUTF16(head[2:], 1)
	}
	for len(head) > 0 {
		line := head
		if end := bytes.IndexByte(head, '\n'); end >= 0 {
			line, head = head[:end], head[end+1:]
		} else {
			head = nil
		}
		line = bytes.TrimLeft(bytes.TrimRight(line, "\r \t"), " \t")
		if len(line) == 0 {
			continue
		}
		if microDVDLine.Match(line) {
			return SidecarSubtitleMicroDVD
		}
		return ""
	}
	return ""
}

// narrowUTF16 keeps the ASCII units of UTF-16 text (low byte at offset
// low of each pair) and turns every other unit into 0xFF, which no
// MicroDVD frame marker contains.
func narrowUTF16(data []byte, low int) []byte {
	out := make([]byte, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		lo, hi := data[i+low], data[i+1-low]
		if hi == 0 && lo < 0x80 {
			out = append(out, lo)
		} else {
			out = append(out, 0xFF)
		}
	}
	return out
}
