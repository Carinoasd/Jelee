package domain

import (
	"errors"
	"slices"
	"testing"
)

func selectionSource(id string, resolution VersionResolution, hdr bool, bitrate, size, quality int64, primary bool) PlaybackSource {
	s := PlaybackSource{ID: id, Container: "mkv", Probed: true, Primary: primary,
		Version: VersionLabels{Resolution: resolution, QualityScore: quality},
		Video:   []PlaybackVideoTrack{{Index: 0, Codec: "hevc", Primary: true}},
		Audio:   []PlaybackAudioTrack{{Index: 1, Codec: "aac", Default: true}}}
	if hdr {
		s.Version.HDR = []VersionHDR{VersionHDR10}
	}
	if bitrate > 0 {
		s.BitRate = &bitrate
	}
	if size > 0 {
		s.SizeBytes = &size
	}
	return s
}

func selectionIDs(sources []PlaybackSource) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.ID)
	}
	return out
}

// TestSelectVersionRules covers every rule in precedence order and the
// reason it reports (G20.2).
func TestSelectVersionRules(t *testing.T) {
	uhd := selectionSource("a-uhd", VersionResolution2160p, true, 60_000_000, 60<<30, 900, false)
	fhd := selectionSource("b-fhd", VersionResolution1080p, false, 12_000_000, 12<<30, 600, false)
	hd := selectionSource("c-hd", VersionResolution720p, false, 4_000_000, 3<<30, 300, false)
	hdHDR := selectionSource("d-hd-hdr", VersionResolution720p, true, 5_000_000, 4<<30, 350, false)
	primaryHD := hd
	primaryHD.Primary = true
	all := func() []PlaybackSource { return []PlaybackSource{uhd, fhd, hd} }
	cases := []struct {
		name    string
		sources []PlaybackSource
		pref    VersionPreference
		exclude func(PlaybackSource) []string
		order   []string
		reason  string
	}{
		{"no preference: quality", all(), VersionPreference{}, nil, []string{"a-uhd", "b-fhd", "c-hd"}, VersionReasonQuality},
		{"main version", []PlaybackSource{uhd, fhd, primaryHD}, VersionPreference{}, nil, []string{"c-hd", "a-uhd", "b-fhd"}, VersionReasonPrimary},
		{"bandwidth", all(), VersionPreference{MaxBitrate: ptr(int64(20_000_000))}, nil, []string{"b-fhd", "c-hd", "a-uhd"}, VersionReasonBandwidth},
		{"bandwidth before main version", []PlaybackSource{uhd, fhd, primaryHD}, VersionPreference{MaxBitrate: ptr(int64(2_000_000))}, nil, []string{"c-hd", "b-fhd", "a-uhd"}, VersionReasonBandwidth},
		{"resolution", all(), VersionPreference{MaxHeight: ptr(1080)}, nil, []string{"b-fhd", "c-hd", "a-uhd"}, VersionReasonResolution},
		{"resolution decides", []PlaybackSource{uhd, fhd}, VersionPreference{MaxHeight: ptr(1080)}, nil, []string{"b-fhd", "a-uhd"}, VersionReasonResolution},
		{"sdr", []PlaybackSource{hdHDR, hd}, VersionPreference{DynamicRange: ptr(VersionRangeSDR)}, nil, []string{"c-hd", "d-hd-hdr"}, VersionReasonRange},
		{"hdr", []PlaybackSource{fhd, uhd}, VersionPreference{DynamicRange: ptr(VersionRangeHDR)}, nil, []string{"a-uhd", "b-fhd"}, VersionReasonRange},
		{"smaller", all(), VersionPreference{PreferSmaller: ptr(true)}, nil, []string{"c-hd", "b-fhd", "a-uhd"}, VersionReasonSize},
		{"smaller off", all(), VersionPreference{PreferSmaller: ptr(false)}, nil, []string{"a-uhd", "b-fhd", "c-hd"}, VersionReasonQuality},
		{"resolution before range", []PlaybackSource{uhd, hdHDR, fhd}, VersionPreference{MaxHeight: ptr(1080), DynamicRange: ptr(VersionRangeHDR)}, nil,
			[]string{"d-hd-hdr", "b-fhd", "a-uhd"}, VersionReasonResolution},
		{"range breaks the tie", []PlaybackSource{hdHDR, fhd}, VersionPreference{MaxHeight: ptr(1080), DynamicRange: ptr(VersionRangeHDR)}, nil,
			[]string{"d-hd-hdr", "b-fhd"}, VersionReasonRange},
		{"capability excludes the preferred", all(), VersionPreference{MaxHeight: ptr(1080)}, func(s PlaybackSource) []string {
			if s.ID == "b-fhd" {
				return []string{PlaybackReasonVideoCodec}
			}
			return nil
		}, []string{"b-fhd", "c-hd", "a-uhd"}, VersionReasonResolution},
		{"only playable", all(), VersionPreference{}, func(s PlaybackSource) []string {
			if s.ID != "c-hd" {
				return []string{PlaybackReasonContainer}
			}
			return nil
		}, []string{"a-uhd", "b-fhd", "c-hd"}, VersionReasonOnlyPlayable},
		{"none playable", all(), VersionPreference{}, func(PlaybackSource) []string { return []string{PlaybackReasonContainer} }, []string{"a-uhd", "b-fhd", "c-hd"}, VersionReasonNonePlayable},
		{"only version", []PlaybackSource{hd}, VersionPreference{MaxHeight: ptr(480)}, nil, []string{"c-hd"}, VersionReasonOnlyVersion},
		{"no version", nil, VersionPreference{}, nil, []string{}, VersionReasonNoVersion},
		{"stable", []PlaybackSource{selectionSource("y", "", false, 0, 0, 0, false), selectionSource("x", "", false, 0, 0, 0, false)}, VersionPreference{MaxHeight: ptr(720)}, nil, []string{"x", "y"}, VersionReasonStableOrder},
	}
	for _, c := range cases {
		sel := SelectVersion(c.sources, c.pref, c.exclude)
		if got := selectionIDs(c.sources); !slices.Equal(got, c.order) {
			t.Fatalf("%s: order %v, want %v", c.name, got, c.order)
		}
		if sel.Reason != c.reason {
			t.Fatalf("%s: reason %q, want %q", c.name, sel.Reason, c.reason)
		}
		want := ""
		for _, s := range c.sources {
			if c.exclude == nil || len(c.exclude(s)) == 0 {
				want = s.ID
				break
			}
		}
		if sel.DefaultSourceID != want {
			t.Fatalf("%s: default %q, want %q", c.name, sel.DefaultSourceID, want)
		}
	}
	sel := SelectVersion(all(), VersionPreference{MaxBitrate: ptr(int64(1)), MaxHeight: ptr(480), DynamicRange: ptr("sdr"), PreferSmaller: ptr(true)},
		func(s PlaybackSource) []string {
			if s.ID == "a-uhd" {
				return []string{PlaybackReasonBitrate}
			}
			return nil
		})
	if !slices.Equal(sel.Preferences, []string{"bandwidth", "resolution", "dynamic_range", "size"}) || len(sel.Excluded) != 1 ||
		sel.Excluded[0].SourceID != "a-uhd" || sel.Excluded[0].Reasons[0] != PlaybackReasonBitrate {
		t.Fatalf("explanation %+v", sel)
	}
}

