package domain

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

// trackSource has embedded English (default) and Japanese audio, a forced
// English PGS and a German subtitle; external English SDH, Chinese
// (Traditional), forced Japanese subtitles and a commentary track.
func trackSource() PlaybackSource {
	return PlaybackSource{
		ID: "10000000-0000-4000-8000-000000000001",
		Audio: []PlaybackAudioTrack{
			{Index: 1, Language: "eng", Default: true},
			{Index: 2, Language: "jpn"},
		},
		Subtitles: []PlaybackSubtitleTrack{
			{Index: 3, Language: "eng", Forced: true},
			{Index: 4, Language: "ger"},
		},
		External: []PlaybackExternalTrack{
			{ID: "e0000000-0000-4000-8000-000000000001", Kind: SidecarKindSubtitle, Language: "en", SDH: true},
			{ID: "e0000000-0000-4000-8000-000000000002", Kind: SidecarKindSubtitle, Language: "zh-Hant"},
			{ID: "e0000000-0000-4000-8000-000000000003", Kind: SidecarKindSubtitle, Language: "ja", Forced: true},
			{ID: "e0000000-0000-4000-8000-000000000004", Kind: SidecarKindAudio, Language: "en", Commentary: true},
			{ID: "e0000000-0000-4000-8000-000000000005", Kind: SidecarKindSubtitle, Languages: []string{"en", "ja"}},
		},
	}
}

func describeSelection(sel *TrackSelection) string {
	switch {
	case sel == nil:
		return "none"
	case sel.Kind == TrackEmbedded:
		return "e:" + string(rune('0'+*sel.Index))
	}
	return "x:" + sel.ID[len(sel.ID)-1:]
}

func TestSelectDefaultTracks(t *testing.T) {
	cases := []struct {
		name            string
		p               TrackPreference
		audio, subtitle string
	}{
		{"file defaults", TrackPreference{}, "e:1", "e:3"},
		{"audio language by ISO 639-2", TrackPreference{AudioLanguage: ptr("ja")}, "e:2", "x:3"},
		{"commentary on request", TrackPreference{AudioCommentary: ptr(true), AudioLanguage: ptr("ja")}, "x:4", "e:3"},
		{"named tracks", TrackPreference{AudioTrack: ptr("e:2"), SubtitleTrack: ptr("e:4")}, "e:2", "e:4"},
		{"unknown named track falls back", TrackPreference{AudioTrack: ptr("e:9"), SubtitleTrack: ptr("x:e0000000-0000-4000-8000-000000000099")}, "e:1", "e:3"},
		{"off wins over a named subtitle", TrackPreference{SubtitleMode: ptr(SubtitleModeOff), SubtitleTrack: ptr("e:4")}, "e:1", "none"},
		{"always, region implies script", TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("zh-TW")}, "e:1", "x:2"},
		{"always prefers SDH when asked", TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("en"), SubtitleSDH: ptr(true)}, "e:1", "x:1"},
		{"always avoids SDH otherwise", TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("en")}, "e:1", "x:5"},
		{"always without language", TrackPreference{SubtitleMode: ptr(SubtitleModeAlways)}, "e:1", "e:4"},
		{"always with no match", TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("ko")}, "e:1", "none"},
		{"auto, foreign audio", TrackPreference{AudioLanguage: ptr("ja"), SubtitleLanguage: ptr("de")}, "e:2", "e:4"},
		{"auto, same language", TrackPreference{SubtitleLanguage: ptr("en")}, "e:1", "e:3"},
		{"forced follows the audio", TrackPreference{AudioLanguage: ptr("ja"), SubtitleMode: ptr(SubtitleModeForced)}, "e:2", "x:3"},
	}
	for _, c := range cases {
		got := SelectDefaultTracks(trackSource(), c.p, TrackBasisItem)
		if a, s := describeSelection(got.Audio), describeSelection(got.Subtitle); a != c.audio || s != c.subtitle || got.Basis != TrackBasisItem {
			t.Errorf("%s: audio %s subtitle %s, want %s %s", c.name, a, s, c.audio, c.subtitle)
		}
	}
	// A source without tracks picks nothing.
	if got := SelectDefaultTracks(PlaybackSource{}, TrackPreference{AudioLanguage: ptr("en")}, TrackBasisSource); got.Audio != nil || got.Subtitle != nil {
		t.Fatal("empty source", got)
	}
}

