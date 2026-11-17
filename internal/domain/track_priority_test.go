package domain

import "testing"

func xid(n string) string { return "e0000000-0000-4000-8000-00000000000" + n }

// TestTrackPriorityLadder is the written-down order in which SelectDefaultTracks
// weighs candidates when the external and embedded tracks compete (G15, G16;
// stage 10 design point 5; W10-14). Each case isolates one rung: the stronger
// rule must win even when the weaker one points the other way.
//
// Subtitles, strongest first: a track the preference names; the language
// match (exact tag, then same script, then same language); not-SDH unless
// SDH is asked for; the file's default flag; an external file over an
// embedded stream; the listed order (embedded streams as the probe lists them, then external files by path).
// Forced tracks never compete as a full subtitle.
//
// Audio, strongest first: a named track; a commentary track when one is asked
// for; the language; commentary avoided otherwise; the default flag; then,
// unlike subtitles, the embedded stream before an external file; the listed
// order. The stage 10 design recommended external-first for audio too; the
// implemented order keeps the container's own audio ahead because an
// external audio file is delivered without sync tools (recorded in
// docs/fixtures.md as a deviation).
func TestTrackPriorityLadder(t *testing.T) {
	always := func(language string) TrackPreference {
		return TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr(language)}
	}
	subtitleCase := func(name string, embedded []PlaybackSubtitleTrack, external []PlaybackExternalTrack, p TrackPreference, want string) {
		t.Helper()
		source := PlaybackSource{Audio: []PlaybackAudioTrack{{Index: 1, Language: "eng"}}, Subtitles: embedded, External: external}
		if got := describeSelection(SelectDefaultTracks(source, p, TrackBasisUser).Subtitle); got != want {
			t.Errorf("subtitle %s: %s, want %s", name, got, want)
		}
	}
	sub := func(n string, o PlaybackExternalTrack) PlaybackExternalTrack {
		o.ID, o.Kind = xid(n), SidecarKindSubtitle
		return o
	}
	subtitleCase("named track beats everything", []PlaybackSubtitleTrack{{Index: 2, Language: "jpn"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en"})},
		TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("en"), SubtitleTrack: ptr("e:2")}, "e:2")
	subtitleCase("language beats external", []PlaybackSubtitleTrack{{Index: 2, Language: "eng"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "ja"})}, always("en"), "e:2")
	subtitleCase("exact tag beats same language", []PlaybackSubtitleTrack{{Index: 2, Language: "zh-TW"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "zh-CN"})}, always("zh-TW"), "e:2")
	subtitleCase("same script beats same language", []PlaybackSubtitleTrack{{Index: 2, Language: "zh-CN"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "zh-HK"})}, always("zh-TW"), "x:1")
	subtitleCase("non-SDH beats external", []PlaybackSubtitleTrack{{Index: 2, Language: "eng"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en", SDH: true})}, always("en"), "e:2")
	subtitleCase("SDH wanted beats non-SDH external", []PlaybackSubtitleTrack{{Index: 2, Language: "eng", SDH: true}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en"})},
		TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("en"), SubtitleSDH: ptr(true)}, "e:2")
	subtitleCase("default flag beats external", []PlaybackSubtitleTrack{{Index: 2, Language: "eng", Default: true}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en"})}, always("en"), "e:2")
	subtitleCase("external beats embedded on equal terms", []PlaybackSubtitleTrack{{Index: 2, Language: "eng"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en"})}, always("en"), "x:1")
	subtitleCase("external default beats embedded default", []PlaybackSubtitleTrack{{Index: 2, Language: "eng", Default: true}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en", Default: true})}, always("en"), "x:1")
	subtitleCase("listed order decides among embedded streams", []PlaybackSubtitleTrack{{Index: 4, Language: "eng"}, {Index: 2, Language: "eng"}}, nil, always("en"), "e:4")
	subtitleCase("first listed external first", nil,
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en"}), sub("2", PlaybackExternalTrack{Language: "en"})}, always("en"), "x:1")
	subtitleCase("forced never competes as a full subtitle", []PlaybackSubtitleTrack{{Index: 2, Language: "eng"}},
		[]PlaybackExternalTrack{sub("1", PlaybackExternalTrack{Language: "en", Forced: true, Default: true})}, always("en"), "e:2")
	subtitleCase("off wins over a named track", []PlaybackSubtitleTrack{{Index: 2, Language: "eng"}}, nil,
		TrackPreference{SubtitleMode: ptr(SubtitleModeOff), SubtitleTrack: ptr("e:2")}, "none")

	audioCase := func(name string, embedded []PlaybackAudioTrack, external []PlaybackExternalTrack, p TrackPreference, want string) {
		t.Helper()
		source := PlaybackSource{Audio: embedded, External: external}
		if got := describeSelection(SelectDefaultTracks(source, p, TrackBasisUser).Audio); got != want {
			t.Errorf("audio %s: %s, want %s", name, got, want)
		}
	}
	aud := func(n string, o PlaybackExternalTrack) PlaybackExternalTrack {
		o.ID, o.Kind = xid(n), SidecarKindAudio
		return o
	}
	audioCase("named track beats everything", []PlaybackAudioTrack{{Index: 1, Language: "eng", Default: true}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en"})}, TrackPreference{AudioTrack: ptr("x:" + xid("1"))}, "x:1")
	audioCase("language beats embedded", []PlaybackAudioTrack{{Index: 1, Language: "eng"}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "ja"})}, TrackPreference{AudioLanguage: ptr("ja")}, "x:1")
	audioCase("commentary asked for", []PlaybackAudioTrack{{Index: 1, Language: "eng"}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en", Commentary: true})}, TrackPreference{AudioLanguage: ptr("en"), AudioCommentary: ptr(true)}, "x:1")
	audioCase("commentary avoided otherwise", []PlaybackAudioTrack{{Index: 1, Language: "eng", Commentary: true}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en"})}, TrackPreference{AudioLanguage: ptr("en")}, "x:1")
	audioCase("default flag beats embedded order", []PlaybackAudioTrack{{Index: 1, Language: "eng"}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en", Default: true})}, TrackPreference{AudioLanguage: ptr("en")}, "x:1")
	audioCase("embedded beats external on equal terms", []PlaybackAudioTrack{{Index: 1, Language: "eng"}},
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en"})}, TrackPreference{AudioLanguage: ptr("en")}, "e:1")
	audioCase("first listed external first", nil,
		[]PlaybackExternalTrack{aud("1", PlaybackExternalTrack{Language: "en"}), aud("2", PlaybackExternalTrack{Language: "en"})}, TrackPreference{AudioLanguage: ptr("en")}, "x:1")
}
