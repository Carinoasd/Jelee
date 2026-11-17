package domain

import (
	"strconv"
	"testing"
)

const sessionSubtitleID = "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a001"

func sessionSource() PlaybackSource {
	s := trackSource()
	s.ID = "src"
	s.External = append(s.External, PlaybackExternalTrack{ID: sessionSubtitleID, Kind: SidecarKindSubtitle, Format: "ass", Language: "zh-Hant"},
		PlaybackExternalTrack{ID: "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a002", Kind: SidecarKindSubtitle, Format: "Bad!"})
	return s
}

func TestResolveTrackReport(t *testing.T) {
	source := sessionSource()
	external := UpstreamExternalSubtitles(source)
	var upstream int
	found := false
	for _, e := range external {
		if e.Track.ID == sessionSubtitleID {
			upstream, found = e.Index, true
		}
		if e.Track.Format == "Bad!" {
			t.Fatal("a format outside a route token was numbered")
		}
	}
	if !found {
		t.Fatal("external subtitle not numbered", external)
	}
	audio := source.Audio[len(source.Audio)-1].Index
	embeddedSub := source.Subtitles[0].Index
	cases := []struct {
		name string
		r    PlaybackTrackReport
		want SessionTracks
		ok   bool
	}{
		{"upstream indexes", PlaybackTrackReport{AudioIndex: &audio, SubtitleIndex: &embeddedSub}, SessionTracks{SourceID: "src", AudioTrack: refOf(audio), SubtitleTrack: refOf(embeddedSub)}, true},
		{"upstream external subtitle", PlaybackTrackReport{SubtitleIndex: &upstream}, SessionTracks{SourceID: "src", SubtitleTrack: "x:" + sessionSubtitleID}, true},
		{"upstream off", PlaybackTrackReport{SubtitleIndex: ptr(-1)}, SessionTracks{SourceID: "src", SubtitleTrack: SubtitleTrackOff}, true},
		{"audio index of a subtitle", PlaybackTrackReport{AudioIndex: &embeddedSub}, SessionTracks{SourceID: "src"}, false},
		{"unknown index", PlaybackTrackReport{AudioIndex: ptr(999), SubtitleIndex: ptr(998)}, SessionTracks{SourceID: "src"}, false},
		{"native refs", PlaybackTrackReport{AudioTrack: refOf(audio), SubtitleTrack: "x:" + sessionSubtitleID}, SessionTracks{SourceID: "src", AudioTrack: refOf(audio), SubtitleTrack: "x:" + sessionSubtitleID}, true},
		{"native off", PlaybackTrackReport{SubtitleTrack: "off"}, SessionTracks{SourceID: "src", SubtitleTrack: SubtitleTrackOff}, true},
		{"native missing", PlaybackTrackReport{AudioTrack: "e:77", SubtitleTrack: "x:" + "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0ff"}, SessionTracks{SourceID: "src"}, false},
		{"native subtitle as audio", PlaybackTrackReport{AudioTrack: "x:" + sessionSubtitleID}, SessionTracks{SourceID: "src"}, false},
	}
	for _, c := range cases {
		got, ok := ResolveTrackReport(source, c.r)
		if got != c.want || ok != c.ok {
			t.Fatalf("%s: %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	invalid := []PlaybackTrackReport{{AudioIndex: ptr(-1)}, {SubtitleIndex: ptr(-2)}, {AudioIndex: ptr(100000)}, {AudioTrack: "off"}, {SubtitleTrack: "e:01"}, {AudioTrack: "x:NOPE"}}
	for _, r := range invalid {
		if r.Valid() {
			t.Fatalf("%+v valid", r)
		}
	}
	if (PlaybackTrackReport{AudioIndex: ptr(1)}).Fingerprint() == (PlaybackTrackReport{SubtitleIndex: ptr(1)}).Fingerprint() {
		t.Fatal("fingerprints collide")
	}
	if err := (PlaybackReport{Kind: PlaybackReportProgress, PlayKey: "k", Tracks: &PlaybackTrackReport{SubtitleIndex: ptr(-5)}}).Validate(); err == nil {
		t.Fatal("invalid tracks accepted in a report")
	}
}

func refOf(index int) string { return "e:" + strconv.Itoa(index) }

// TestSessionChoicePrecedence: a session choice of the same version wins
// over every stored level, never applies to another version, and a named
// subtitle is shown even when broader levels turn subtitles off.
func TestSessionChoicePrecedence(t *testing.T) {
	source := sessionSource()
	audio := source.Audio[len(source.Audio)-1].Index
	set := TrackPreferenceSet{
		User:     &TrackPreference{SubtitleMode: ptr(SubtitleModeOff), AudioLanguage: ptr("en")},
		Versions: map[string]TrackPreference{"src": {AudioTrack: ptr("e:1")}},
		Session:  &SessionTracks{SourceID: "src", AudioTrack: refOf(audio), SubtitleTrack: "x:" + sessionSubtitleID},
	}
	p, basis := set.Effective("src")
	if basis != TrackBasisSession || *p.AudioTrack != refOf(audio) || *p.SubtitleMode != SubtitleModeAlways {
		t.Fatalf("session level %+v %s", p, basis)
	}
	got := SelectDefaultTracks(source, p, basis)
	if got.Audio == nil || got.Audio.Index == nil || *got.Audio.Index != audio || got.Subtitle == nil || got.Subtitle.ID != sessionSubtitleID {
		t.Fatalf("session tracks %+v", got)
	}
	if _, basis = set.Effective("other"); basis != TrackBasisUser {
		t.Fatal("session choice applied to another version", basis)
	}
	set.Session = &SessionTracks{SourceID: "src", SubtitleTrack: SubtitleTrackOff}
	set.User = &TrackPreference{SubtitleMode: ptr(SubtitleModeAlways), SubtitleLanguage: ptr("zh-Hant")}
	p, basis = set.Effective("src")
	if basis != TrackBasisSession || *p.SubtitleMode != SubtitleModeOff || *p.AudioTrack != "e:1" {
		t.Fatalf("session off %+v %s", p, basis)
	}
	if got := SelectDefaultTracks(source, p, basis); got.Subtitle != nil {
		t.Fatal("subtitles shown after the session turned them off")
	}
}
