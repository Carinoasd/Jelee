package compat

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
)

// Stream display titles (G24.2). Most upstream clients label their track
// menus with MediaStream.DisplayTitle alone, so every listed stream carries
// one, assembled the way upstream assembles it (MediaStream.DisplayTitle):
//
//	video:    resolution, codec and HDR format joined by spaces
//	audio:    language - codec or profile - channel layout or "N ch"
//	          - Commentary - Default - External
//	subtitle: language (else Undetermined) - SDH - Default - Forced
//	          - format - External
//
// A stream with its own title starts with that title and appends, after
// " - ", only the parts the title does not already contain. Language names
// and the flag words follow the locale the request negotiates through
// Accept-Language (i18n.Locale), like the server's error messages; codec,
// resolution and channel tokens are not translated. The commentary word is
// a Jelee addition: upstream has no commentary flag to show.

// requestLocale is the display locale of a request.
func requestLocale(r *http.Request) string {
	return i18n.Locale(r.Header.Get("Accept-Language"))
}

// setDisplayTitles fills DisplayTitle of every stream that has none.
func setDisplayTitles(info *mediaSourceInfo, locale string) {
	for i := range info.MediaStreams {
		if info.MediaStreams[i].DisplayTitle == "" {
			info.MediaStreams[i].DisplayTitle = displayTitle(info.MediaStreams[i], locale)
		}
	}
}

func displayTitle(s mediaStream, locale string) string {
	var parts []string
	separator := " - "
	switch s.Type {
	case mediaStreamVideo:
		separator = " "
		if r := resolutionText(s.Width, s.Height); r != "" {
			parts = append(parts, r)
		}
		if s.Codec != "" {
			parts = append(parts, strings.ToUpper(s.Codec))
		}
		if hdr := rangeText(s.VideoRangeType); hdr != "" {
			parts = append(parts, hdr)
		}
	case mediaStreamAudio:
		if name := languageText(s.Language, locale); name != "" {
			parts = append(parts, name)
		}
		switch {
		case s.Profile != "" && !strings.EqualFold(s.Profile, "lc"):
			parts = append(parts, s.Profile)
		case s.Codec != "":
			parts = append(parts, audioCodecName(s.Codec))
		}
		switch {
		case s.ChannelLayout != "":
			parts = append(parts, firstUpper(s.ChannelLayout))
		case s.Channels != nil:
			parts = append(parts, strconv.FormatInt(*s.Channels, 10)+" ch")
		}
		if s.commentary {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelCommentary))
		}
		if s.IsDefault {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelDefault))
		}
		if s.IsExternal {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelExternal))
		}
	case mediaStreamSubtitle:
		name := languageText(s.Language, locale)
		if name == "" {
			name = i18n.TrackLabel(locale, i18n.TrackLabelUndetermined)
		}
		parts = append(parts, name)
		if s.IsHearingImpaired {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelSDH))
		}
		if s.IsDefault {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelDefault))
		}
		if s.IsForced {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelForced))
		}
		if s.Codec != "" {
			parts = append(parts, strings.ToUpper(s.Codec))
		}
		if s.IsExternal {
			parts = append(parts, i18n.TrackLabel(locale, i18n.TrackLabelExternal))
		}
	}
	if s.Title == "" {
		return strings.Join(parts, separator)
	}
	var b strings.Builder
	b.WriteString(s.Title)
	title := strings.ToLower(s.Title)
	for _, part := range parts {
		if !strings.Contains(title, strings.ToLower(part)) {
			b.WriteString(" - ")
			b.WriteString(part)
		}
	}
	return b.String()
}

// specialLanguages declare no language: undetermined, multiple, no
// linguistic content and uncoded.
var specialLanguages = map[string]bool{"und": true, "mul": true, "zxx": true, "mis": true}

// languageText names a declared language in locale; a language outside the
// name table keeps the declared code with a capital first letter, as
// upstream does with codes it cannot name. Special codes name nothing.
func languageText(language, locale string) string {
	language = strings.TrimSpace(language)
	if language == "" || specialLanguages[strings.ToLower(language)] {
		return ""
	}
	if tag, ok := domain.NormalizeTrackLanguage(language); ok {
		if name, ok := i18n.LanguageName(locale, tag); ok {
			return name
		}
	}
	return firstUpper(language)
}

// audioCodecName is upstream's friendly audio codec name.
func audioCodecName(codec string) string {
	switch strings.ToLower(codec) {
	case "ac3":
		return "Dolby Digital"
	case "eac3":
		return "Dolby Digital+"
	case "dca", "dts":
		return "DTS"
	case "truehd":
		return "Dolby TrueHD"
	}
	return strings.ToUpper(codec)
}

// resolutionText is upstream's resolution name of a frame size; the probe
// does not record interlacing, so every name is progressive.
func resolutionText(width, height *int64) string {
	if width == nil || height == nil {
		return ""
	}
	w, h := *width, *height
	for _, r := range []struct {
		w, h int64
		name string
	}{
		{256, 144, "144p"}, {426, 240, "240p"}, {640, 360, "360p"}, {682, 384, "384p"}, {720, 404, "404p"}, {854, 480, "480p"},
		{960, 544, "540p"}, {1024, 576, "576p"}, {1280, 962, "720p"}, {2560, 1440, "1080p"}, {4096, 3072, "4K"}, {8192, 6144, "8K"},
	} {
		if w <= r.w && h <= r.h {
			return r.name
		}
	}
	return ""
}

// rangeText names an HDR stream's format; SDR and unknown show nothing.
func rangeText(rangeType string) string {
	switch rangeType {
	case "":
		return ""
	case "DOVI":
		return "Dolby Vision"
	case "HDR10Plus":
		return "HDR10+"
	}
	return rangeType
}

// frameRate converts a probe rational to frames per second, rounded to
// three decimals; nil when unknown.
func frameRate(r *domain.MediaRational) *float64 {
	if r == nil || r.Numerator <= 0 || r.Denominator <= 0 {
		return nil
	}
	v := math.Round(float64(r.Numerator)/float64(r.Denominator)*1000) / 1000
	if v <= 0 || v > 1000 {
		return nil
	}
	return &v
}

func firstUpper(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
