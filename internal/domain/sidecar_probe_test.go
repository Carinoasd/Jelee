package domain

import (
	"bytes"
	"testing"
)

func TestSniffSubtitleSub(t *testing.T) {
	utf16le := func(text string) []byte {
		out := []byte{0xFF, 0xFE}
		for _, r := range text {
			out = append(out, byte(r), byte(r>>8))
		}
		return out
	}
	utf16be := func(text string) []byte {
		out := []byte{0xFE, 0xFF}
		for _, r := range text {
			out = append(out, byte(r>>8), byte(r))
		}
		return out
	}
	cases := map[string]struct {
		head []byte
		want string
	}{
		"vobsub pack header":     {[]byte{0, 0, 1, 0xBA, 0x44, 0, 4, 0, 4, 1}, SidecarSubtitleVobSub},
		"microdvd":               {[]byte("{0}{25}Hello|World\n{26}{50}Bye\n"), SidecarSubtitleMicroDVD},
		"microdvd fps line":      {[]byte("{1}{1}23.976\r\n{0}{25}Hi\r\n"), SidecarSubtitleMicroDVD},
		"microdvd open end":      {[]byte("{100}{}Text"), SidecarSubtitleMicroDVD},
		"microdvd utf8 bom":      {append([]byte{0xEF, 0xBB, 0xBF}, "{0}{25}Hi"...), SidecarSubtitleMicroDVD},
		"microdvd blank lead":    {[]byte("\r\n \n\t{0}{25}Hi"), SidecarSubtitleMicroDVD},
		"microdvd utf16le":       {utf16le("{0}{25}字幕\n"), SidecarSubtitleMicroDVD},
		"microdvd utf16be":       {utf16be("{0}{25}字幕\n"), SidecarSubtitleMicroDVD},
		"srt content":            {[]byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), ""},
		"subviewer":              {[]byte("[INFORMATION]\n[TITLE]x\n"), ""},
		"brace without frames":   {[]byte("{a}{b}text"), ""},
		"second line not looked": {[]byte("garbage\n{0}{25}Hi"), ""},
		"mpeg system header":     {[]byte{0, 0, 1, 0xBB}, ""},
		"truncated pack":         {[]byte{0, 0, 1}, ""},
		"empty":                  {nil, ""},
		"utf16 wide brace":       {[]byte{0xFF, 0xFE, '{', 0x01, '0', 0, '}', 0, '{', 0, '1', 0, '}', 0}, ""},
	}
	for name, tc := range cases {
		if got := SniffSubtitleSub(tc.head); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
	// Only the first SidecarSniffBytes count.
	late := append(bytes.Repeat([]byte("\n"), SidecarSniffBytes), "{0}{25}Hi"...)
	if got := SniffSubtitleSub(late); got != "" {
		t.Fatalf("content past the sniff window decided the kind: %q", got)
	}
}

func FuzzSniffSubtitleSub(f *testing.F) {
	f.Add([]byte{0, 0, 1, 0xBA})
	f.Add([]byte("{0}{25}Hi"))
	f.Add([]byte{0xFF, 0xFE, '{', 0})
	f.Add([]byte{0xFE, 0xFF, 0, '{', 0})
	f.Fuzz(func(t *testing.T, head []byte) {
		got := SniffSubtitleSub(head)
		switch got {
		case "", SidecarSubtitleMicroDVD, SidecarSubtitleVobSub:
		default:
			t.Fatalf("unknown kind %q", got)
		}
		if got == SidecarSubtitleVobSub && !bytes.HasPrefix(head, []byte{0, 0, 1, 0xBA}) {
			t.Fatal("vobsub without a pack header")
		}
		if got == SidecarSubtitleMicroDVD && bytes.IndexByte(head, '{') < 0 && !bytes.Contains(head, []byte{'{', 0}) {
			t.Fatal("microdvd without a frame marker")
		}
	})
}

func TestSidecarProbeScopeAndInitialState(t *testing.T) {
	cases := []struct {
		kind, format, want string
	}{
		{SidecarKindAudio, "mka", SidecarProbePending},
		{SidecarKindAudio, "dts", SidecarProbePending},
		{SidecarKindSubtitle, "sub", SidecarProbePending},
		{SidecarKindSubtitle, "sup", SidecarProbePending},
		{SidecarKindSubtitle, "idx", SidecarProbeNotApplicable},
		{SidecarKindSubtitle, "srt", SidecarProbeNotApplicable},
		{SidecarKindSubtitle, "ass", SidecarProbeNotApplicable},
	}
	for _, tc := range cases {
		if got := InitialSidecarProbeState(tc.kind, tc.format); got != tc.want {
			t.Errorf("%s/%s: %s want %s", tc.kind, tc.format, got, tc.want)
		}
	}
}

