package domain

import (
	"math/big"
	"slices"
	"strings"
)

// Direct play reason codes (G10.5). They only explain why a client cannot
// play an original resource as it is; none of them names a conversion,
// because the server never offers one.
const (
	PlaybackReasonContainer      = "container_unsupported"
	PlaybackReasonVideoCodec     = "video_codec_unsupported"
	PlaybackReasonAudioCodec     = "audio_codec_unsupported"
	PlaybackReasonBitrate        = "bitrate_exceeds_client"
	PlaybackReasonSourceNotProbe = "source_not_probed"
	// Track level codes. A subtitle or an external audio file the client
	// cannot read never blocks the source itself; it is reported per track.
	PlaybackReasonSubtitleFormat = "subtitle_format_unsupported"
	PlaybackReasonTrackNotProbed = "track_not_probed"

	// PlaybackUnsupportedCode marks a source decision that is not direct
	// playable. It is a decision code inside a successful check, not an
	// HTTP error.
	PlaybackUnsupportedCode = "direct_play_unsupported"

	// ClientCapabilityListMax bounds every declared list; ClientCapabilityTokenMax
	// bounds every token in it.
	ClientCapabilityListMax  = 32
	ClientCapabilityTokenMax = 32
	// ClientCapabilityBitrateMax is one terabit per second, far above any
	// real decoder, so a declared ceiling never overflows arithmetic.
	ClientCapabilityBitrateMax = 1_000_000_000_000
)

// ClientCapabilities is what a client declares it can decode. Every field
// is a statement about the client, never a request to change the resource:
// MaxBitrate is the highest average bit rate the client can sustain, used
// only to report bitrate_exceeds_client. Field names deliberately differ from
// the upstream transformation parameters (videoCodec, maxStreamingBitrate,
// ...), which the production guard keeps rejecting.
type ClientCapabilities struct {
	Containers      []string `json:"containers"`
	VideoCodecs     []string `json:"videoCodecs"`
	AudioCodecs     []string `json:"audioCodecs"`
	SubtitleFormats []string `json:"subtitleFormats"`
	// MaxBitrate is in bits per second; zero declares no ceiling.
	MaxBitrate int64 `json:"maxBitrate"`
}

// PlaybackSourceRecord is what storage returns for one authorized media
// source. FileName is only the last path element; no root or directory
// reaches this layer. Metadata is nil when no current probe result exists.
type PlaybackSourceRecord struct {
	ID          string
	ContentType string
	FileName    string
	Metadata    *MediaMetadata
	// ScanSize is the size the catalog scan observed, if any.
	ScanSize *int64
	Sidecars []SidecarTrackRecord
	// Primary marks the administrator's main version of the item (G20.3).
	Primary bool
}

func (PlaybackSourceRecord) String() string   { return "playback source (redacted)" }
func (PlaybackSourceRecord) GoString() string { return "playback source (redacted)" }

// PlaybackSource is the client facing description of one original resource
// (G10.4, G15.2, G16.3). Probed is false when no current probe result
// exists; the stream lists are then empty and only the container, the
// file-name version labels, the scan size and external tracks are known.
type PlaybackSource struct {
	ID             string                  `json:"id"`
	Container      string                  `json:"container"`
	ContentType    string                  `json:"contentType"`
	Probed         bool                    `json:"probed"`
	SizeBytes      *int64                  `json:"sizeBytes,omitempty"`
	DurationMicros *int64                  `json:"durationMicros,omitempty"`
	BitRate        *int64                  `json:"bitRate,omitempty"`
	Version        VersionLabels           `json:"version"`
	Video          []PlaybackVideoTrack    `json:"videoTracks"`
	Audio          []PlaybackAudioTrack    `json:"audioTracks"`
	Subtitles      []PlaybackSubtitleTrack `json:"subtitleTracks"`
	External       []PlaybackExternalTrack `json:"externalTracks"`
	// Attachments are the Matroska attachments the MediaInfo supplement
	// listed (G15.7); empty without the supplement.
	Attachments []PlaybackAttachment `json:"attachments,omitempty"`
	// Primary marks the administrator's main version; it is listed first.
	Primary bool `json:"primary"`
	// DefaultTracks is the audio and subtitle the user's preferences pick
	// (G16.5, G20.4); only playback information fills it.
	DefaultTracks *DefaultTracks `json:"defaultTracks,omitempty"`
}

