package domain

import (
	"maps"
	"slices"
	"testing"
)

func TestPlanSourceParts(t *testing.T) {
	c := func(id, dir, stem string, part int) SourcePartCandidate {
		return SourcePartCandidate{SourceID: id, Directory: dir, Stem: stem, Part: part}
	}
	const stem = "movie (2010) - #.mkv"
	cases := []struct {
		name     string
		in       []SourcePartCandidate
		versions [][]string
		reasons  map[string]string
	}{
		{"cd1 cd2", []SourcePartCandidate{c("b", ".", stem, 2), c("a", ".", stem, 1)}, [][]string{{"a", "b"}},
			map[string]string{"a": SourcePartMerged, "b": SourcePartMerged}},
		{"three parts in any order", []SourcePartCandidate{c("z", "m", stem, 3), c("x", "m", stem, 1), c("y", "m", stem, 2)}, [][]string{{"x", "y", "z"}},
			map[string]string{"x": SourcePartMerged, "y": SourcePartMerged, "z": SourcePartMerged}},
		{"gap", []SourcePartCandidate{c("a", ".", stem, 1), c("c", ".", stem, 3)}, nil,
			map[string]string{"a": SourcePartGap, "c": SourcePartGap}},
		{"first part missing", []SourcePartCandidate{c("b", ".", stem, 2), c("c", ".", stem, 3)}, nil,
			map[string]string{"b": SourcePartGap, "c": SourcePartGap}},
		{"single part", []SourcePartCandidate{c("a", ".", stem, 1), c("v", ".", "", 0)}, nil,
			map[string]string{"a": SourcePartSingle}},
		{"duplicate number", []SourcePartCandidate{c("a", ".", stem, 1), c("b", ".", stem, 1), c("c", ".", stem, 2)}, nil,
			map[string]string{"a": SourcePartDuplicate, "b": SourcePartDuplicate, "c": SourcePartDuplicate}},
		{"mixed release tags", []SourcePartCandidate{c("a", ".", "movie 1080p #.mkv", 1), c("b", ".", "movie 720p #.mkv", 2)}, nil,
			map[string]string{"a": SourcePartMixed, "b": SourcePartMixed}},
		{"mixed containers", []SourcePartCandidate{c("a", ".", "movie #.mkv", 1), c("b", ".", "movie #.avi", 2)}, nil,
			map[string]string{"a": SourcePartMixed, "b": SourcePartMixed}},
		{"other directory", []SourcePartCandidate{c("a", "x", stem, 1), c("b", "y", stem, 2)}, nil,
			map[string]string{"a": SourcePartSingle, "b": SourcePartSingle}},
		{"two multi-part versions beside a single file", []SourcePartCandidate{
			c("h1", ".", "movie 1080p #.mkv", 1), c("h2", ".", "movie 1080p #.mkv", 2),
			c("l1", ".", "movie 720p #.mkv", 1), c("l2", ".", "movie 720p #.mkv", 2), c("l3", ".", "movie 720p #.mkv", 3),
			c("u", ".", "", 0)}, [][]string{{"h1", "h2"}, {"l1", "l2", "l3"}},
			map[string]string{"h1": SourcePartMerged, "h2": SourcePartMerged, "l1": SourcePartMerged, "l2": SourcePartMerged, "l3": SourcePartMerged}},
		{"merged set beside a stray part", []SourcePartCandidate{c("a", ".", stem, 1), c("b", ".", stem, 2), c("s", ".", "movie extra #.mkv", 3)}, [][]string{{"a", "b"}},
			map[string]string{"a": SourcePartMerged, "b": SourcePartMerged, "s": SourcePartSingle}},
		{"out of range", []SourcePartCandidate{c("a", ".", stem, 1), c("b", ".", stem, SourcePartsMax+1)}, nil,
			map[string]string{"a": SourcePartSingle}},
		{"no markers", []SourcePartCandidate{c("a", ".", "", 0), c("b", ".", "", 0)}, nil, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanSourceParts(tc.in)
			if !slices.EqualFunc(got.Versions, tc.versions, slices.Equal[[]string]) {
				t.Fatalf("versions = %v, want %v", got.Versions, tc.versions)
			}
			if !maps.Equal(got.Reasons, tc.reasons) {
				t.Fatalf("reasons = %v, want %v", got.Reasons, tc.reasons)
			}
			// The plan depends on the set, not on the order.
			reversed := slices.Clone(tc.in)
			slices.Reverse(reversed)
			again := PlanSourceParts(reversed)
			if !slices.EqualFunc(again.Versions, got.Versions, slices.Equal[[]string]) || !maps.Equal(again.Reasons, got.Reasons) {
				t.Fatalf("order changed the plan: %v / %v", again, got)
			}
		})
	}
}

