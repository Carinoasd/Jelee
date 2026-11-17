package formatsamples

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AudioSpec is how one external audio sample is made: the ffmpeg output
// arguments for a quarter-second 440 Hz tone, and what a probe must report.
type AudioSpec struct {
	// File is the sample's file name; its extension is the sidecar format.
	File string
	// Format is the sidecar extension (lower case).
	Format string
	// Codec is the ffprobe codec name of the audio stream.
	Codec string
	// Channels is the expected channel count.
	Channels int
	// Args are the ffmpeg output arguments (codec, muxer) without the file.
	Args []string
	// Handmade marks a sample written byte by byte here, not by ffmpeg.
	Handmade bool
	// Seconds overrides the tone length for streams the demuxer can only
	// recognise from several access units (TrueHD, MLP).
	Seconds string
	// NoDuration marks a raw bitstream whose ffprobe reports no duration
	// (TrueHD and MLP carry no length in the stream).
	NoDuration bool
	// Container notes a container that may carry other codecs (mka, m4a,
	// ogg, oga): the extension alone does not name the codec.
	Container bool
}

// Input returns the ffmpeg input arguments of the spec's tone: a 440 Hz sine
// at 48 kHz, a quarter second unless Seconds says otherwise.
func (s AudioSpec) Input() []string {
	seconds := s.Seconds
	if seconds == "" {
		seconds = "0.25"
	}
	return []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=" + seconds + ":sample_rate=48000"}
}

// AudioSpecs lists the audio samples ffmpeg can make with its own encoders
// plus the hand-made MP3. Formats that cannot be produced are in
// MissingAudio. The slice is fresh.
func AudioSpecs() []AudioSpec {
	return []AudioSpec{
		{File: "tone.aac", Format: "aac", Codec: "aac", Channels: 1, Args: []string{"-c:a", "aac", "-f", "adts"}},
		{File: "tone-aac.m4a", Format: "m4a", Codec: "aac", Channels: 1, Args: []string{"-c:a", "aac"}, Container: true},
		{File: "tone-alac.m4a", Format: "m4a", Codec: "alac", Channels: 1, Args: []string{"-c:a", "alac"}, Container: true},
		{File: "tone.ac3", Format: "ac3", Codec: "ac3", Channels: 6, Args: []string{"-ac", "6", "-c:a", "ac3", "-b:a", "192k"}},
		{File: "tone.eac3", Format: "eac3", Codec: "eac3", Channels: 6, Args: []string{"-ac", "6", "-c:a", "eac3", "-b:a", "192k"}},
		{File: "tone.ec3", Format: "ec3", Codec: "eac3", Channels: 6, Args: []string{"-ac", "6", "-c:a", "eac3", "-b:a", "192k", "-f", "eac3"}},
		{File: "tone.dts", Format: "dts", Codec: "dts", Channels: 2, Args: []string{"-ac", "2", "-c:a", "dca", "-strict", "-2"}},
		{File: "tone.thd", Format: "thd", Codec: "truehd", Channels: 2, Args: []string{"-ac", "2", "-c:a", "truehd", "-strict", "-2", "-f", "truehd"}, Seconds: "1", NoDuration: true},
		{File: "tone.truehd", Format: "truehd", Codec: "truehd", Channels: 2, Args: []string{"-ac", "2", "-c:a", "truehd", "-strict", "-2", "-f", "truehd"}, Seconds: "1", NoDuration: true},
		{File: "tone.mlp", Format: "mlp", Codec: "mlp", Channels: 2, Args: []string{"-ac", "2", "-c:a", "mlp", "-strict", "-2", "-f", "mlp"}, Seconds: "1", NoDuration: true},
		{File: "tone.flac", Format: "flac", Codec: "flac", Channels: 1, Args: []string{"-c:a", "flac"}},
		{File: "tone.opus", Format: "opus", Codec: "opus", Channels: 2, Args: []string{"-ac", "2", "-c:a", "opus", "-strict", "-2", "-f", "ogg"}},
		{File: "tone.ogg", Format: "ogg", Codec: "vorbis", Channels: 2, Args: []string{"-ac", "2", "-c:a", "vorbis", "-strict", "-2", "-f", "ogg"}, Container: true},
		{File: "tone.oga", Format: "oga", Codec: "vorbis", Channels: 2, Args: []string{"-ac", "2", "-c:a", "vorbis", "-strict", "-2", "-f", "ogg"}, Container: true},
		{File: "tone.wav", Format: "wav", Codec: "pcm_s16le", Channels: 1, Args: []string{"-c:a", "pcm_s16le"}},
		{File: "tone-ac3.mka", Format: "mka", Codec: "ac3", Channels: 6, Args: []string{"-ac", "6", "-c:a", "ac3", "-b:a", "192k", "-f", "matroska"}, Container: true},
		{File: "tone-flac.mka", Format: "mka", Codec: "flac", Channels: 1, Args: []string{"-c:a", "flac", "-f", "matroska"}, Container: true},
		{File: "handmade.mp3", Format: "mp3", Codec: "mp3", Channels: 1, Handmade: true},
	}
}