type PlaybackVideoTrack struct {
	Index     int            `json:"index"`
	Codec     string         `json:"codec,omitempty"`
	Profile   string         `json:"profile,omitempty"`
	Level     *int64         `json:"level,omitempty"`
	Width     *int64         `json:"width,omitempty"`
	Height    *int64         `json:"height,omitempty"`
	FrameRate *MediaRational `json:"frameRate,omitempty"`
	BitRate   *int64         `json:"bitRate,omitempty"`
	Default   bool           `json:"default"`
	// Primary marks the stream the decision checks: the default, else the
	// largest, else the first real video stream. Cover art is never listed.
	Primary bool `json:"primary"`
}

type PlaybackAudioTrack struct {
	Index         int    `json:"index"`
	Codec         string `json:"codec,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Language      string `json:"language,omitempty"`
	Channels      *int64 `json:"channels,omitempty"`
	ChannelLayout string `json:"channelLayout,omitempty"`
	SampleRate    *int64 `json:"sampleRate,omitempty"`
	BitRate       *int64 `json:"bitRate,omitempty"`
	Default       bool   `json:"default"`
	Forced        bool   `json:"forced"`
	Atmos         bool   `json:"atmos"`
}

type PlaybackSubtitleTrack struct {
	Index int `json:"index"`
	// Codec is the probed codec name; Format is its canonical subtitle format
	// as clients declare it (srt, ass, webvtt, pgs, ...).
	Codec    string `json:"codec,omitempty"`
	Format   string `json:"format,omitempty"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default"`
	Forced   bool   `json:"forced"`
	// Title is the Matroska track name from the MediaInfo supplement.
	Title string `json:"title,omitempty"`
	// Extractable marks a text subtitle inside a Matroska/WebM source that
	// mkvextract can copy to the rebuildable cache unconverted (G15.5).
	Extractable bool `json:"extractable"`
	// URL is set by the API layer when extraction is available.
	URL string `json:"url,omitempty"`
	// OCRSource marks a PGS or VobSub track inside a Matroska/WebM source
	// whose text subtitle OCR can derive (G15.6). The track itself is still
	// only delivered as it is.
	OCRSource bool `json:"-"`
	// OCR is the derived SRT track, set by the API layer once it exists.
	OCR *PlaybackOCRTrack `json:"ocr,omitempty"`
}

// PlaybackOCRTrack is an additional SubRip track recognized from a bitmap
// subtitle by subtitle OCR (G15.6). It is derived data in a rebuildable
// cache, never a replacement of the original track.
type PlaybackOCRTrack struct {
	Format string `json:"format"`
	// Title names the source track and marks the text as OCR-derived.
	Title string `json:"title"`
	URL   string `json:"url"`
}

// OCRSubtitleCodecs are the probed bitmap codecs subtitle OCR reads.
var OCRSubtitleCodecs = map[string]bool{"hdmv_pgs_subtitle": true, "dvd_subtitle": true}

// OCRTrackTitle is the display title of a derived OCR track: the source
// track's title or language, followed by the OCR marker.
func OCRTrackTitle(track PlaybackSubtitleTrack) string {
	base := track.Title
	if base == "" {
		base = track.Language
	}
	if base == "" {
		return "OCR"
	}
	return base + " (OCR)"
}

// PlaybackAttachment is one Matroska attachment of a source. ID is the
// 1-based attachment ID; StreamIndex is the probe stream index of the same
// attachment when the probe listed exactly as many attachment streams.
type PlaybackAttachment struct {
	ID          int    `json:"id"`
	StreamIndex *int   `json:"streamIndex,omitempty"`
	FileName    string `json:"fileName"`
	Font        bool   `json:"font"`
	// URL is set by the API layer for fonts when extraction is available.
	URL string `json:"url,omitempty"`
}

// ExtractableSubtitleCodecs are the embedded text subtitle codecs that are
// extracted as they are (G15.5), keyed by probe codec, with the file
// extension the extracted text keeps.
var ExtractableSubtitleCodecs = map[string]string{"subrip": "srt", "ass": "ass", "ssa": "ssa", "webvtt": "vtt"}

// IsMatroskaMetadata reports whether the probe identified Matroska or WebM.
func IsMatroskaMetadata(meta MediaMetadata) bool {
	return slices.Contains(meta.Format.Names, "matroska") || slices.Contains(meta.Format.Names, "webm")
}

