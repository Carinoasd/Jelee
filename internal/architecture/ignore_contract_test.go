package architecture

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// Persistence must name the matcher actually shipped by this build. Keep this
// cross-layer check outside domain so its production dependency stays pure.
// The source observer does not export a proof schema; this checks no such API.
func TestIgnoreProgramIdentityMatchesMatcher(t *testing.T) {
	if domain.IgnoreProgramVersion != ignore.ProgramVersion {
		t.Fatalf("retained program version %q differs from matcher %q", domain.IgnoreProgramVersion, ignore.ProgramVersion)
	}
}
