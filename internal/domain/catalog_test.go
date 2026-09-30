package domain

import (
	"strings"
	"testing"
)

func TestValidIDCanonicalBoundary(t *testing.T) {
	// The boundary validates the textual UUID form, not issuance or existence.
	for _, id := range []string{
		"12345678-1234-1234-1234-123456789abc",
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	} {
		if !ValidID(id) {
			t.Errorf("canonical identifier rejected: %q", id)
		}
	}
	canonical := "12345678-1234-1234-1234-123456789abc"
	for _, tc := range []struct{ name, id string }{
		{"empty", ""},
		{"truncated", canonical[:35]},
		{"extended", canonical + "0"},
		{"uppercase", strings.ToUpper(canonical)},
		{"no_separators", strings.ReplaceAll(canonical, "-", "")},
		{"wrong_separator", strings.Replace(canonical, "-", "_", 1)},
		{"misplaced_separator", "1234567-81234-1234-1234-123456789abc"},
		{"non_hex", "g" + canonical[1:]},
		{"unicode_lookalike_same_bytes", "α" + canonical[2:]},
		{"nul", "\x00" + canonical[1:]},
		{"leading_space", " " + canonical[1:]},
		{"trailing_newline", canonical[:35] + "\n"},
		{"quoted", `"` + canonical + `"`},
		{"braces", "{" + canonical + "}"},
		{"urn", "urn:uuid:" + canonical},
		{"encoded", strings.Replace(canonical, "-", "%2d", 1)},
		{"path", "../../" + canonical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ValidID(tc.id) {
				t.Fatalf("noncanonical input accepted: %q", tc.id)
			}
		})
	}
}
