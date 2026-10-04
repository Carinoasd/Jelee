package domain

import (
	"strconv"
	"strings"
)

// Track preferences (G16.5, G20.4). A user keeps defaults of their own, one
// preference per item (every version) and one per version (one media
// source). A member left null inherits the broader level; the most specific
// level that sets a member decides it. Preferences only choose which of the
// original tracks a client starts with: nothing is converted, mixed or
// burned in, and a client may always pick another track.

// Subtitle modes.
const (
	// SubtitleModeAuto shows subtitles in the subtitle language when the
	// chosen audio is in another language, else only forced subtitles.
	SubtitleModeAuto = "auto"
	// SubtitleModeAlways shows subtitles in the subtitle language whatever
	// the audio.
	SubtitleModeAlways = "always"
	// SubtitleModeForced shows forced (foreign parts only) subtitles only.
	SubtitleModeForced = "forced"
	// SubtitleModeOff shows no subtitles.
	SubtitleModeOff = "off"
)

// Basis values: which level decided the default tracks.
const (
	TrackBasisVersion = "version"
	TrackBasisItem    = "item"
	TrackBasisUser    = "user"
	TrackBasisLocale  = "locale"
	TrackBasisSource  = "source"
)

// Track selection kinds.
const (
	TrackEmbedded = "embedded"
	TrackExternal = "external"
)

// TrackPreference is one stored level. Every member is optional. A named
// track ("e:<stream index>" for an embedded stream, "x:<track ID>" for an
// external file) is only valid on a version.
type TrackPreference struct {
	AudioLanguage    *string `json:"audioLanguage"`
	AudioCommentary  *bool   `json:"audioCommentary"`
	AudioTrack       *string `json:"audioTrack"`
	SubtitleMode     *string `json:"subtitleMode"`
	SubtitleLanguage *string `json:"subtitleLanguage"`
	SubtitleSDH      *bool   `json:"subtitleSdh"`
	SubtitleTrack    *string `json:"subtitleTrack"`
}

// Empty reports whether no member is set; an empty preference is stored as
// no row.
func (p TrackPreference) Empty() bool {
	return p.AudioLanguage == nil && p.AudioCommentary == nil && p.AudioTrack == nil && p.SubtitleMode == nil &&
		p.SubtitleLanguage == nil && p.SubtitleSDH == nil && p.SubtitleTrack == nil
}

// NormalizeTrackPreference validates a preference for a level and returns
// it with canonical language tags. version is true for a version level, the
// only level that may name a track.
func NormalizeTrackPreference(p TrackPreference, version bool) (TrackPreference, error) {
	out := p
	for _, language := range []**string{&out.AudioLanguage, &out.SubtitleLanguage} {
		if *language == nil {
			continue
		}
		tag, ok := NormalizeTrackLanguage(**language)
		if !ok {
			return TrackPreference{}, ErrInvalid
		}
		*language = &tag
	}
	if out.SubtitleMode != nil {
		switch *out.SubtitleMode {
		case SubtitleModeAuto, SubtitleModeAlways, SubtitleModeForced, SubtitleModeOff:
		default:
			return TrackPreference{}, ErrInvalid
		}
	}
	for _, track := range []*string{out.AudioTrack, out.SubtitleTrack} {
		if track == nil {
			continue
		}
		if !version || !ValidTrackRef(*track) {
			return TrackPreference{}, ErrInvalid
		}
	}
	return out, nil
}

// ValidTrackRef accepts "e:<index>" (0–99999) and "x:<track ID>".
func ValidTrackRef(ref string) bool {
	kind, value, ok := strings.Cut(ref, ":")
	if !ok {
		return false
	}
	switch kind {
	case "e":
		if len(value) < 1 || len(value) > 5 {
			return false
		}
		_, err := strconv.ParseUint(value, 10, 32)
		return err == nil && (value == "0" || value[0] != '0')
	case "x":
		return ValidID(value) && strings.ToLower(value) == value
	}
	return false
}

// NormalizeTrackLanguage maps a language tag or code ("en", "eng",
// "zh-TW", "chs") to the canonical BCP 47 shape the sidecar parser emits.
func NormalizeTrackLanguage(value string) (string, bool) {
	if len(value) == 0 || len(value) > 35 {
		return "", false
	}
	tag, ok := sidecarLanguage(strings.TrimSpace(value))
	if !ok || !ValidSidecarLanguageTag(tag) {
		return "", false
	}
	return tag, true
}

// TrackPreferenceSet is everything one user stored for one item, plus the
// account locale used when no level names a subtitle language.
type TrackPreferenceSet struct {
	User     *TrackPreference
	Item     *TrackPreference
	Versions map[string]TrackPreference
	Locale   string
}

