package domain

import (
	"bytes"
	"slices"
	"testing"
)

func groupingInt(v int) *int { return &v }

func groupingMicros(minutes float64) *int64 {
	v := int64(minutes * 60e6)
	return &v
}

func TestVersionDurationsClose(t *testing.T) {
	minute := int64(60e6)
	for _, tc := range []struct {
		name string
		a, b int64
		want bool
	}{
		{"identical", 120 * minute, 120 * minute, true},
		{"floor for a short episode", 22 * minute, 22*minute + 30e6, true},
		{"beyond the floor of a short episode", 22 * minute, 22*minute + 31e6, false},
		{"2% of a feature", 120 * minute, 122*minute + 24e6, true},
		{"beyond 2% of the longer runtime", 120 * minute, 122*minute + 30e6, false},
		{"ceiling for a long cut", 200 * minute, 203 * minute, true},
		{"beyond the ceiling", 200 * minute, 203*minute + 1, false},
		{"order does not matter", 122*minute + 24e6, 120 * minute, true},
		{"extended cut", 148 * minute, 162 * minute, false},
		{"unknown", 0, 120 * minute, false},
		{"negative", -1, -1, false},
	} {
		if got := VersionDurationsClose(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: VersionDurationsClose(%d,%d)=%t", tc.name, tc.a, tc.b, got)
		}
	}
}