func TestSidecarProbeFactsFrom(t *testing.T) {
	text := func(v string) *string { return &v }
	number := func(v int64) *int64 { return &v }
	yes := true
	meta := MediaMetadata{Format: MediaFormat{DurationMicros: number(90_000_000), BitRate: number(640_000)}, Streams: []MediaStream{
		{Index: 0, Kind: "video", Codec: text("mjpeg")},
		{Index: 1, Kind: "audio", Codec: text("ac3"), Audio: &MediaAudio{Channels: number(2)}},
		{Index: 2, Kind: "audio", Codec: text("truehd"), Profile: text("Dolby TrueHD + Dolby Atmos"), Default: &yes, DurationMicros: number(80_000_000),
			Audio: &MediaAudio{Channels: number(8), ChannelLayout: text("7.1"), SampleRate: number(48000)}},
	}}
	facts, ok := SidecarProbeFactsFrom(SidecarKindAudio, meta)
	want := SidecarProbeFacts{Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8, ChannelLayout: "7.1", SampleRate: 48000, BitRate: 640_000, DurationMicros: 80_000_000}
	if !ok || facts != want {
		t.Fatalf("default audio stream not chosen: %+v", facts)
	}
	if !SidecarAtmos(facts.Codec, facts.Profile) || SidecarAtmos("ac3", facts.Profile) {
		t.Fatal("atmos rule differs from the embedded one")
	}
	meta.Streams[2].Default = nil
	if facts, _ := SidecarProbeFactsFrom(SidecarKindAudio, meta); facts.Codec != "ac3" || facts.Channels != 2 || facts.DurationMicros != 90_000_000 {
		t.Fatalf("first audio stream not chosen: %+v", facts)
	}
	if _, ok := SidecarProbeFactsFrom(SidecarKindSubtitle, meta); ok {
		t.Fatal("subtitle facts from a file without a subtitle stream")
	}
	sub := MediaMetadata{Streams: []MediaStream{{Index: 0, Kind: "subtitle", Codec: text("hdmv_pgs_subtitle"), DurationMicros: number(5),
		Profile: text("x"), Audio: &MediaAudio{Channels: number(2)}}}}
	if facts, ok := SidecarProbeFactsFrom(SidecarKindSubtitle, sub); !ok || facts != (SidecarProbeFacts{Codec: "hdmv_pgs_subtitle", DurationMicros: 5}) {
		t.Fatalf("subtitle facts: %+v", facts)
	}
	hostile := MediaMetadata{Streams: []MediaStream{{Index: 0, Kind: "audio", Codec: text("Bad Codec"), Profile: text("bad\x00profile"),
		Audio: &MediaAudio{Channels: number(999), SampleRate: number(-1), ChannelLayout: text("5.1 ; drop")}}}}
	facts, ok = SidecarProbeFactsFrom(SidecarKindAudio, hostile)
	if !ok || facts != (SidecarProbeFacts{}) {
		t.Fatalf("hostile values kept: %+v", facts)
	}
	if !ValidSidecarProbeResult(SidecarProbeResult{ID: "00000000-0000-4000-8000-000000000001", State: SidecarProbeReady, Facts: facts}) {
		t.Fatal("sanitized facts rejected")
	}
}

func TestValidSidecarProbeResult(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	valid := []SidecarProbeResult{
		{ID: id, State: SidecarProbeFailed},
		{ID: id, State: SidecarProbeReady},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{Codec: "dts", Profile: "DTS-HD MA", Channels: 8, ChannelLayout: "7.1", SampleRate: 48000, BitRate: 1, DurationMicros: 1}},
	}
	for i, v := range valid {
		if !ValidSidecarProbeResult(v) {
			t.Errorf("valid %d rejected", i)
		}
	}
	invalid := []SidecarProbeResult{
		{ID: "x", State: SidecarProbeReady},
		{ID: id, State: SidecarProbePending},
		{ID: id, State: SidecarProbeNotApplicable},
		{ID: id, State: SidecarProbeFailed, Facts: SidecarProbeFacts{Codec: "dts"}},
		{ID: id, State: SidecarProbeReady, Size: -1},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{Codec: "DTS"}},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{Channels: 257}},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{SampleRate: 768001}},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{ChannelLayout: "5.1 side"}},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{Profile: "a\nb"}},
		{ID: id, State: SidecarProbeReady, Facts: SidecarProbeFacts{BitRate: -1}},
	}
	for i, v := range invalid {
		if ValidSidecarProbeResult(v) {
			t.Errorf("invalid %d accepted", i)
		}
	}
	if !ValidSidecarInspection(SidecarInspection{ID: id, Fingerprint: make([]byte, 32), SubtitleKind: SidecarSubtitleVobSub}) ||
		ValidSidecarInspection(SidecarInspection{ID: id, Fingerprint: make([]byte, 32), SubtitleKind: "pgs"}) {
		t.Fatal("inspection subtitle kind check")
	}
}

