//go:build linux || windows

package ignoresource

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyProofsRetainCheckedAbsenceAndShadowing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", ".ignore"), []byte("rule"), 0600); err != nil {
		t.Fatal(err)
	}
	observation, err := NewResolver().ObserveLegacy(context.Background(), root, "a/b")
	if err != nil {
		t.Fatal(err)
	}
	proofs := observation.DirectoryProofs()
	if len(proofs) != 3 {
		t.Fatal("missing ancestor evidence")
	}
	if proofs[0].Checked || proofs[0].RulePresent || proofs[0].RuleIdentity != ([32]byte{}) {
		t.Fatal("shadowed source fabricated")
	}
	if !proofs[1].Checked || !proofs[1].RulePresent || proofs[1].RuleSHA256 != sha256.Sum256([]byte("rule")) || proofs[1].RuleSize != 4 {
		t.Fatal("selected source missing")
	}
	if !proofs[2].Checked || proofs[2].RulePresent || proofs[2].RuleSHA256 != ([32]byte{}) {
		t.Fatal("nearer absence not recorded")
	}
	for i, p := range proofs {
		if p.Version != LegacySourceProofVersion || p.Identity == ([32]byte{}) {
			t.Fatal("version/identity missing")
		}
		if i == 0 && p.ParentIdentity != ([32]byte{}) || i > 0 && p.ParentIdentity != proofs[i-1].Identity {
			t.Fatal("parent chain broken")
		}
		raw, err := json.Marshal(p)
		if err != nil || string(raw) != "{}" {
			t.Fatal("proof JSON exposed data")
		}
		if strings.Contains(fmt.Sprintf("%v %#v", p, p), "root") {
			t.Fatal("proof log exposure")
		}
	}
	proofs[1].Directory = "mutated"
	proofs[1].RuleSHA256 = [32]byte{}
	if observation.DirectoryProofs()[1].Directory != "a" || observation.DirectoryProofs()[1].RuleSHA256 == ([32]byte{}) {
		t.Fatal("proof alias mutation")
	}
	var zero LegacyObservation
	if zero.DirectoryProofs() != nil {
		t.Fatal("zero observation claims evidence")
	}
}
