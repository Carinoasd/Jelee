package domain

import (
	"slices"
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

// TestUpstreamExternalTracksNumbering: external subtitles follow every
// embedded stream, one slot per OCR-capable bitmap subtitle follows them,
// and external audio files come last, so adding audio never moves a
// subtitle number. A reported audio index maps back to its file.
func TestUpstreamExternalTracksNumbering(t *testing.T) {
	const audioID = "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0a1"
	source := PlaybackSource{ID: "src",
		Video:     []PlaybackVideoTrack{{Index: 0}},
		Audio:     []PlaybackAudioTrack{{Index: 1}},
		Subtitles: []PlaybackSubtitleTrack{{Index: 2, Codec: "hdmv_pgs_subtitle", OCRSource: true}, {Index: 3, Codec: "subrip"}},
		External: []PlaybackExternalTrack{
			{ID: audioID, Kind: SidecarKindAudio, Format: "mka"},
			{ID: sessionSubtitleID, Kind: SidecarKindSubtitle, Format: "srt"},
			{ID: "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0a2", Kind: SidecarKindAudio, Format: "Bad!"},
			{ID: "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0a3", Kind: SidecarKindSubtitle, Format: "ass"},
		},
	}
	got := map[string]int{}
	for _, e := range UpstreamExternalTracks(source) {
		got[e.Track.ID] = e.Index
	}
	want := map[string]int{sessionSubtitleID: 4, "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0a3": 5, audioID: 7}
	if len(got) != len(want) {
		t.Fatalf("numbered %v, want %v", got, want)
	}
	for id, index := range want {
		if got[id] != index {
			t.Fatalf("numbered %v, want %v", got, want)
		}
	}
	if subs := UpstreamExternalSubtitles(source); len(subs) != 2 || subs[0].Index != 4 || subs[1].Index != 5 {
		t.Fatalf("subtitles %+v", subs)
	}
	if audio := UpstreamExternalAudio(source); len(audio) != 1 || audio[0].Index != 7 || audio[0].Track.ID != audioID {
		t.Fatalf("audio %+v", audio)
	}
	for _, c := range []struct {
		index int
		want  SessionTracks
		ok    bool
	}{
		{7, SessionTracks{SourceID: "src", AudioTrack: "x:" + audioID}, true},
		{1, SessionTracks{SourceID: "src", AudioTrack: "e:1"}, true},
		// The OCR slot and an external subtitle are not audio.
		{6, SessionTracks{SourceID: "src"}, false},
		{4, SessionTracks{SourceID: "src"}, false},
	} {
		index := c.index
		if got, ok := ResolveTrackReport(source, PlaybackTrackReport{AudioIndex: &index}); got != c.want || ok != c.ok {
			t.Fatalf("audio index %d: %+v %v, want %+v %v", c.index, got, ok, c.want, c.ok)
		}
	}
}

// TestUpstreamOCRSlotsNumberExternalBitmapsAfterEmbedded: external .sup
// files and paired .idx files get OCR slots after the embedded bitmap
// tracks, so every subtitle number of a source without them stays as it
// was; only the external audio after them moves.
func TestUpstreamOCRSlotsNumberExternalBitmapsAfterEmbedded(t *testing.T) {
	const (
		sup, idx, sub = "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0b1", "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0b2", "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0b3"
		lone, audio   = "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0b4", "0b4ad0fc-4a6f-4e0e-9f43-7d2f7dc8a0b5"
	)
	base := PlaybackSource{ID: "src",
		Video:     []PlaybackVideoTrack{{Index: 0}},
		Subtitles: []PlaybackSubtitleTrack{{Index: 1, Codec: "hdmv_pgs_subtitle", OCRSource: true}},
		External: []PlaybackExternalTrack{
			{ID: sessionSubtitleID, Kind: SidecarKindSubtitle, Format: "srt"},
			{ID: audio, Kind: SidecarKindAudio, Format: "mka"},
		},
	}
	before := UpstreamOCRSlots(base)
	if len(before) != 1 || before[0].Index != 3 || before[0].Embedded == nil || before[0].Embedded.Index != 1 {
		t.Fatalf("slots %+v", before)
	}
	if a := UpstreamExternalAudio(base); len(a) != 1 || a[0].Index != 4 {
		t.Fatalf("audio %+v", a)
	}
	withBitmaps := base
	withBitmaps.External = append(slices.Clone(base.External),
		PlaybackExternalTrack{ID: sup, Kind: SidecarKindSubtitle, Format: "sup", OCRSource: true},
		PlaybackExternalTrack{ID: idx, Kind: SidecarKindSubtitle, Format: "idx", PairedTrackID: sub, OCRSource: true},
		PlaybackExternalTrack{ID: lone, Kind: SidecarKindSubtitle, Format: "idx"},
	)
	// Every external subtitle file keeps its own slot; the embedded OCR
	// slot moves only by the external subtitle files, as before.
	subs := UpstreamExternalSubtitles(withBitmaps)
	if len(subs) != 4 || subs[0].Index != 2 || subs[1].Track.ID != sup || subs[1].Index != 3 || subs[3].Index != 5 {
		t.Fatalf("subtitles %+v", subs)
	}
	slots := UpstreamOCRSlots(withBitmaps)
	if len(slots) != 3 || slots[0].Embedded == nil || slots[0].Index != 6 || slots[1].External == nil || slots[1].External.ID != sup || slots[1].Index != 7 ||
		slots[2].External == nil || slots[2].External.ID != idx || slots[2].Index != 8 {
		t.Fatalf("slots %+v", slots)
	}
	if a := UpstreamExternalAudio(withBitmaps); len(a) != 1 || a[0].Index != 9 {
		t.Fatalf("audio %+v", a)
	}
}

func TestExternalOCRSource(t *testing.T) {
	for _, c := range []struct {
		kind, format, paired string
		want                 bool
	}{
		{SidecarKindSubtitle, "sup", "", true},
		{SidecarKindSubtitle, "idx", "pair", true},
		{SidecarKindSubtitle, "idx", "", false},
		{SidecarKindSubtitle, "sub", "", false},
		{SidecarKindSubtitle, "srt", "", false},
		{SidecarKindAudio, "sup", "", false},
	} {
		if got := ExternalOCRSource(c.kind, c.format, c.paired); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
	tracks := playbackExternalTracks([]SidecarTrackRecord{
		{ID: "idx", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "idx"}, PairedTrackID: "sub"},
		{ID: "sub", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "sub"}, SubtitleKind: SidecarSubtitleVobSub},
		{ID: "sup", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "sup", Title: "Commentary"}},
	})
	if len(tracks) != 2 || !tracks[0].OCRSource || !tracks[1].OCRSource {
		t.Fatalf("%+v", tracks)
	}
	if OCRExternalTrackTitle(tracks[1]) != "Commentary (OCR)" || OCRExternalTrackTitle(PlaybackExternalTrack{Language: "chi"}) != "chi (OCR)" || OCRExternalTrackTitle(PlaybackExternalTrack{}) != "OCR" {
		t.Fatal("title")
	}
}
