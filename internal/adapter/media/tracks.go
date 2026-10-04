package media

import (
	"slices"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TrackKind names the kind of an external track route. The values match the
// sidecar kinds stored by the scan.
type TrackKind string

const (
	TrackSubtitle TrackKind = domain.SidecarKindSubtitle
	TrackAudio    TrackKind = domain.SidecarKindAudio
)

func (k TrackKind) valid() bool { return k == TrackSubtitle || k == TrackAudio }

// trackTypes maps a lower-case sidecar extension to the content type it is
// delivered with. The table is fixed: the system MIME database is never
// consulted, so a host mapping cannot turn a subtitle into a document type.
// Extensions that name no single format (".sub" may be MicroDVD text or VobSub
// data; ".alac" has no standard container) and anything unknown are sent as
// application/octet-stream.
var trackTypes = map[TrackKind]map[string]string{
	TrackSubtitle: {
		"srt": "application/x-subrip", "ass": "text/x-ssa", "ssa": "text/x-ssa",
		"vtt": "text/vtt", "webvtt": "text/vtt", "ttml": "application/ttml+xml", "dfxp": "application/ttml+xml",
		"smi": "application/x-sami", "sami": "application/x-sami", "idx": "text/plain", "sup": "application/x-pgs",
	},
	TrackAudio: {
		"mka": "audio/x-matroska", "aac": "audio/aac", "m4a": "audio/mp4", "ac3": "audio/ac3",
		"eac3": "audio/eac3", "ec3": "audio/eac3", "dts": "audio/vnd.dts", "dtshd": "audio/vnd.dts.hd",
		"thd": "audio/vnd.dolby.mlp", "truehd": "audio/vnd.dolby.mlp", "mlp": "audio/vnd.dolby.mlp",
		"flac": "audio/flac", "opus": "audio/opus", "ogg": "audio/ogg", "oga": "audio/ogg",
		"mp3": "audio/mpeg", "wav": "audio/wav",
	},
}

// textSubtitles are the subtitle extensions that are text, so a detected
// charset is meaningful for them. A ".sub" with a detected charset is
// MicroDVD text; without one it stays opaque.
var textSubtitles = map[string]string{
	"srt": "", "ass": "", "ssa": "", "vtt": "", "webvtt": "", "ttml": "", "dfxp": "",
	"smi": "", "sami": "", "idx": "", "sub": "text/x-microdvd",
}

// TrackContentType returns the content type of an external track from its
// file extension (with or without the leading dot) and, for a text subtitle,
// its detected charset. The charset is only reported as a parameter and only
// when it is one of the names the scan records; the bytes are delivered as
// they are. Unknown formats are application/octet-stream.
func TrackContentType(kind TrackKind, extension, charset string) string {
	ext := strings.ToLower(strings.TrimPrefix(extension, "."))
	contentType := trackTypes[kind][ext]
	known := kind == TrackSubtitle && charset != "" && slices.Contains(domain.SidecarCharsets, charset)
	if fallback, text := textSubtitles[ext]; kind == TrackSubtitle && text && known {
		if contentType == "" {
			contentType = fallback
		}
		return contentType + "; charset=" + charset
	}
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}