func TestTrackPreferenceLevelsAndValidation(t *testing.T) {
	source := "10000000-0000-4000-8000-000000000001"
	set := TrackPreferenceSet{
		User:     &TrackPreference{AudioLanguage: ptr("en"), SubtitleLanguage: ptr("de")},
		Item:     &TrackPreference{AudioLanguage: ptr("ja")},
		Versions: map[string]TrackPreference{source: {SubtitleMode: ptr(SubtitleModeOff)}},
		Locale:   "zh-TW",
	}
	p, basis := set.Effective(source)
	if basis != TrackBasisVersion || *p.AudioLanguage != "ja" || *p.SubtitleLanguage != "de" || *p.SubtitleMode != SubtitleModeOff {
		t.Fatalf("version merge: %+v %s", p, basis)
	}
	if p, basis = set.Effective("other"); basis != TrackBasisItem || p.SubtitleMode != nil {
		t.Fatalf("item merge: %+v %s", p, basis)
	}
	if p, basis = (TrackPreferenceSet{Locale: "zh-TW"}).Effective(source); basis != TrackBasisLocale || *p.SubtitleLanguage != "zh-TW" {
		t.Fatalf("locale: %+v %s", p, basis)
	}
	if _, basis = (TrackPreferenceSet{Locale: "??"}).Effective(source); basis != TrackBasisSource {
		t.Fatal("no preference basis", basis)
	}
	if _, basis = (TrackPreferenceSet{Item: &TrackPreference{}, Locale: "en-US"}).Effective(source); basis != TrackBasisLocale {
		t.Fatal("an empty level counts", basis)
	}

	normalized, err := NormalizeTrackPreference(TrackPreference{AudioLanguage: ptr("jpn"), SubtitleLanguage: ptr("chs"), AudioTrack: ptr("e:0"), SubtitleTrack: ptr("x:e0000000-0000-4000-8000-000000000001")}, true)
	if err != nil || *normalized.AudioLanguage != "ja" || *normalized.SubtitleLanguage != "zh-Hans" {
		t.Fatalf("normalize: %+v %v", normalized, err)
	}
	for name, c := range map[string]struct {
		p       TrackPreference
		version bool
	}{
		"track on item":    {TrackPreference{AudioTrack: ptr("e:1")}, false},
		"bad track":        {TrackPreference{SubtitleTrack: ptr("e:01")}, true},
		"upper case track": {TrackPreference{SubtitleTrack: ptr("x:E0000000-0000-4000-8000-000000000001")}, true},
		"bad mode":         {TrackPreference{SubtitleMode: ptr("sometimes")}, false},
		"bad language":     {TrackPreference{AudioLanguage: ptr("english!")}, false},
		"empty language":   {TrackPreference{SubtitleLanguage: ptr("")}, false},
	} {
		if _, err := NormalizeTrackPreference(c.p, c.version); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted", name)
		}
	}
	for ref, ok := range map[string]bool{"e:0": true, "e:99999": true, "e:100000": false, "e:": false, "x:nope": false, "y:1": false, "e1": false} {
		if ValidTrackRef(ref) != ok {
			t.Errorf("ValidTrackRef(%q) != %t", ref, ok)
		}
	}
	if !(TrackPreference{}).Empty() || (TrackPreference{SubtitleSDH: ptr(false)}).Empty() {
		t.Fatal("Empty")
	}
}

func TestItemVersionDomainRules(t *testing.T) {
	conflicts := ExternalIDConflicts([]byte(`[{"type":"TMDB","value":"949"},{"type":"imdb","value":"tt1"},{"type":"tvdb","value":" 5 "}]`),
		[]byte(`[{"type":"tmdb","value":"949"},{"type":"IMDB","value":"tt2"},{"type":"tvdb","value":"5"},{"type":"anidb","value":"7"}]`))
	if !slices.Equal(conflicts, []string{"imdb"}) {
		t.Fatal("conflicts", conflicts)
	}
	if len(ExternalIDConflicts(nil, []byte(`[{"type":"tmdb","value":"1"}]`))) != 0 || len(ExternalIDConflicts([]byte(`{bad`), []byte(`[]`))) != 0 ||
		len(ExternalIDConflicts([]byte(`[{"type":"","value":"1"},{"type":"tmdb","value":" "}]`), []byte(`[{"type":"tmdb","value":"2"}]`))) != 0 {
		t.Fatal("documents without usable IDs conflict")
	}
	now := time.Now()
	op := VersionOperation{Kind: VersionOpMerge, UndoUntil: now.Add(time.Hour)}
	for name, c := range map[string]struct {
		op     VersionOperation
		later  bool
		ok     bool
		reason string
	}{
		"open":            {op, false, true, ""},
		"later":           {op, true, false, VersionUndoBlockedLater},
		"expired":         {VersionOperation{Kind: VersionOpSplit, UndoUntil: now}, false, false, VersionUndoBlockedExpired},
		"undone":          {VersionOperation{Kind: VersionOpSplit, UndoUntil: now.Add(time.Hour), UndoneAt: &now}, false, false, VersionUndoBlockedUndone},
		"primary ignores": {VersionOperation{Kind: VersionOpPrimary, UndoUntil: now.Add(time.Hour)}, true, true, ""},
	} {
		if ok, reason := VersionUndoState(c.op, now, c.later); ok != c.ok || reason != c.reason {
			t.Errorf("%s: %t %q", name, ok, reason)
		}
	}
	valid := SplitVersionInput{SourceID: "10000000-0000-4000-8000-000000000001"}
	if !valid.Valid() || (SplitVersionInput{SourceID: "x"}).Valid() || (SplitVersionInput{SourceID: valid.SourceID, Title: "  "}).Valid() ||
		(SplitVersionInput{SourceID: valid.SourceID, Title: string([]byte{0xff})}).Valid() {
		t.Fatal("split input validation")
	}
	if !VersionMergeKind("Episode") || VersionMergeKind("Season") {
		t.Fatal("merge kinds")
	}
}
