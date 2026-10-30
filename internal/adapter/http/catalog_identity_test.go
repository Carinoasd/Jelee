package httpapi

import (
	"encoding/json"
	"testing"
)

func TestIdentityPatchDecoding(t *testing.T) {
	end, yes := 4, true
	cases := []struct {
		field, value string
		end          *int
		ok           bool
		check        func(t *testing.T, kind, parent string, number, numberEnd int, has bool)
	}{
		{"kind", `"Movie"`, nil, true, func(t *testing.T, kind, _ string, _, _ int, has bool) {
			if kind != "Movie" || !has {
				t.Error("kind")
			}
		}},
		{"kind", `null`, nil, false, nil},
		{"kind", `3`, nil, false, nil},
		{"parent", `null`, nil, true, func(t *testing.T, _, parent string, _, _ int, has bool) {
			if parent != "" || !has {
				t.Error("parent null")
			}
		}},
		{"parent", `"33333333-3333-4333-8333-333333333333"`, nil, true, nil},
		{"parent", `""`, nil, false, nil},
		{"parent", `7`, nil, false, nil},
		{"season", `2`, nil, true, func(t *testing.T, _, _ string, number, numberEnd int, _ bool) {
			if number != 2 || numberEnd != 0 {
				t.Error("season")
			}
		}},
		{"season", `2`, &end, false, nil},
		{"season", `null`, nil, false, nil},
		{"season", `"2"`, nil, false, nil},
		{"episode", `3`, nil, true, func(t *testing.T, _, _ string, number, numberEnd int, _ bool) {
			if number != 3 || numberEnd != 3 {
				t.Error("single episode")
			}
		}},
		{"episode", `3`, &end, true, func(t *testing.T, _, _ string, number, numberEnd int, _ bool) {
			if number != 3 || numberEnd != 4 {
				t.Error("episode range")
			}
		}},
		{"episode", `1.5`, nil, false, nil},
		{"title", `"x"`, nil, false, nil},
	}
	for _, c := range cases {
		p, err := identityPatch(c.field, json.RawMessage(c.value), c.end, &yes)
		if (err == nil) != c.ok {
			t.Errorf("%s %s: err %v", c.field, c.value, err)
			continue
		}
		if c.check != nil {
			c.check(t, p.Kind, p.Parent, p.Number, p.NumberEnd, p.HasValue)
		}
	}
	if p, err := identityPatch("season", nil, nil, &yes); err != nil || p.HasValue || p.Locked == nil {
		t.Error("lock without value", err)
	}
	if _, err := identityPatch("episode", nil, &end, &yes); err == nil {
		t.Error("range without value accepted")
	}
}