func TestDecideVersionGrouping(t *testing.T) {
	movie := func(id, ids string, minutes ...float64) VersionGroupingCandidate {
		c := VersionGroupingCandidate{ItemID: id, Kind: "Movie", ExternalIDs: []byte(ids)}
		for _, m := range minutes {
			c.Durations = append(c.Durations, groupingMicros(m))
		}
		return c
	}
	episode := func(id, ids string, season, number int, minutes float64) VersionGroupingCandidate {
		c := movie(id, ids, minutes)
		c.Kind, c.Season, c.Episode, c.EpisodeEnd = "Episode", groupingInt(season), groupingInt(number), groupingInt(number)
		return c
	}
	with := func(c VersionGroupingCandidate, change func(*VersionGroupingCandidate)) VersionGroupingCandidate {
		change(&c)
		return c
	}
	tmdb := `[{"type":"tmdb","value":"603"}]`
	for _, tc := range []struct {
		name     string
		a, b     VersionGroupingCandidate
		merge    bool
		reason   string
		evidence []string
	}{
		{"same tmdb id and runtime", movie("a", tmdb, 136), movie("b", tmdb, 136.5), true, VersionGroupingMerged, []string{"external_id:tmdb", "duration"}},
		{"provider type and value case", movie("a", `[{"type":"IMDB","value":"TT0133093"}]`, 136), movie("b", `[{"type":"imdb","value":"tt0133093"}]`, 136), true, VersionGroupingMerged, []string{"external_id:imdb", "duration"}},
		{"two shared providers", movie("a", `[{"type":"tmdb","value":"603"},{"type":"imdb","value":"tt0133093"}]`, 136), movie("b", `[{"type":"imdb","value":"tt0133093"},{"type":"tmdb","value":"603"}]`, 136), true, VersionGroupingMerged, []string{"external_id:imdb", "external_id:tmdb", "duration"}},
		{"every version of both items close", movie("a", tmdb, 136, 136.2), movie("b", tmdb, 135.9), true, VersionGroupingMerged, []string{"external_id:tmdb", "duration"}},
		{"conflicting second provider", movie("a", `[{"type":"tmdb","value":"603"},{"type":"imdb","value":"tt1"}]`, 136), movie("b", `[{"type":"tmdb","value":"603"},{"type":"imdb","value":"tt2"}]`, 136), false, VersionGroupingIDConflict, nil},
		{"different tmdb id", movie("a", tmdb, 136), movie("b", `[{"type":"tmdb","value":"604"}]`, 136), false, VersionGroupingIDConflict, nil},
		{"only a weak provider shared", movie("a", `[{"type":"custom","value":"x"}]`, 136), movie("b", `[{"type":"custom","value":"x"}]`, 136), false, VersionGroupingNoSharedID, nil},
		{"disjoint providers", movie("a", `[{"type":"imdb","value":"tt1"}]`, 136), movie("b", tmdb, 136), false, VersionGroupingNoSharedID, nil},
		{"unreadable ids", movie("a", `{`, 136), movie("b", tmdb, 136), false, VersionGroupingNoSharedID, nil},
		{"runtime too far apart", movie("a", tmdb, 136), movie("b", tmdb, 150), false, VersionGroupingDuration, nil},
		{"one version of many too far", movie("a", tmdb, 136, 150), movie("b", tmdb, 136), false, VersionGroupingDuration, nil},
		{"not probed", movie("a", tmdb, 136), with(movie("b", tmdb), func(c *VersionGroupingCandidate) { c.Durations = []*int64{nil} }), false, VersionGroupingDurationUnknown, nil},
		{"no versions", movie("a", tmdb, 136), movie("b", tmdb), false, VersionGroupingDurationUnknown, nil},
		{"zero runtime", movie("a", tmdb, 136), movie("b", tmdb, 0), false, VersionGroupingDurationUnknown, nil},
		{"locked", with(movie("a", tmdb, 136), func(c *VersionGroupingCandidate) { c.Held = VersionGroupingHeldLocked }), movie("b", tmdb, 136), false, VersionGroupingHeldLocked, nil},
		{"manual on the other side", movie("a", tmdb, 136), with(movie("b", tmdb, 136), func(c *VersionGroupingCandidate) { c.Held = VersionGroupingHeldManual }), false, VersionGroupingHeldManual, nil},
		{"imported", with(movie("a", tmdb, 136), func(c *VersionGroupingCandidate) { c.Held = VersionGroupingHeldImported }), movie("b", tmdb, 136), false, VersionGroupingHeldImported, nil},
		{"movie and episode", movie("a", tmdb, 22), episode("b", tmdb, 1, 1, 22), false, VersionGroupingKind, nil},
		{"container kind", with(movie("a", tmdb, 22), func(c *VersionGroupingCandidate) { c.Kind = "Season" }), with(movie("b", tmdb, 22), func(c *VersionGroupingCandidate) { c.Kind = "Season" }), false, VersionGroupingKind, nil},
		{"same episode", episode("a", tmdb, 1, 5, 22), episode("b", tmdb, 1, 5, 22.2), true, VersionGroupingMerged, []string{"external_id:tmdb", "duration"}},
		{"episode numbers differ", episode("a", tmdb, 1, 5, 22), episode("b", tmdb, 1, 6, 22), false, VersionGroupingNumbers, nil},
		{"season differs", episode("a", tmdb, 1, 5, 22), episode("b", tmdb, 2, 5, 22), false, VersionGroupingNumbers, nil},
		{"range differs", episode("a", tmdb, 1, 5, 44), with(episode("b", tmdb, 1, 5, 44), func(c *VersionGroupingCandidate) { c.EpisodeEnd = groupingInt(6) }), false, VersionGroupingNumbers, nil},
		{"episode without numbers", with(episode("a", tmdb, 1, 5, 22), func(c *VersionGroupingCandidate) { c.Episode = nil }), with(episode("b", tmdb, 1, 5, 22), func(c *VersionGroupingCandidate) { c.Episode = nil }), false, VersionGroupingNumbers, nil},
		{"short episode beyond the floor", episode("a", tmdb, 1, 5, 22), episode("b", tmdb, 1, 5, 23), false, VersionGroupingDuration, nil},
		{"nfo numbers override the file name", with(episode("a", tmdb, 1, 6, 22), func(c *VersionGroupingCandidate) { c.NFONumbers = true }), episode("b", tmdb, 1, 6, 22), true, VersionGroupingMerged, []string{"external_id:tmdb", "duration", VersionGroupingNFONumbers}},
	} {
		got := DecideVersionGrouping(tc.a, tc.b)
		if got.Merge != tc.merge || got.Reason != tc.reason || !slices.Equal(got.Evidence, tc.evidence) {
			t.Errorf("%s: got %+v, want merge=%t reason=%s evidence=%v", tc.name, got, tc.merge, tc.reason, tc.evidence)
		}
		// The decision is symmetric except for which side a hold names.
		if back := DecideVersionGrouping(tc.b, tc.a); back.Merge != got.Merge {
			t.Errorf("%s: asymmetric decision %+v", tc.name, back)
		}
	}
}