// Effective merges the levels for one version, most specific first, and
// reports the most specific level that set any member.
func (s TrackPreferenceSet) Effective(sourceID string) (TrackPreference, string) {
	var out TrackPreference
	basis := ""
	apply := func(p *TrackPreference, level string) {
		if p == nil || p.Empty() {
			return
		}
		if basis == "" {
			basis = level
		}
		fill(&out.AudioLanguage, p.AudioLanguage)
		fill(&out.AudioCommentary, p.AudioCommentary)
		fill(&out.AudioTrack, p.AudioTrack)
		fill(&out.SubtitleMode, p.SubtitleMode)
		fill(&out.SubtitleLanguage, p.SubtitleLanguage)
		fill(&out.SubtitleSDH, p.SubtitleSDH)
		fill(&out.SubtitleTrack, p.SubtitleTrack)
	}
	if v, ok := s.Versions[sourceID]; ok {
		apply(&v, TrackBasisVersion)
	}
	apply(s.Item, TrackBasisItem)
	apply(s.User, TrackBasisUser)
	if out.SubtitleLanguage == nil {
		if tag, ok := NormalizeTrackLanguage(s.Locale); ok {
			out.SubtitleLanguage = &tag
			if basis == "" {
				basis = TrackBasisLocale
			}
		}
	}
	if basis == "" {
		basis = TrackBasisSource
	}
	return out, basis
}

func fill[T any](dst **T, src *T) {
	if *dst == nil && src != nil {
		v := *src
		*dst = &v
	}
}

// TrackSelection names one original track: an embedded stream by index or
// an external file by ID.
type TrackSelection struct {
	Kind  string `json:"kind"`
	Index *int   `json:"index,omitempty"`
	ID    string `json:"id,omitempty"`
}

// DefaultTracks is the audio and subtitle a client should start with.
// Subtitle is nil when no subtitle should be shown; Audio is nil only for a
// source without any known audio track.
type DefaultTracks struct {
	Audio    *TrackSelection `json:"audio"`
	Subtitle *TrackSelection `json:"subtitle"`
	// Basis is the level that decided: version, item, user, locale (only
	// the account language applied) or source (the file's own defaults).
	Basis string `json:"basis"`
}

type trackCandidate struct {
	sel        TrackSelection
	languages  []string
	isDefault  bool
	forced     bool
	sdh        bool
	commentary bool
}

func (c trackCandidate) ref() string {
	if c.sel.Kind == TrackEmbedded {
		return "e:" + strconv.Itoa(*c.sel.Index)
	}
	return "x:" + c.sel.ID
}

func embeddedSelection(index int) TrackSelection {
	i := index
	return TrackSelection{Kind: TrackEmbedded, Index: &i}
}

// SelectDefaultTracks picks the default audio and subtitle of one source
// for an effective preference. Embedded streams come before external files
// on equal terms; within each, the listed order decides.
func SelectDefaultTracks(source PlaybackSource, p TrackPreference, basis string) DefaultTracks {
	var audio, subtitles []trackCandidate
	for _, a := range source.Audio {
		audio = append(audio, trackCandidate{sel: embeddedSelection(a.Index), languages: trackLanguages(a.Language, nil), isDefault: a.Default, forced: a.Forced})
	}
	for _, s := range source.Subtitles {
		subtitles = append(subtitles, trackCandidate{sel: embeddedSelection(s.Index), languages: trackLanguages(s.Language, nil), isDefault: s.Default, forced: s.Forced})
	}
	for _, t := range source.External {
		c := trackCandidate{sel: TrackSelection{Kind: TrackExternal, ID: t.ID}, languages: trackLanguages(t.Language, t.Languages),
			isDefault: t.Default, forced: t.Forced, sdh: t.SDH, commentary: t.Commentary}
		switch t.Kind {
		case SidecarKindAudio:
			audio = append(audio, c)
		case SidecarKindSubtitle:
			subtitles = append(subtitles, c)
		}
	}
	out := DefaultTracks{Basis: basis}
	chosen := pickAudio(audio, p)
	audioLanguages := []string(nil)
	if chosen != nil {
		sel := chosen.sel
		out.Audio = &sel
		audioLanguages = chosen.languages
	}
	if sub := pickSubtitle(subtitles, p, audioLanguages); sub != nil {
		sel := sub.sel
		out.Subtitle = &sel
	}
	return out
}

func trackLanguages(language string, languages []string) []string {
	var out []string
	for _, value := range append([]string{language}, languages...) {
		if tag, ok := NormalizeTrackLanguage(value); ok {
			out = append(out, tag)
		}
	}
	return out
}

// languageScore ranks how well a track's languages match a wanted tag: 3
// for the same tag, 2 for the same language and script (zh-TW and zh-Hant),
// 1 for the same primary language, 0 otherwise.
func languageScore(want string, have []string) int {
	best := 0
	for _, tag := range have {
		switch {
		case strings.EqualFold(tag, want):
			return 3
		case primaryLanguage(tag) != primaryLanguage(want):
		case languageScript(tag) != "" && languageScript(tag) == languageScript(want):
			best = max(best, 2)
		default:
			best = max(best, 1)
		}
	}
	return best
}

func primaryLanguage(tag string) string {
	primary, _, _ := strings.Cut(tag, "-")
	return strings.ToLower(primary)
}