func TestPlaybackExternalTracksProbeAndPairing(t *testing.T) {
	ids := []string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000a3",
		"00000000-0000-4000-8000-0000000000a4", "00000000-0000-4000-8000-0000000000a5", "00000000-0000-4000-8000-0000000000a6",
		"00000000-0000-4000-8000-0000000000a7", "00000000-0000-4000-8000-0000000000a8"}
	record := func(id, kind, format string, probe SidecarProbeRecord) SidecarTrackRecord {
		return SidecarTrackRecord{ID: id, Track: SidecarTrack{Kind: kind, Format: format}, Probe: probe, Size: 10}
	}
	idx := record(ids[0], SidecarKindSubtitle, "idx", SidecarProbeRecord{State: SidecarProbeNotApplicable})
	idx.PairedTrackID = ids[1]
	pairedSub := record(ids[1], SidecarKindSubtitle, "sub", SidecarProbeRecord{State: SidecarProbePending})
	pairedSub.SubtitleKind = SidecarSubtitleVobSub
	microdvd := record(ids[2], SidecarKindSubtitle, "sub", SidecarProbeRecord{State: SidecarProbePending})
	microdvd.SubtitleKind = SidecarSubtitleMicroDVD
	mka := record(ids[3], SidecarKindAudio, "mka", SidecarProbeRecord{State: SidecarProbeReady, Facts: SidecarProbeFacts{Codec: "eac3",
		Profile: "Dolby Digital Plus + Dolby Atmos", Channels: 6, ChannelLayout: "5.1(side)", SampleRate: 48000, BitRate: 768000, DurationMicros: 7}})
	pendingOgg := record(ids[4], SidecarKindAudio, "ogg", SidecarProbeRecord{State: SidecarProbePending})
	failedDTS := record(ids[5], SidecarKindAudio, "dts", SidecarProbeRecord{State: SidecarProbeFailed})
	mislabeled := record(ids[6], SidecarKindAudio, "dts", SidecarProbeRecord{State: SidecarProbeReady, Facts: SidecarProbeFacts{Codec: "ac3"}})
	sup := record(ids[7], SidecarKindSubtitle, "sup", SidecarProbeRecord{State: SidecarProbeReady, Facts: SidecarProbeFacts{Codec: "hdmv_pgs_subtitle", DurationMicros: 9}})
	legacy := SidecarTrackRecord{ID: ids[2], Track: SidecarTrack{Kind: SidecarKindAudio, Format: "flac"}}

	source := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", Sidecars: []SidecarTrackRecord{
		idx, pairedSub, microdvd, mka, pendingOgg, failedDTS, mislabeled, sup}})
	if len(source.External) != 7 {
		t.Fatalf("the paired .sub data half must be hidden: %d tracks", len(source.External))
	}
	byID := map[string]PlaybackExternalTrack{}
	for _, track := range source.External {
		byID[track.ID] = track
	}
	if _, listed := byID[ids[1]]; listed || byID[ids[0]].PairedTrackID != ids[1] || byID[ids[0]].Codec != "vobsub" {
		t.Fatalf("idx pair: %+v", byID[ids[0]])
	}
	if got := byID[ids[2]]; got.Codec != "microdvd" || got.ProbeState != SidecarProbePending {
		t.Fatalf("microdvd .sub: %+v", got)
	}
	if got := byID[ids[3]]; got.Codec != "eac3" || !got.Atmos || got.Channels != 6 || got.ChannelLayout != "5.1(side)" || got.SampleRate != 48000 ||
		got.BitRate != 768000 || got.DurationMicros != 7 || got.ProbeState != SidecarProbeReady {
		t.Fatalf("probed mka: %+v", got)
	}
	if got := byID[ids[4]]; got.Codec != "" || got.ProbeState != SidecarProbePending {
		t.Fatalf("pending ogg: %+v", got)
	}
	if got := byID[ids[5]]; got.Codec != "dts" || got.ProbeState != SidecarProbeFailed || got.Channels != 0 {
		t.Fatalf("failed dts keeps the extension codec and is still listed: %+v", got)
	}
	if got := byID[ids[6]]; got.Codec != "ac3" {
		t.Fatalf("probed codec must win over the extension: %+v", got)
	}
	if got := byID[ids[7]]; got.Codec != "pgs" || got.DurationMicros != 9 || got.Channels != 0 {
		t.Fatalf("probed sup: %+v", got)
	}
	decision := CheckPlayback(ClientCapabilities{AudioCodecs: []string{"eac3", "ac3"}, SubtitleFormats: []string{"microdvd"}}, source)
	reasons := map[string]string{}
	for _, track := range decision.Tracks {
		if track.External {
			reasons[track.ID] = track.Reason
		}
	}
	if reasons[ids[3]] != "" || reasons[ids[4]] != PlaybackReasonTrackNotProbed || reasons[ids[6]] != "" || reasons[ids[2]] != "" ||
		reasons[ids[5]] != PlaybackReasonAudioCodec {
		t.Fatalf("decisions: %v", reasons)
	}
	if got := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", Sidecars: []SidecarTrackRecord{legacy}}).External[0]; got.ProbeState != SidecarProbePending || got.Codec != "flac" {
		t.Fatalf("record without a stored state: %+v", got)
	}
	// An .idx whose .sub is gone stays a track and names nothing.
	idx.PairedTrackID = ""
	alone := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", Sidecars: []SidecarTrackRecord{idx, pairedSub}})
	if len(alone.External) != 2 {
		t.Fatalf("unpaired .sub must stay listed: %d", len(alone.External))
	}
}
