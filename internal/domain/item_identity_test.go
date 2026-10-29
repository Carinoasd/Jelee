package domain

import (
	"testing"
	"time"
)

func TestValidItemIdentityPatches(t *testing.T) {
	item := "11111111-1111-4111-8111-111111111111"
	yes, no := true, false
	value := func(p ItemIdentityPatch) ItemIdentityPatch { p.HasValue = true; return p }
	cases := []struct {
		name    string
		rev     int64
		patches []ItemIdentityPatch
		want    bool
	}{
		{"kind value", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "kind", Kind: "Movie"})}, true},
		{"kind value locked", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "kind", Kind: "HomeVideo", Locked: &yes})}, true},
		{"value never unlocked", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "kind", Kind: "Movie", Locked: &no})}, false},
		{"structural kind", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "kind", Kind: "Series"})}, false},
		{"parent none", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "parent"})}, true},
		{"parent id", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "parent", Parent: item})}, true},
		{"parent bad id", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "parent", Parent: "x"})}, false},
		{"season", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "season", Number: ItemIdentityNumberMax})}, true},
		{"season too high", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "season", Number: ItemIdentityNumberMax + 1})}, false},
		{"season negative", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "season", Number: -1})}, false},
		{"season with end", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "season", Number: 1, NumberEnd: 2})}, false},
		{"episode range", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "episode", Number: 3, NumberEnd: 4})}, true},
		{"episode reversed", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "episode", Number: 4, NumberEnd: 3})}, false},
		{"mixed value", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "season", Number: 1, Kind: "Movie"})}, false},
		{"lock only", 1, []ItemIdentityPatch{{Field: "episode", Locked: &yes}}, true},
		{"unlock only", 1, []ItemIdentityPatch{{Field: "parent", Locked: &no}}, true},
		{"neither", 1, []ItemIdentityPatch{{Field: "season"}}, false},
		{"stray value without flag", 1, []ItemIdentityPatch{{Field: "season", Number: 2, Locked: &yes}}, false},
		{"unknown field", 1, []ItemIdentityPatch{value(ItemIdentityPatch{Field: "title"})}, false},
		{"duplicate", 1, []ItemIdentityPatch{{Field: "kind", Locked: &yes}, {Field: "kind", Locked: &no}}, false},
		{"empty", 1, nil, false},
		{"revision zero", 0, []ItemIdentityPatch{{Field: "kind", Locked: &yes}}, false},
		{"revision exhausted", ItemIdentityRevisionMax, []ItemIdentityPatch{{Field: "kind", Locked: &yes}}, false},
	}
	for _, c := range cases {
		if got := ValidItemIdentityPatches(item, c.rev, c.patches); got != c.want {
			t.Errorf("%s: got %t", c.name, got)
		}
	}
	if ValidItemIdentityPatches("x", 1, []ItemIdentityPatch{{Field: "kind", Locked: &yes}}) {
		t.Error("invalid item accepted")
	}
	five := []ItemIdentityPatch{{Field: "kind", Locked: &yes}, {Field: "parent", Locked: &yes}, {Field: "season", Locked: &yes}, {Field: "episode", Locked: &yes}, {Field: "kind", Locked: &no}}
	if ValidItemIdentityPatches(item, 1, five) {
		t.Error("more patches than fields accepted")
	}
}

func TestItemIdentityRules(t *testing.T) {
	ordered := OrderItemIdentityPatches([]ItemIdentityPatch{{Field: "episode"}, {Field: "season"}, {Field: "kind"}, {Field: "parent"}})
	for i, field := range ItemIdentityFields() {
		if ordered[i].Field != field {
			t.Fatalf("order %v", ordered)
		}
	}
	for _, c := range []struct {
		kind, field string
		want        bool
	}{{"Movie", "kind", true}, {"Movie", "season", false}, {"Season", "season", true}, {"Season", "episode", false}, {"Episode", "episode", true}, {"Episode", "parent", true}, {"Series", "parent", false}, {"Episode", "title", false}} {
		if ItemIdentityFieldApplies(c.kind, c.field) != c.want {
			t.Errorf("%s/%s", c.kind, c.field)
		}
	}
	for _, c := range []struct {
		kind, parent string
		want         bool
	}{{"Season", "Series", true}, {"Episode", "Season", true}, {"Episode", "Series", true}, {"Season", "Season", false}, {"Movie", "Series", false}, {"Episode", "Episode", false}} {
		if ItemIdentityParentAllowed(c.kind, c.parent) != c.want {
			t.Errorf("%s under %s", c.kind, c.parent)
		}
	}
	if !ItemIdentityLeafKind("HomeVideo") || ItemIdentityLeafKind("Season") {
		t.Error("leaf kinds")
	}
}

func TestCloneItemIdentityIsDeep(t *testing.T) {
	n, now := 3, time.Now()
	v := ItemIdentity{Season: &n, Episode: &n, EpisodeEnd: &n, UpdatedAt: &now, Scan: &ItemIdentityScan{Kind: "Episode", Season: &n, Episode: &n, EpisodeEnd: &n}}
	c := CloneItemIdentity(v)
	n = 9
	now = now.Add(time.Hour)
	if *c.Season != 3 || *c.Episode != 3 || *c.EpisodeEnd != 3 || *c.Scan.Season != 3 || *c.Scan.Episode != 3 || *c.Scan.EpisodeEnd != 3 || c.UpdatedAt.Equal(now) || c.Scan == v.Scan {
		t.Fatalf("clone shares state %+v", c)
	}
	if empty := CloneItemIdentity(ItemIdentity{}); empty.Scan != nil || empty.Season != nil || empty.UpdatedAt != nil {
		t.Fatal("empty clone")
	}
}

func TestValidCatalogPendingDecision(t *testing.T) {
	text := func(s string) *string { return &s }
	number := func(n int) *int { return &n }
	cases := []struct {
		name string
		d    CatalogPendingDecision
		want bool
	}{
		{"empty", CatalogPendingDecision{}, true},
		{"movie", CatalogPendingDecision{Kind: "movie", Title: text("Heat"), Year: number(1995)}, true},
		{"episode", CatalogPendingDecision{Kind: "episode", SeriesTitle: text("Show"), Season: number(1), Episode: number(2), EpisodeEnd: number(3)}, true},
		{"series kind", CatalogPendingDecision{Kind: "series"}, false},
		{"blank title", CatalogPendingDecision{Title: text("  ")}, false},
		{"nul title", CatalogPendingDecision{Title: text("a\x00")}, false},
		{"long title", CatalogPendingDecision{SeriesTitle: text(string(make([]byte, 1025)))}, false},
		{"year", CatalogPendingDecision{Year: number(0)}, false},
		{"season", CatalogPendingDecision{Season: number(-1)}, false},
		{"episode", CatalogPendingDecision{Episode: number(ItemIdentityNumberMax + 1)}, false},
		{"range", CatalogPendingDecision{Episode: number(4), EpisodeEnd: number(3)}, false},
		{"movie numbers", CatalogPendingDecision{Kind: "movie", Season: number(1)}, false},
	}
	for _, c := range cases {
		if got := ValidCatalogPendingDecision(c.d); got != c.want {
			t.Errorf("%s: got %t", c.name, got)
		}
	}
}