// languageScript returns the explicit script subtag, or the script a
// Chinese region implies.
func languageScript(tag string) string {
	parts := strings.Split(tag, "-")
	for _, part := range parts[1:] {
		if len(part) == 4 {
			return part
		}
	}
	if strings.EqualFold(parts[0], "zh") && len(parts) > 1 {
		switch strings.ToUpper(parts[len(parts)-1]) {
		case "TW", "HK", "MO":
			return "Hant"
		case "CN", "SG", "MY":
			return "Hans"
		}
	}
	return ""
}

func findTrack(candidates []trackCandidate, ref *string) *trackCandidate {
	if ref == nil {
		return nil
	}
	for i := range candidates {
		if candidates[i].ref() == *ref {
			return &candidates[i]
		}
	}
	return nil
}

// best returns the first candidate with the highest score; a negative
// score excludes a candidate.
func best(candidates []trackCandidate, score func(trackCandidate) int) *trackCandidate {
	var chosen *trackCandidate
	top := -1
	for i := range candidates {
		if s := score(candidates[i]); s > top {
			chosen, top = &candidates[i], s
		}
	}
	if top < 0 {
		return nil
	}
	return chosen
}

func boolScore(v bool) int {
	if v {
		return 1
	}
	return 0
}

// pickAudio prefers a named track; then a commentary track when one is
// asked for; then the language, avoiding commentary otherwise; then the
// file's default flag.
func pickAudio(candidates []trackCandidate, p TrackPreference) *trackCandidate {
	if named := findTrack(candidates, p.AudioTrack); named != nil {
		return named
	}
	wantCommentary := p.AudioCommentary != nil && *p.AudioCommentary
	return best(candidates, func(c trackCandidate) int {
		language := 0
		if p.AudioLanguage != nil {
			language = languageScore(*p.AudioLanguage, c.languages)
		}
		return boolScore(wantCommentary && c.commentary)*1000 + language*100 + boolScore(c.commentary == wantCommentary)*10 + boolScore(c.isDefault)
	})
}

// pickSubtitle applies the subtitle mode (auto when unset). A named track
// wins unless subtitles are off.
func pickSubtitle(candidates []trackCandidate, p TrackPreference, audioLanguages []string) *trackCandidate {
	mode := SubtitleModeAuto
	if p.SubtitleMode != nil {
		mode = *p.SubtitleMode
	}
	if mode == SubtitleModeOff {
		return nil
	}
	if named := findTrack(candidates, p.SubtitleTrack); named != nil {
		return named
	}
	wantSDH := p.SubtitleSDH != nil && *p.SubtitleSDH
	full := func(language string) *trackCandidate {
		return best(candidates, func(c trackCandidate) int {
			score := languageScore(language, c.languages)
			if score == 0 || c.forced {
				// Forced tracks only cover foreign parts; a full subtitle is
				// wanted here.
				return -1
			}
			return score*100 + boolScore(c.sdh == wantSDH)*10 + boolScore(c.isDefault)
		})
	}
	forced := func(languages []string) *trackCandidate {
		return best(candidates, func(c trackCandidate) int {
			if !c.forced {
				return -1
			}
			score := 0
			for _, language := range languages {
				score = max(score, languageScore(language, c.languages))
			}
			if len(languages) > 0 && score == 0 {
				return -1
			}
			return score*10 + boolScore(c.isDefault)
		})
	}
	switch mode {
	case SubtitleModeForced:
		return forced(audioLanguages)
	case SubtitleModeAlways:
		if p.SubtitleLanguage != nil {
			return full(*p.SubtitleLanguage)
		}
		return best(candidates, func(c trackCandidate) int { return boolScore(!c.forced)*10 + boolScore(c.isDefault) })
	}
	// Auto: subtitles in the subtitle language when the audio is in
	// another language; otherwise forced subtitles for the audio language,
	// and without any language the file's own default subtitle.
	if p.SubtitleLanguage != nil {
		if len(audioLanguages) > 0 && languageScore(*p.SubtitleLanguage, audioLanguages) == 0 {
			if chosen := full(*p.SubtitleLanguage); chosen != nil {
				return chosen
			}
		}
		return forced(append(append([]string{}, audioLanguages...), *p.SubtitleLanguage))
	}
	if chosen := forced(audioLanguages); chosen != nil {
		return chosen
	}
	return best(candidates, func(c trackCandidate) int {
		if !c.isDefault {
			return -1
		}
		return boolScore(c.forced)
	})
}

// VersionTrackPreference is the stored preference of one version.
type VersionTrackPreference struct {
	SourceID   string          `json:"sourceId"`
	Preference TrackPreference `json:"preference"`
}

// TrackPreferenceView is what a user stored for one item: their own
// defaults, the item level and every version level. A null level inherits.
type TrackPreferenceView struct {
	ItemID   string                   `json:"itemId"`
	User     *TrackPreference         `json:"user"`
	Item     *TrackPreference         `json:"item"`
	Versions []VersionTrackPreference `json:"versions"`
}
