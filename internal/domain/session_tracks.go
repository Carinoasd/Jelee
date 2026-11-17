package domain

import (
	"strconv"
	"time"
)

// Session level track choices (G20.4). While a client plays one version it
// reports the audio and subtitle it switched to; the server keeps the latest
// choice per user and item for SessionTrackTTL and applies it, for that
// version only, when the user resumes the item or asks for playback
// information again. Precedence of the default tracks, most specific first:
//
//	session choice (this version) > version preference > item preference >
//	user defaults (G16.5) > account language (subtitles) > the file's flags
//
// A session choice names exact tracks, so it never carries over to another
// version; storing a track preference of the item, or the user's defaults,
// removes the item's (or every) session choice, so a deliberate preference
// is never shadowed by an older switch. Like preferences it only picks
// among original tracks and is not audited.

const (
	// TrackBasisSession means a session choice decided the default tracks.
	TrackBasisSession = "session"
	// SubtitleTrackOff is the session choice "no subtitles".
	SubtitleTrackOff = "off"
	// SessionTrackTTL is how long a session choice is applied.
	SessionTrackTTL = 30 * 24 * time.Hour
	// SessionTrackUserMax bounds the choices one user keeps; older ones
	// are removed first.
	SessionTrackUserMax = 1000
	// trackIndexMax matches the "e:<index>" reference range.
	trackIndexMax = 99999
)

// PlaybackTrackReport is the track state a playback report carries. The
// upstream API names tracks by stream index (AudioIndex, SubtitleIndex,
// with -1 for no subtitles and external files numbered after every
// embedded stream, see UpstreamExternalTracks); the native API names them by reference (AudioTrack,
// SubtitleTrack: "e:<index>", "x:<track ID>" or "off" for subtitles).
type PlaybackTrackReport struct {
	AudioIndex    *int
	SubtitleIndex *int
	AudioTrack    string
	SubtitleTrack string
}

// Empty reports whether the report names no track.
func (r PlaybackTrackReport) Empty() bool {
	return r.AudioIndex == nil && r.SubtitleIndex == nil && r.AudioTrack == "" && r.SubtitleTrack == ""
}

// Valid checks the shape only; whether a track exists is decided against
// the version (ResolveTrackReport).
func (r PlaybackTrackReport) Valid() bool {
	if r.AudioIndex != nil && (*r.AudioIndex < 0 || *r.AudioIndex > trackIndexMax) {
		return false
	}
	if r.SubtitleIndex != nil && (*r.SubtitleIndex < -1 || *r.SubtitleIndex > trackIndexMax) {
		return false
	}
	if r.AudioTrack != "" && !ValidTrackRef(r.AudioTrack) {
		return false
	}
	return r.SubtitleTrack == "" || r.SubtitleTrack == SubtitleTrackOff || ValidTrackRef(r.SubtitleTrack)
}

// Fingerprint identifies the reported state, so an unchanged state is not
// written again.
func (r PlaybackTrackReport) Fingerprint() string {
	index := func(v *int) string {
		if v == nil {
			return "-"
		}
		return strconv.Itoa(*v)
	}
	return index(r.AudioIndex) + "|" + index(r.SubtitleIndex) + "|" + r.AudioTrack + "|" + r.SubtitleTrack
}

// SessionTracks is a stored session choice for one version. An empty
// member was not reported and inherits the preferences.
type SessionTracks struct {
	SourceID      string `json:"sourceId"`
	AudioTrack    string `json:"audioTrack,omitempty"`
	SubtitleTrack string `json:"subtitleTrack,omitempty"`
}

// UpstreamExternal is an external file with its upstream stream index.
type UpstreamExternal struct {
	Index int
	Track PlaybackExternalTrack
}

// UpstreamSubtitle is an external subtitle file with its upstream stream
// index.
type UpstreamSubtitle = UpstreamExternal