// TestSelectVersionNeverAcceptsConversion: a declaration excludes what the
// client cannot play as it is; nothing else becomes acceptable.
func TestSelectVersionNeverAcceptsConversion(t *testing.T) {
	sources := []PlaybackSource{selectionSource("a", VersionResolution2160p, true, 80_000_000, 0, 900, true), selectionSource("b", VersionResolution1080p, false, 8_000_000, 0, 500, false)}
	sources[1].Container = "avi"
	exclude := DirectPlayExclusion(ClientCapabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"aac"}, MaxBitrate: 10_000_000})
	sel := SelectVersion(sources, VersionPreference{}, exclude)
	if sel.DefaultSourceID != "" || sel.Reason != VersionReasonNonePlayable || len(sel.Excluded) != 2 {
		t.Fatalf("selection %+v", sel)
	}
	if !slices.Contains(sel.Excluded[0].Reasons, PlaybackReasonContainer) || !slices.Contains(sel.Excluded[0].Reasons, PlaybackReasonBitrate) {
		t.Fatalf("reasons %+v", sel.Excluded)
	}
	unprobed := PlaybackSource{ID: "u", Container: "mp4"}
	if sel := SelectVersion([]PlaybackSource{unprobed}, VersionPreference{}, exclude); sel.DefaultSourceID != "" || !slices.Contains(sel.Excluded[0].Reasons, PlaybackReasonSourceNotProbe) {
		t.Fatalf("unprobed %+v", sel)
	}
}

func TestNormalizeVersionPreference(t *testing.T) {
	valid := []VersionPreference{{}, {MaxHeight: ptr(2160)}, {DynamicRange: ptr("hdr")}, {MaxBitrate: ptr(int64(ClientCapabilityBitrateMax))}, {PreferSmaller: ptr(false)}}
	for _, p := range valid {
		if _, err := NormalizeVersionPreference(p); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	invalid := []VersionPreference{{MaxHeight: ptr(1000)}, {MaxHeight: ptr(0)}, {DynamicRange: ptr("HDR")}, {DynamicRange: ptr("")}, {MaxBitrate: ptr(int64(0))}, {MaxBitrate: ptr(int64(ClientCapabilityBitrateMax + 1))}}
	for _, p := range invalid {
		if _, err := NormalizeVersionPreference(p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v accepted", p)
		}
	}
	if !(VersionPreference{}).Empty() || (VersionPreference{PreferSmaller: ptr(false)}).Empty() {
		t.Fatal("empty")
	}
	for tier, want := range map[VersionResolution]int64{VersionResolutionSD: 480, VersionResolution1080p: 1080, VersionResolution4320p: 4320, "": 0, "weird": 0} {
		if got := VersionHeight(PlaybackSource{Version: VersionLabels{Resolution: tier}}); got != want {
			t.Fatalf("%s: %d", tier, got)
		}
	}
}