func TestBuildPlaybackSourcesFoldsParts(t *testing.T) {
	duration := func(v int64) *MediaMetadata {
		h264, aac := "h264", "aac"
		return &MediaMetadata{Format: MediaFormat{DurationMicros: &v}, Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: &h264, Video: &MediaVideo{}},
			{Index: 1, Kind: "audio", Codec: &aac},
		}}
	}
	records := []PlaybackSourceRecord{
		{ID: "p2", ContentType: "video/x-matroska", FileName: "m.cd2.mkv", Metadata: duration(40), PartOf: "p1", PartNumber: 2},
		{ID: "p1", ContentType: "video/x-matroska", FileName: "m.cd1.mkv", Metadata: duration(60), PartOf: "p1", PartNumber: 1},
		{ID: "solo", ContentType: "video/mp4", FileName: "m.mp4", Metadata: duration(100)},
		// A later part whose first part is not listed stays a version.
		{ID: "orphan", ContentType: "video/mp4", FileName: "o.cd2.mp4", PartOf: "gone", PartNumber: 2},
	}
	sources := BuildPlaybackSources(records)
	ids := []string{}
	for _, s := range sources {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{"p1", "solo", "orphan"}) {
		t.Fatalf("versions = %v", ids)
	}
	v := sources[0]
	if v.PartCount() != 2 || v.Parts[0].ID != "p1" || v.Parts[1].ID != "p2" || v.Parts[1].Number != 2 {
		t.Fatalf("parts = %+v", v.Parts)
	}
	if *v.Parts[0].StartMicros != 0 || *v.Parts[1].StartMicros != 60 || *v.TotalDurationMicros != 100 || *v.RuntimeMicros() != 100 || *v.DurationMicros != 60 {
		t.Fatalf("timeline = %v %v %v", *v.Parts[1].StartMicros, *v.TotalDurationMicros, *v.DurationMicros)
	}
	if sources[1].PartCount() != 1 || *sources[1].RuntimeMicros() != 100 {
		t.Fatal("single file runtime")
	}
	version, part, ok := FindPart(sources, "p2")
	if !ok || version.ID != "p1" || part.Number != 2 || *part.StartMicros != 60 {
		t.Fatalf("FindPart = %v %+v %v", version.ID, part, ok)
	}
	if _, part, ok = FindPart(sources, "solo"); !ok || part.Number != 1 || *part.StartMicros != 0 {
		t.Fatal("FindPart of a single file")
	}
	if _, _, ok = FindPart(sources, "missing"); ok {
		t.Fatal("FindPart found a missing source")
	}

	// An unknown duration leaves the later starts and the total unknown.
	records[1].Metadata = nil
	sources = BuildPlaybackSources(records)
	if v = sources[0]; v.Parts[0].StartMicros == nil || v.Parts[1].StartMicros != nil || v.TotalDurationMicros != nil || v.RuntimeMicros() != nil {
		t.Fatalf("unknown duration: %+v", v.Parts)
	}
}

func TestDecideDirectPlayChecksEveryPart(t *testing.T) {
	h264, hevc, aac := "h264", "hevc", "aac"
	meta := func(codec *string) *MediaMetadata {
		d := int64(10)
		return &MediaMetadata{Format: MediaFormat{DurationMicros: &d}, Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: codec, Video: &MediaVideo{}},
			{Index: 1, Kind: "audio", Codec: &aac},
		}}
	}
	records := []PlaybackSourceRecord{
		{ID: "a", ContentType: "video/x-matroska", FileName: "m.cd1.mkv", Metadata: meta(&h264), PartOf: "a", PartNumber: 1},
		{ID: "b", ContentType: "video/x-matroska", FileName: "m.cd2.mkv", Metadata: meta(&hevc), PartOf: "a", PartNumber: 2},
	}
	source := BuildPlaybackSources(records)[0]
	caps := ClientCapabilities{Containers: []string{"mkv"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}}
	if ok, reasons := DecideDirectPlay(caps, source); ok || !slices.Equal(reasons, []string{PlaybackReasonVideoCodec}) {
		t.Fatalf("part 2 is hevc: %v %v", ok, reasons)
	}
	caps.VideoCodecs = append(caps.VideoCodecs, "hevc")
	if ok, reasons := DecideDirectPlay(caps, source); !ok || len(reasons) != 0 {
		t.Fatalf("every part playable: %v %v", ok, reasons)
	}
	// An unprobed later part is never confirmed.
	records[1].Metadata = nil
	source = BuildPlaybackSources(records)[0]
	if ok, reasons := DecideDirectPlay(caps, source); ok || !slices.Equal(reasons, []string{PlaybackReasonSourceNotProbe}) {
		t.Fatalf("unprobed part: %v %v", ok, reasons)
	}
	if exclude := DirectPlayExclusion(caps)(source); !slices.Equal(exclude, []string{PlaybackReasonSourceNotProbe}) {
		t.Fatalf("exclusion = %v", exclude)
	}
}
