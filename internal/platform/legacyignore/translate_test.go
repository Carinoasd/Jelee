package legacyignore

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestFixedUpstreamTranslation(t *testing.T) {
	data, err := os.ReadFile("testdata/ignore-021.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Commit string
		Cases  []struct {
			Name     string
			Rules    []string
			Path     string
			Errors   []string
			Compiled []struct {
				Negative bool
				Regex    *string
			}
			Ignored bool
		}
	}
	if json.Unmarshal(data, &fixture) != nil || fixture.Commit != "f7c6f07d66d0e1043d901a2ab2f58daca1862066" || len(fixture.Cases) < 13 {
		t.Fatal("missing pinned oracle")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ignored, index, invalid := false, 0, 0
			for _, input := range tc.Rules {
				expr, err := Translate(input)
				if err != nil {
					t.Fatal(err)
				}
				var compiled *regexp.Regexp
				if !expr.Inactive {
					compiled, err = regexp.Compile("(?i)" + expr.Pattern)
					if err != nil {
						invalid++
						continue
					}
				}
				if index >= len(tc.Compiled) {
					t.Fatal("unexpected expression")
				}
				want := tc.Compiled[index]
				index++
				if expr.Negative != want.Negative || expr.Inactive != (want.Regex == nil) || want.Regex != nil && expr.Pattern != *want.Regex {
					t.Fatalf("upstream expression differs for %q: got %#v want %#v", input, expr, want)
				}
				if compiled != nil && compiled.MatchString(tc.Path) {
					ignored = !expr.Negative
				}
			}
			if index != len(tc.Compiled) || invalid != len(tc.Errors) || ignored != tc.Ignored {
				t.Fatal("fixed ASCII oracle outcome differs", index, invalid, ignored)
			}
		})
	}
}

func TestTranslationBounds(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", MaxPatternBytes+1), "bad\x00pattern", string([]byte{0xff})} {
		if _, err := Translate(input); err != ErrInvalid {
			t.Fatal("unbounded or invalid input accepted")
		}
	}
	if _, err := Translate(strings.Repeat("x", MaxPatternBytes)); err != nil {
		t.Fatal(err)
	}
}

// Engine-only examples still prove translation text, without pretending that
// Go regexp can execute the dependency's complete .NET grammar.
func TestEngineOracleTranslation(t *testing.T) {
	data, err := os.ReadFile("testdata/ignore-021.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		EngineCases []struct {
			Name     string
			Rules    []string
			Errors   []string
			Compiled []struct {
				Negative bool
				Regex    *string
			}
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.EngineCases) != 48 {
		t.Fatal("missing engine boundary cases")
	}
	for _, tc := range fixture.EngineCases {
		t.Run(tc.Name, func(t *testing.T) {
			if len(tc.Errors) != 0 {
				return
			} // Upstream rejected these before reflection.
			if len(tc.Rules) != len(tc.Compiled) {
				t.Fatal("invalid oracle")
			}
			for i, rule := range tc.Rules {
				got, err := Translate(rule)
				if err != nil {
					t.Fatal(err)
				}
				want := tc.Compiled[i]
				if got.Negative != want.Negative || got.Inactive != (want.Regex == nil) || want.Regex != nil && got.Pattern != *want.Regex {
					t.Fatalf("translation differs for %q: %#v", rule, got)
				}
			}
		})
	}
}