func TestVersionGroupingProbes(t *testing.T) {
	got := VersionGroupingProbes([]byte(`[{"type":"tmdb","value":"603"},{"type":"custom","value":"x"},{"type":"IMDb","value":"tt0133093","default":true},{"type":"tmdb","value":"603"},{"type":"tvdb","value":" "}]`))
	want := []string{`[{"type":"tmdb","value":"603"}]`, `[{"type":"IMDb","value":"tt0133093"}]`}
	if !slices.Equal(got, want) {
		t.Fatalf("probes %q", got)
	}
	if VersionGroupingProbes([]byte(`not json`)) != nil || VersionGroupingProbes(nil) != nil {
		t.Fatal("probes of an unreadable document")
	}
	if got := SharedVersionGroupingIDs([]byte(`[{"type":"tvdb","value":"1"}]`), []byte(`[{"type":"TVDB","value":"1"}]`)); !slices.Equal(got, []string{"tvdb"}) {
		t.Fatalf("shared %v", got)
	}
}

func TestVersionGroupingEvidence(t *testing.T) {
	base := VersionGroupingCandidate{ItemID: "a", Kind: "Episode", ExternalIDs: []byte(`[{"type":"tmdb","value":"1"}]`), Season: groupingInt(1), Episode: groupingInt(2), EpisodeEnd: groupingInt(2), Durations: []*int64{groupingMicros(22), nil}}
	first := VersionGroupingEvidence(base)
	if len(first) != 32 || !bytes.Equal(first, VersionGroupingEvidence(base)) {
		t.Fatal("evidence not a stable digest")
	}
	// The item ID and the NFO-over-file-name flag do not change the
	// decision inputs; everything else does.
	same := base
	same.ItemID, same.NFONumbers = "b", true
	if !bytes.Equal(first, VersionGroupingEvidence(same)) {
		t.Fatal("evidence depends on the item ID")
	}
	changes := []func(*VersionGroupingCandidate){
		func(c *VersionGroupingCandidate) { c.Kind = "Movie" },
		func(c *VersionGroupingCandidate) { c.ExternalIDs = []byte(`[{"type":"tmdb","value":"2"}]`) },
		func(c *VersionGroupingCandidate) { c.Season = nil },
		func(c *VersionGroupingCandidate) { c.Episode = groupingInt(3) },
		func(c *VersionGroupingCandidate) { c.EpisodeEnd = groupingInt(3) },
		func(c *VersionGroupingCandidate) { c.Held = VersionGroupingHeldLocked },
		func(c *VersionGroupingCandidate) { c.Durations = []*int64{groupingMicros(22), groupingMicros(22)} },
		func(c *VersionGroupingCandidate) { c.Durations = c.Durations[:1] },
	}
	for i, change := range changes {
		c := base
		c.Durations = slices.Clone(base.Durations)
		change(&c)
		if bytes.Equal(first, VersionGroupingEvidence(c)) {
			t.Errorf("change %d kept the evidence", i)
		}
	}
}

func TestCatalogNumbersDisagree(t *testing.T) {
	for _, tc := range []struct {
		name            string
		season, episode *int
		want            bool
	}{
		{"no NFO numbers", nil, nil, false},
		{"same numbers", groupingInt(1), groupingInt(5), false},
		{"other episode", groupingInt(1), groupingInt(6), true},
		{"other season", groupingInt(2), nil, true},
		{"episode only, same", nil, groupingInt(5), false},
	} {
		if got := CatalogNumbersDisagree(tc.season, tc.episode, 1, 5); got != tc.want {
			t.Errorf("%s: %t", tc.name, got)
		}
	}
}