// MissingAudio lists the sidecar audio formats no sample can be made for in
// this repository, with the reason. Their naming, MIME type and delivery are
// covered by the table-driven tests that need no content; their probe
// result is not verified here and stays on the owner list (D42).
var MissingAudio = map[string]string{
	"dtshd": "no ffmpeg encoder writes DTS-HD (Master Audio) and no hand-made stream is attempted; a core-only DTS file would only repeat the dts sample",
	"alac":  "ALAC has no raw bitstream container, so a standalone .alac file does not exist; ALAC is covered inside m4a (tone-alac.m4a)",
}

// Mp3 returns a hand-made MPEG-1 Layer III stream: 40 silent mono frames
// at 32 kbit/s and 44.1 kHz (about one second). Each frame is a valid
// header, empty side information and zero main data, which decoders play
// as silence. ffmpeg builds without an MP3 encoder (libmp3lame) can still
// probe it.
func Mp3() []byte {
	const frames, size = 40, 104
	header := []byte{0xFF, 0xFB, 0x10, 0xC0} // MPEG-1, Layer III, no CRC, 32 kbit/s, 44.1 kHz, mono
	var out bytes.Buffer
	for range frames {
		frame := make([]byte, size)
		copy(frame, header)
		out.Write(frame)
	}
	return out.Bytes()
}

// ErrNoEncoder is returned by Encode when the ffmpeg in use lacks the
// encoder or muxer a spec needs.
var ErrNoEncoder = errors.New("formatsamples: ffmpeg cannot encode this sample")

// Encode writes spec's sample into dir with the given ffmpeg and returns its
// path. A hand-made sample needs no tool. ErrNoEncoder wraps the ffmpeg
// message when the build cannot make the sample.
func Encode(ffmpeg, dir string, spec AudioSpec) (string, error) {
	path := filepath.Join(dir, spec.File)
	if spec.Handmade {
		return path, os.WriteFile(path, Mp3(), 0o600)
	}
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, spec.Input()...)
	args = append(append(args, spec.Args...), path)
	if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil { //nolint:gosec // G204: test-support tool path chosen by the test, fixed argument table
		return "", errors.Join(ErrNoEncoder, errors.New(string(bytes.TrimSpace(out))))
	}
	return path, nil
}

// AudioFile is one audio sample written to disk.
type AudioFile struct {
	Spec AudioSpec
	Path string
	Data []byte
}

// EncodeAll writes every audio sample into dir. ffmpeg may be empty: only
// the hand-made sample is written then. A sample the tool cannot make is
// returned in skipped with the reason, and is never substituted.
func EncodeAll(ffmpeg, dir string) (made []AudioFile, skipped map[string]string, err error) {
	skipped = make(map[string]string)
	for _, spec := range AudioSpecs() {
		if !spec.Handmade && ffmpeg == "" {
			skipped[spec.File] = "ffmpeg not available"
			continue
		}
		path, encodeErr := Encode(ffmpeg, dir, spec)
		if errors.Is(encodeErr, ErrNoEncoder) {
			skipped[spec.File] = "the local ffmpeg cannot encode it: " + strings.Join(strings.Fields(encodeErr.Error()), " ")
			continue
		}
		if encodeErr != nil {
			return nil, nil, encodeErr
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G304: a file this function just wrote
		if readErr != nil {
			return nil, nil, readErr
		}
		made = append(made, AudioFile{Spec: spec, Path: path, Data: data})
	}
	return made, skipped, nil
}

// Experimental reports whether the encoder is marked experimental in ffmpeg
// (the spec needs "-strict -2"). A build may leave those out.
func (s AudioSpec) Experimental() bool {
	for i, arg := range s.Args {
		if arg == "-strict" && i+1 < len(s.Args) && s.Args[i+1] == "-2" {
			return true
		}
	}
	return false
}
