package subtitles

import (
	"slices"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Stored sidecar rows accept exactly the charsets detection can report.
func TestDomainSidecarCharsetsMatch(t *testing.T) {
	var names []string
	for _, cs := range Charsets() {
		names = append(names, string(cs))
	}
	if !slices.Equal(names, domain.SidecarCharsets) {
		t.Fatal("domain.SidecarCharsets differs from subtitles.Charsets", names, domain.SidecarCharsets)
	}
}
