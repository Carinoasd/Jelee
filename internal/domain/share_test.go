package domain

import (
	"strings"
	"testing"
	"time"
)

func TestShareInputValid(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	id := "11111111-1111-4111-8111-111111111111"
	ok := ShareInput{LibraryID: id, ExpiresAt: now.Add(time.Hour), MaxStreams: 1, Note: "family"}
	if !ok.Valid(now) {
		t.Fatal("valid share refused")
	}
	item := ok
	item.LibraryID, item.ItemID = "", id
	if !item.Valid(now) {
		t.Fatal("item share refused")
	}
	for name, change := range map[string]func(*ShareInput){
		"both":        func(in *ShareInput) { in.ItemID = id },
		"neither":     func(in *ShareInput) { in.LibraryID = "" },
		"bad library": func(in *ShareInput) { in.LibraryID = "x" },
		"bad item":    func(in *ShareInput) { in.LibraryID, in.ItemID = "", "x" },
		"no expiry":   func(in *ShareInput) { in.ExpiresAt = time.Time{} },
		"too soon":    func(in *ShareInput) { in.ExpiresAt = now.Add(time.Minute) },
		"too late":    func(in *ShareInput) { in.ExpiresAt = now.Add(91 * 24 * time.Hour) },
		"no streams":  func(in *ShareInput) { in.MaxStreams = 0 },
		"streams":     func(in *ShareInput) { in.MaxStreams = ShareStreamsMax + 1 },
		"long note":   func(in *ShareInput) { in.Note = strings.Repeat("x", ShareNoteMax+1) },
		"control":     func(in *ShareInput) { in.Note = "a\nb" },
	} {
		in := ok
		change(&in)
		if in.Valid(now) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNetworkRuleInputValid(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	ok := NetworkRuleInput{LibraryID: id, Network: "lan", CIDRs: []string{"10.0.0.0/8"}, ClientKinds: []string{"web", "native"}, Note: "office\nwifi"}
	if !ok.Valid() {
		t.Fatal("valid rule refused")
	}
	for name, change := range map[string]func(*NetworkRuleInput){
		"library":    func(in *NetworkRuleInput) { in.LibraryID = "x" },
		"network":    func(in *NetworkRuleInput) { in.Network = "office" },
		"empty cidr": func(in *NetworkRuleInput) { in.CIDRs = []string{""} },
		"long cidr":  func(in *NetworkRuleInput) { in.CIDRs = []string{strings.Repeat("1", 65)} },
		"cidrs": func(in *NetworkRuleInput) {
			in.CIDRs = make([]string, NetworkRuleCIDRsMax+1)
			for i := range in.CIDRs {
				in.CIDRs[i] = "10.0.0.1"
			}
		},
		"kind":      func(in *NetworkRuleInput) { in.ClientKinds = []string{"tv"} },
		"duplicate": func(in *NetworkRuleInput) { in.ClientKinds = []string{"web", "web"} },
		"kinds":     func(in *NetworkRuleInput) { in.ClientKinds = []string{"web", "native", "web"} },
		"note":      func(in *NetworkRuleInput) { in.Note = "a\x00" },
		"long note": func(in *NetworkRuleInput) { in.Note = strings.Repeat("x", NetworkRuleNoteMax+1) },
	} {
		in := ok
		change(&in)
		if in.Valid() {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestClientRuleLibraries(t *testing.T) {
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	in := ClientRuleInput{Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "restrict_libraries", Libraries: []string{strings.ToUpper(b), a, b}}.Normalize()
	if !in.Valid() || len(in.Libraries) != 2 || in.Libraries[0] != a || in.Libraries[1] != b {
		t.Fatalf("restrict_libraries %+v", in)
	}
	observe := ClientRuleInput{Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "observe", Intent: "restrict_libraries", Libraries: []string{a}}.Normalize()
	if !observe.Valid() {
		t.Fatal("observed restrict_libraries refused")
	}
	for name, bad := range map[string]ClientRuleInput{
		"without libraries": {Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "restrict_libraries"},
		"libraries on deny": {Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "deny", Libraries: []string{a}},
		"invalid library":   {Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "restrict_libraries", Libraries: []string{"x"}},
	} {
		if bad.Normalize().Valid() {
			t.Errorf("%s accepted", name)
		}
	}
	if (ClientRuleInput{Action: "deny", Libraries: []string{}}).Normalize().Libraries != nil {
		t.Fatal("empty libraries kept")
	}
}