// UpstreamExternalTracks numbers the external files of a source after every
// embedded stream, in the stable order storage lists them: first the
// subtitle files, as upstream does; then one slot per bitmap subtitle that
// OCR can derive text from (the compatibility layer numbers the derived
// SubRip streams there, listed or not, so the numbers never move when an
// OCR result appears); then the audio files. Upstream has no external audio
// stream; numbering it last keeps every subtitle index unchanged. Files
// whose format cannot appear in a route token (lower case letters and
// digits, at most 16) are left out.
func UpstreamExternalTracks(source PlaybackSource) []UpstreamExternal {
	next := 0
	for _, v := range source.Video {
		next = max(next, v.Index+1)
	}
	for _, a := range source.Audio {
		next = max(next, a.Index+1)
	}
	for _, s := range source.Subtitles {
		next = max(next, s.Index+1)
	}
	var out []UpstreamExternal
	for _, kind := range []string{SidecarKindSubtitle, SidecarKindAudio} {
		if kind == SidecarKindAudio {
			for _, s := range source.Subtitles {
				if s.OCRSource {
					next++
				}
			}
		}
		for _, track := range source.External {
			if track.Kind != kind || !upstreamRouteToken(track.Format) {
				continue
			}
			out = append(out, UpstreamExternal{Index: next, Track: track})
			next++
		}
	}
	return out
}

// UpstreamExternalSubtitles is the subtitle part of UpstreamExternalTracks.
func UpstreamExternalSubtitles(source PlaybackSource) []UpstreamSubtitle {
	return upstreamExternalKind(source, SidecarKindSubtitle)
}

// UpstreamExternalAudio is the audio part of UpstreamExternalTracks.
func UpstreamExternalAudio(source PlaybackSource) []UpstreamExternal {
	return upstreamExternalKind(source, SidecarKindAudio)
}

func upstreamExternalKind(source PlaybackSource, kind string) []UpstreamExternal {
	var out []UpstreamExternal
	for _, e := range UpstreamExternalTracks(source) {
		if e.Track.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func upstreamRouteToken(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// ResolveTrackReport maps a report to the tracks of the version it played.
// A track the version does not have is dropped; ok is false when nothing
// usable remains.
func ResolveTrackReport(source PlaybackSource, r PlaybackTrackReport) (SessionTracks, bool) {
	out := SessionTracks{SourceID: source.ID}
	has := func(ref string, audio bool) bool {
		for _, t := range source.External {
			if "x:"+t.ID == ref && (t.Kind == SidecarKindAudio) == audio {
				return true
			}
		}
		if audio {
			for _, a := range source.Audio {
				if "e:"+strconv.Itoa(a.Index) == ref {
					return true
				}
			}
			return false
		}
		for _, s := range source.Subtitles {
			if "e:"+strconv.Itoa(s.Index) == ref {
				return true
			}
		}
		return false
	}
	switch {
	case r.AudioTrack != "":
		if has(r.AudioTrack, true) {
			out.AudioTrack = r.AudioTrack
		}
	case r.AudioIndex != nil:
		if ref := "e:" + strconv.Itoa(*r.AudioIndex); has(ref, true) {
			out.AudioTrack = ref
			break
		}
		for _, e := range UpstreamExternalAudio(source) {
			if e.Index == *r.AudioIndex {
				out.AudioTrack = "x:" + e.Track.ID
			}
		}
	}
	switch {
	case r.SubtitleTrack == SubtitleTrackOff:
		out.SubtitleTrack = SubtitleTrackOff
	case r.SubtitleTrack != "":
		if has(r.SubtitleTrack, false) {
			out.SubtitleTrack = r.SubtitleTrack
		}
	case r.SubtitleIndex != nil && *r.SubtitleIndex < 0:
		out.SubtitleTrack = SubtitleTrackOff
	case r.SubtitleIndex != nil:
		if ref := "e:" + strconv.Itoa(*r.SubtitleIndex); has(ref, false) {
			out.SubtitleTrack = ref
			break
		}
		for _, e := range UpstreamExternalSubtitles(source) {
			if e.Index == *r.SubtitleIndex {
				out.SubtitleTrack = "x:" + e.Track.ID
			}
		}
	}
	return out, out.AudioTrack != "" || out.SubtitleTrack != ""
}

// preference turns a session choice into the most specific preference
// level: a named subtitle is shown whatever the broader subtitle mode.
func (s SessionTracks) preference() TrackPreference {
	var p TrackPreference
	if s.AudioTrack != "" {
		v := s.AudioTrack
		p.AudioTrack = &v
	}
	switch s.SubtitleTrack {
	case "":
	case SubtitleTrackOff:
		mode := SubtitleModeOff
		p.SubtitleMode = &mode
	default:
		v, mode := s.SubtitleTrack, SubtitleModeAlways
		p.SubtitleTrack, p.SubtitleMode = &v, &mode
	}
	return p
}
