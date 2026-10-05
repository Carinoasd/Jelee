package domain

import (
	"strings"
	"testing"
)

func TestCollectionAndPlaylistInputs(t *testing.T) {
	name := " Star "
	in := CollectionInput{Name: "  Saga ", Overview: "line\nbreak\ttab", NFOName: &name}.Normalize()
	if in.Name != "Saga" || *in.NFOName != "Star" || !in.Valid() {
		t.Fatalf("normalized input: %+v", in)
	}
	for label, bad := range map[string]CollectionInput{
		"blank":            {Name: ""},
		"control":          {Name: "a\x00b"},
		"untrimmed":        {Name: " a"},
		"long":             {Name: strings.Repeat("a", CollectionNameMax+1)},
		"overview control": {Name: "a", Overview: "\x07"},
		"long overview":    {Name: "a", Overview: strings.Repeat("a", CollectionOverviewMax+1)},
		"blank nfo name":   {Name: "a", NFOName: new("")},
		"invalid utf8":     {Name: "\xff"},
	} {
		if bad.Valid() {
			t.Errorf("%s accepted", label)
		}
	}
	if p := (PlaylistInput{Name: " Evening "}).Normalize(); p.Name != "Evening" || !p.Valid() {
		t.Fatalf("playlist input: %+v", p)
	}
	if (PlaylistInput{Name: "\t"}).Normalize().Valid() {
		t.Fatal("blank playlist name accepted")
	}
	id := "33333333-3333-4333-8333-333333333333"
	if !ValidMembershipBatch([]string{id, id}) || ValidMembershipBatch(nil) || ValidMembershipBatch([]string{"x"}) || ValidMembershipBatch(make([]string, MembershipBatchMax+1)) {
		t.Fatal("membership batch validation")
	}
}
