package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MediaMetadata records only explicit probe observations. Nil fields mean
// unknown/absent, not zero, false, unsupported, or a failed playback decision.
// Parsing these values does not validate a media file or certify compatibility.
type MediaMetadata struct {
	Format   MediaFormat    `json:"format"`
	Streams  []MediaStream  `json:"streams"`
	Chapters []MediaChapter `json:"chapters"`
	// Matroska is the optional MediaInfo supplement (metadata schema 2).
	Matroska *MediaMatroska `json:"matroska,omitempty"`
}

type MediaFormat struct {
	Names          []string `json:"names,omitempty"`
	DurationMicros *int64   `json:"durationMicros,omitempty"`
	SizeBytes      *int64   `json:"sizeBytes,omitempty"`
	BitRate        *int64   `json:"bitRate,omitempty"`
}

type MediaRational struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

type MediaStream struct {
	Index          int     `json:"index"`
	Kind           string  `json:"kind"`
	Codec          *string `json:"codec,omitempty"`
	Profile        *string `json:"profile,omitempty"`
	DurationMicros *int64  `json:"durationMicros,omitempty"`
	BitRate        *int64  `json:"bitRate,omitempty"`
	Language       *string `json:"language,omitempty"`
	Default        *bool   `json:"default,omitempty"`
	Forced         *bool   `json:"forced,omitempty"`
	// AttachedPic is the attached_pic disposition: an embedded cover image
	// (G40.4), not a playable video track.
	AttachedPic *bool `json:"attachedPic,omitempty"`
	// Title is the stream's own title tag, kept only for audio and subtitle
	// streams and only when it passes ValidStreamTitle (B3, parser v5).
	Title *string `json:"title,omitempty"`
	// The accessibility and purpose dispositions below are kept only when
	// true (B3, parser v5); absent means the file did not state them.
	HearingImpaired *bool       `json:"hearingImpaired,omitempty"`
	Captions        *bool       `json:"captions,omitempty"`
	Comment         *bool       `json:"comment,omitempty"`
	VisualImpaired  *bool       `json:"visualImpaired,omitempty"`
	Descriptions    *bool       `json:"descriptions,omitempty"`
	Video           *MediaVideo `json:"video,omitempty"`
	Audio           *MediaAudio `json:"audio,omitempty"`
}

// MaxStreamTitleBytes bounds a stored stream title.
const MaxStreamTitleBytes = 256

// ValidStreamTitle accepts a trimmed, non-empty, valid UTF-8 title without
// control, format (bidi override, zero width) or line/paragraph separator
// characters and without the U+FFFD replacement character (a decoder already
// gave up on those bytes). Titles come from untrusted files and reach clients as display
// text, so anything else is dropped, never repaired.
func ValidStreamTitle(title string) bool {
	if title == "" || len(title) > MaxStreamTitleBytes || !utf8.ValidString(title) || strings.TrimSpace(title) != title {
		return false
	}
	return !strings.ContainsFunc(title, func(r rune) bool {
		return r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	})
}

type MediaVideo struct {
	Level            *int64                 `json:"level,omitempty"`
	Width            *int64                 `json:"width,omitempty"`
	Height           *int64                 `json:"height,omitempty"`
	FrameRate        *MediaRational         `json:"frameRate,omitempty"`
	AverageFrameRate *MediaRational         `json:"averageFrameRate,omitempty"`
	ColorRange       *string                `json:"colorRange,omitempty"`
	ColorSpace       *string                `json:"colorSpace,omitempty"`
	ColorTransfer    *string                `json:"colorTransfer,omitempty"`
	ColorPrimaries   *string                `json:"colorPrimaries,omitempty"`
	MasteringDisplay *MediaMasteringDisplay `json:"masteringDisplay,omitempty"`
	ContentLight     *MediaContentLight     `json:"contentLight,omitempty"`
	DolbyVision      *MediaDolbyVision      `json:"dolbyVision,omitempty"`
	HDR10Plus        *bool                  `json:"hdr10Plus,omitempty"`
}

type MediaMasteringDisplay struct {
	RedX         *MediaRational `json:"redX,omitempty"`
	RedY         *MediaRational `json:"redY,omitempty"`
	GreenX       *MediaRational `json:"greenX,omitempty"`
	GreenY       *MediaRational `json:"greenY,omitempty"`
	BlueX        *MediaRational `json:"blueX,omitempty"`
	BlueY        *MediaRational `json:"blueY,omitempty"`
	WhiteX       *MediaRational `json:"whiteX,omitempty"`
	WhiteY       *MediaRational `json:"whiteY,omitempty"`
	MinLuminance *MediaRational `json:"minLuminance,omitempty"`
	MaxLuminance *MediaRational `json:"maxLuminance,omitempty"`
}

type MediaContentLight struct {
	MaxContent *int64 `json:"maxContent,omitempty"`
	MaxAverage *int64 `json:"maxAverage,omitempty"`
}

type MediaDolbyVision struct {
	Profile          *int64 `json:"profile,omitempty"`
	Level            *int64 `json:"level,omitempty"`
	RPU              *bool  `json:"rpu,omitempty"`
	EnhancementLayer *bool  `json:"enhancementLayer,omitempty"`
	BaseLayer        *bool  `json:"baseLayer,omitempty"`
	CompatibilityID  *int64 `json:"compatibilityId,omitempty"`
}

type MediaAudio struct {
	Channels         *int64  `json:"channels,omitempty"`
	ChannelLayout    *string `json:"channelLayout,omitempty"`
	SampleRate       *int64  `json:"sampleRate,omitempty"`
	BitsPerSample    *int64  `json:"bitsPerSample,omitempty"`
	BitsPerRawSample *int64  `json:"bitsPerRawSample,omitempty"`
	Atmos            *bool   `json:"atmos,omitempty"`
}

type MediaChapter struct {
	ID          int64  `json:"id"`
	StartMicros *int64 `json:"startMicros,omitempty"`
	EndMicros   *int64 `json:"endMicros,omitempty"`
}