// PlaybackExternalTrack is a sidecar file delivered as it is. Format is the
// file extension; Codec is the canonical subtitle format or audio codec the
// extension determines, empty when only probing could tell (mka, m4a, ogg,
// oga and the MicroDVD/VobSub ".sub").
type PlaybackExternalTrack struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Format     string   `json:"format"`
	Codec      string   `json:"codec,omitempty"`
	Language   string   `json:"language,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	Title      string   `json:"title,omitempty"`
	Forced     bool     `json:"forced"`
	SDH        bool     `json:"sdh"`
	Default    bool     `json:"default"`
	Commentary bool     `json:"commentary"`
	Charset    string   `json:"charset,omitempty"`
	SizeBytes  int64    `json:"sizeBytes"`
	// URL is the direct delivery route of the original file. The HTTP
	// adapter fills it; this layer does not know routes.
	URL string `json:"url,omitempty"`
}

// PlaybackDecision is the direct play verdict for one source. Reasons is
// empty exactly when DirectPlay is true. Tracks reports every audio and
// subtitle track, embedded or external, the client could not read.
type PlaybackDecision struct {
	SourceID   string                  `json:"sourceId"`
	DirectPlay bool                    `json:"directPlay"`
	Code       string                  `json:"code,omitempty"`
	Reasons    []string                `json:"reasons"`
	Tracks     []PlaybackTrackDecision `json:"tracks"`
}

type PlaybackTrackDecision struct {
	Kind string `json:"kind"`
	// Index names an embedded stream; ID names an external track.
	Index     *int   `json:"index,omitempty"`
	ID        string `json:"id,omitempty"`
	External  bool   `json:"external"`
	Supported bool   `json:"supported"`
	// Code is PlaybackUnsupportedCode exactly when Supported is false: the
	// track cannot be direct played by this client and no conversion is
	// offered in its place.
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// URL is the direct delivery route of an external track's original
	// file, filled by the HTTP adapter.
	URL string `json:"url,omitempty"`
}

var playbackContainerTypes = map[string]string{
	"video/mp4": "mp4", "video/x-matroska": "mkv", "video/webm": "webm",
	"video/quicktime": "mov", "video/x-msvideo": "avi", "video/mp2t": "mpegts",
}

// Client tokens are lower-cased first; aliases map common spellings to the
// probe vocabulary so a client need not know ffprobe names.
var playbackContainerAliases = map[string]string{
	"matroska": "mkv", "m4v": "mp4", "ts": "mpegts", "m2ts": "mpegts", "mts": "mpegts", "quicktime": "mov",
}
var playbackVideoAliases = map[string]string{
	"avc": "h264", "avc1": "h264", "h.264": "h264", "x264": "h264",
	"h265": "hevc", "h.265": "hevc", "hvc1": "hevc", "hev1": "hevc", "x265": "hevc",
	"av01": "av1", "vp09": "vp9", "mpeg2": "mpeg2video", "vc-1": "vc1",
}
var playbackAudioAliases = map[string]string{
	"ac-3": "ac3", "e-ac-3": "eac3", "ec-3": "eac3", "ec3": "eac3", "dca": "dts", "mp4a": "aac",
}
var playbackSubtitleAliases = map[string]string{
	"subrip": "srt", "vtt": "webvtt", "dfxp": "ttml", "smi": "sami",
	"hdmv_pgs_subtitle": "pgs", "sup": "pgs", "dvd_subtitle": "vobsub", "idx": "vobsub",
	"dvb_subtitle": "dvb", "tx3g": "mov_text",
}

// Embedded subtitle codecs from the probe whitelist and their formats.
var playbackSubtitleCodecs = map[string]string{
	"subrip": "srt", "srt": "srt", "ass": "ass", "ssa": "ssa", "webvtt": "webvtt", "mov_text": "mov_text",
	"hdmv_pgs_subtitle": "pgs", "dvd_subtitle": "vobsub", "dvb_subtitle": "dvb", "eia_608": "eia_608", "text": "text",
}

// External subtitle extensions and their formats. ".sub" is absent: it is
// MicroDVD text or VobSub data and only probing can tell.
var playbackSidecarSubtitles = map[string]string{
	"srt": "srt", "ass": "ass", "ssa": "ssa", "vtt": "webvtt", "webvtt": "webvtt", "ttml": "ttml", "dfxp": "ttml",
	"smi": "sami", "sami": "sami", "idx": "vobsub", "sup": "pgs",
}

// External audio extensions with exactly one codec. mka, m4a, ogg and oga
// may hold several codecs and are absent. WAV carries PCM, declared as "pcm".
var playbackSidecarAudio = map[string]string{
	"aac": "aac", "ac3": "ac3", "eac3": "eac3", "ec3": "eac3", "dts": "dts", "dtshd": "dts",
	"thd": "truehd", "truehd": "truehd", "mlp": "mlp", "flac": "flac", "alac": "alac",
	"opus": "opus", "mp3": "mp3", "wav": "pcm",
}

// ContainerFromContentType maps a stored media content type to the
// container token clients declare. Unknown types yield "".
func ContainerFromContentType(contentType string) string { return playbackContainerTypes[contentType] }

// NormalizeClientCapabilities validates a declaration and maps aliases to
// canonical tokens. Lists are deduplicated; empty lists declare nothing.
func NormalizeClientCapabilities(c ClientCapabilities) (ClientCapabilities, error) {
	if c.MaxBitrate < 0 || c.MaxBitrate > ClientCapabilityBitrateMax {
		return ClientCapabilities{}, ErrInvalid
	}
	out := ClientCapabilities{MaxBitrate: c.MaxBitrate}
	var err error
	if out.Containers, err = normalizeCapabilityList(c.Containers, playbackContainerAliases); err != nil {
		return ClientCapabilities{}, err
	}
	if out.VideoCodecs, err = normalizeCapabilityList(c.VideoCodecs, playbackVideoAliases); err != nil {
		return ClientCapabilities{}, err
	}
	if out.AudioCodecs, err = normalizeCapabilityList(c.AudioCodecs, playbackAudioAliases); err != nil {
		return ClientCapabilities{}, err
	}
	if out.SubtitleFormats, err = normalizeCapabilityList(c.SubtitleFormats, playbackSubtitleAliases); err != nil {
		return ClientCapabilities{}, err
	}
	return out, nil
}

func normalizeCapabilityList(values []string, aliases map[string]string) ([]string, error) {
	if len(values) > ClientCapabilityListMax {
		return nil, ErrInvalid
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.ToLower(value)
		if len(token) < 1 || len(token) > ClientCapabilityTokenMax {
			return nil, ErrInvalid
		}
		for _, r := range token {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
				return nil, ErrInvalid
			}
		}
		if alias, ok := aliases[token]; ok {
			token = alias
		}
		if !slices.Contains(out, token) {
			out = append(out, token)
		}
	}
	return out, nil
}

// BuildPlaybackSource projects a stored record. It never fails: a record
// without usable probe metadata becomes an unprobed source.
func BuildPlaybackSource(r PlaybackSourceRecord) PlaybackSource {
	var meta MediaMetadata
	if r.Metadata != nil {
		meta = *r.Metadata
	}
	s := PlaybackSource{ID: r.ID, ContentType: r.ContentType, Container: ContainerFromContentType(r.ContentType),
		Probed: r.Metadata != nil, Version: VersionLabelsFromProbe(meta, r.FileName),
		Video: []PlaybackVideoTrack{}, Audio: []PlaybackAudioTrack{}, Subtitles: []PlaybackSubtitleTrack{}, External: []PlaybackExternalTrack{}}
	s.SizeBytes = r.ScanSize
	s.Primary = r.Primary
	if s.Probed {
		if meta.Format.SizeBytes != nil {
			s.SizeBytes = meta.Format.SizeBytes
		}
		s.DurationMicros = meta.Format.DurationMicros
		s.BitRate = meta.Format.BitRate
		if s.BitRate == nil {
			s.BitRate = averageBitRate(s.SizeBytes, s.DurationMicros)
		}
		primary := primaryVideoStream(meta)
		for _, stream := range meta.Streams {
			switch stream.Kind {
			case "video":
				if stream.Video == nil || stream.Codec != nil && versionImageCodecs[*stream.Codec] {
					continue
				}
				v := stream.Video
				track := PlaybackVideoTrack{Index: stream.Index, Codec: playbackText(stream.Codec), Profile: playbackText(stream.Profile), Level: v.Level,
					Width: v.Width, Height: v.Height, FrameRate: v.AverageFrameRate, BitRate: stream.BitRate, Default: playbackFlag(stream.Default),
					Primary: primary != nil && primary.Index == stream.Index}
				if track.FrameRate == nil {
					track.FrameRate = v.FrameRate
				}
				s.Video = append(s.Video, track)
			case "audio":
				track := PlaybackAudioTrack{Index: stream.Index, Codec: playbackText(stream.Codec), Profile: playbackText(stream.Profile),
					Language: playbackText(stream.Language), BitRate: stream.BitRate, Default: playbackFlag(stream.Default), Forced: playbackFlag(stream.Forced)}
				if a := stream.Audio; a != nil {
					track.Channels, track.ChannelLayout, track.SampleRate, track.Atmos = a.Channels, playbackText(a.ChannelLayout), a.SampleRate, playbackFlag(a.Atmos)
				}
				s.Audio = append(s.Audio, track)
			case "subtitle":
				codec := playbackText(stream.Codec)
				_, text := ExtractableSubtitleCodecs[codec]
				s.Subtitles = append(s.Subtitles, PlaybackSubtitleTrack{Index: stream.Index, Codec: codec, Format: playbackSubtitleCodecs[codec],
					Language: playbackText(stream.Language), Default: playbackFlag(stream.Default), Forced: playbackFlag(stream.Forced),
					Title: streamTitle(meta.Matroska, stream.Index), Extractable: text && IsMatroskaMetadata(meta),
					OCRSource: OCRSubtitleCodecs[codec] && IsMatroskaMetadata(meta)})
			}
		}
		s.Attachments = playbackAttachments(meta)
	}
	for _, sidecar := range r.Sidecars {
		t := sidecar.Track
		track := PlaybackExternalTrack{ID: sidecar.ID, Kind: t.Kind, Format: t.Format, Language: t.Language, Languages: t.Languages,
			Title: t.Title, Forced: t.Forced, SDH: t.SDH, Default: t.Default, Commentary: t.Commentary, Charset: sidecar.Charset, SizeBytes: sidecar.Size}
		if t.Kind == SidecarKindSubtitle {
			track.Codec = playbackSidecarSubtitles[t.Format]
		} else {
			track.Codec = playbackSidecarAudio[t.Format]
		}
		s.External = append(s.External, track)
	}
	return s
}

func streamTitle(m *MediaMatroska, index int) string {
	if m == nil {
		return ""
	}
	for _, t := range m.StreamTitles {
		if t.Index == index {
			return t.Title
		}
	}
	return ""
}

// playbackAttachments pairs the supplement's attachments with the probe's
// attachment streams by position when both list the same number.
func playbackAttachments(meta MediaMetadata) []PlaybackAttachment {
	if meta.Matroska == nil || len(meta.Matroska.Attachments) == 0 {
		return nil
	}
	var streams []int
	for _, stream := range meta.Streams {
		if stream.Kind == "attachment" {
			streams = append(streams, stream.Index)
		}
	}
	out := make([]PlaybackAttachment, 0, len(meta.Matroska.Attachments))
	for i, a := range meta.Matroska.Attachments {
		attachment := PlaybackAttachment{ID: a.ID, FileName: a.FileName, Font: a.Font}
		if len(streams) == len(meta.Matroska.Attachments) {
			index := streams[i]
			attachment.StreamIndex = &index
		}
		out = append(out, attachment)
	}
	return out
}

// averageBitRate derives bits per second from size and duration when the
// container did not state a bit rate. Both must be known and positive.
func averageBitRate(size, durationMicros *int64) *int64 {
	if size == nil || durationMicros == nil || *size <= 0 || *durationMicros <= 0 {
		return nil
	}
	rate := new(big.Int).Mul(big.NewInt(*size), big.NewInt(8_000_000))
	rate.Quo(rate, big.NewInt(*durationMicros))
	if !rate.IsInt64() || rate.Int64() < 1 {
		return nil
	}
	value := rate.Int64()
	return &value
}

func playbackText(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func playbackFlag(v *bool) bool { return v != nil && *v }

// DecideDirectPlay reports whether the client can play the source as it is
// and, if not, every reason in a fixed order: container, source_not_probed,
// video codec, audio codec, bit rate. It never proposes a conversion.
//
// An unprobed source is never confirmed: its codecs are unknown, so the
// verdict is false with source_not_probed (plus container_unsupported when
// the stored container already rules it out). The primary video stream must
// be declared; when the source has audio, at least one embedded audio stream
// must be declared. A declared MaxBitrate is exceeded only when the known bit
// rate is strictly greater; an unknown bit rate is not reported.
func DecideDirectPlay(caps ClientCapabilities, source PlaybackSource) (bool, []string) {
	caps, err := NormalizeClientCapabilities(caps)
	if err != nil {
		caps = ClientCapabilities{}
	}
	reasons := make([]string, 0, 4)
	if source.Container == "" || !slices.Contains(caps.Containers, source.Container) {
		reasons = append(reasons, PlaybackReasonContainer)
	}
	if !source.Probed {
		return false, append(reasons, PlaybackReasonSourceNotProbe)
	}
	for _, video := range source.Video {
		if video.Primary && !supportedCodec(caps.VideoCodecs, video.Codec) {
			reasons = append(reasons, PlaybackReasonVideoCodec)
		}
	}
	if len(source.Audio) > 0 && !slices.ContainsFunc(source.Audio, func(a PlaybackAudioTrack) bool { return supportedCodec(caps.AudioCodecs, a.Codec) }) {
		reasons = append(reasons, PlaybackReasonAudioCodec)
	}
	if caps.MaxBitrate > 0 && source.BitRate != nil && *source.BitRate > caps.MaxBitrate {
		reasons = append(reasons, PlaybackReasonBitrate)
	}
	return len(reasons) == 0, reasons
}

// supportedCodec matches a canonical codec. A declared "pcm" covers every
// PCM sample format; an unknown codec is never supported.
func supportedCodec(declared []string, codec string) bool {
	if codec == "" {
		return false
	}
	return slices.Contains(declared, codec) || strings.HasPrefix(codec, "pcm") && slices.Contains(declared, "pcm")
}

// CheckPlayback combines DecideDirectPlay with a per-track report of every
// audio and subtitle track, embedded or external, the client cannot read.
// Unsupported tracks never change the source verdict.
func CheckPlayback(caps ClientCapabilities, source PlaybackSource) PlaybackDecision {
	ok, reasons := DecideDirectPlay(caps, source)
	caps, err := NormalizeClientCapabilities(caps)
	if err != nil {
		caps = ClientCapabilities{}
	}
	d := PlaybackDecision{SourceID: source.ID, DirectPlay: ok, Reasons: reasons, Tracks: []PlaybackTrackDecision{}}
	if !ok {
		d.Code = PlaybackUnsupportedCode
	}
	for _, a := range source.Audio {
		index := a.Index
		d.Tracks = append(d.Tracks, trackDecision(SidecarKindAudio, &index, supportedCodec(caps.AudioCodecs, a.Codec), PlaybackReasonAudioCodec))
	}
	for _, s := range source.Subtitles {
		index := s.Index
		d.Tracks = append(d.Tracks, trackDecision(SidecarKindSubtitle, &index, s.Format != "" && slices.Contains(caps.SubtitleFormats, s.Format), PlaybackReasonSubtitleFormat))
	}
	for _, e := range source.External {
		decision := PlaybackTrackDecision{Kind: e.Kind, ID: e.ID, External: true}
		switch {
		case e.Codec == "":
			decision.Reason = PlaybackReasonTrackNotProbed
		case e.Kind == SidecarKindSubtitle:
			decision.Supported = slices.Contains(caps.SubtitleFormats, e.Codec)
			decision.Reason = PlaybackReasonSubtitleFormat
		default:
			decision.Supported = supportedCodec(caps.AudioCodecs, e.Codec)
			decision.Reason = PlaybackReasonAudioCodec
		}
		if decision.Supported {
			decision.Reason = ""
		} else {
			decision.Code = PlaybackUnsupportedCode
		}
		d.Tracks = append(d.Tracks, decision)
	}
	return d
}

func trackDecision(kind string, index *int, supported bool, reason string) PlaybackTrackDecision {
	d := PlaybackTrackDecision{Kind: kind, Index: index, Supported: supported}
	if !supported {
		d.Code, d.Reason = PlaybackUnsupportedCode, reason
	}
	return d
}
