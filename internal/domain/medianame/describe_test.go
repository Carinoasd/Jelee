package medianame

import (
	"fmt"
	"strings"
)

// describe renders every field in a canonical compact form so table cases
// assert the complete result, not a hand-picked subset.
func describe(p Parsed) string {
	if p.Rejected != RejectNone {
		return "rejected=" + p.Rejected.String()
	}
	parts := []string{p.Kind.String()}
	if p.Title != "" {
		parts = append(parts, "t="+p.Title)
	}
	if p.Year != 0 {
		parts = append(parts, fmt.Sprintf("y=%d", p.Year))
	}
	if p.HasSeason {
		parts = append(parts, fmt.Sprintf("s=%d", p.Season))
	}
	if p.HasEpisode {
		parts = append(parts, fmt.Sprintf("e=%d", p.Episode))
		if p.EpisodeEnd != p.Episode {
			parts = append(parts, fmt.Sprintf("e2=%d", p.EpisodeEnd))
		}
	}
	if p.Absolute != 0 {
		parts = append(parts, fmt.Sprintf("abs=%d", p.Absolute))
	}
	if p.Part != 0 {
		parts = append(parts, fmt.Sprintf("part=%d", p.Part))
	}
	if !p.Date.IsZero() {
		parts = append(parts, fmt.Sprintf("d=%04d-%02d-%02d", p.Date.Year, p.Date.Month, p.Date.Day))
	}
	if p.Special != SpecialNone {
		parts = append(parts, "sp="+p.Special.String())
	}
	if p.Conflict {
		parts = append(parts, "conflict")
	}
	parts = append(parts, "c="+p.Confidence.String())
	return strings.Join(parts, " ")
}
