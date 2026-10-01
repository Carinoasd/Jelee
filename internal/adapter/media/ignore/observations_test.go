package ignoresource

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func TestDirectoryProofsNativeChainAndOwnedCopies(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "")
	text := "# private rule\n*.tmp\n"
	writeRule(t, root, "a/b/.jeleeignore", text)
	r := NewResolver()
	first, err := r.Evaluate(context.Background(), root, "a/b/movie.tmp", ignore.File, ignore.Options{})
	if err != nil || first.Match.Outcome != ignore.Exclude {
		t.Fatal("observe native chain", err)
	}
	proofs := first.DirectoryProofs()
	if len(proofs) != 3 {
		t.Fatal("incomplete chain")
	}
	for i, name := range []string{".", "a", "a/b"} {
		p := proofs[i]
		if p.Directory != name || p.Identity == ([32]byte{}) {
			t.Fatal("directory identity or path missing")
		}
		if i == 0 && p.ParentIdentity != ([32]byte{}) || i > 0 && p.ParentIdentity != proofs[i-1].Identity {
			t.Fatal("parent identity chain is broken")
		}
	}
	// An empty present rule and an absent rule have distinct evidence.
	if !proofs[0].RulePresent || proofs[0].RuleSHA256 != sha256.Sum256(nil) || proofs[0].RuleSize != 0 || proofs[0].RuleIdentity == ([32]byte{}) {
		t.Fatal("empty rule confused with absence")
	}
	if proofs[1].RulePresent || proofs[1].RuleIdentity != ([32]byte{}) || proofs[1].RuleSize != 0 || proofs[1].RuleModifiedNano != 0 || proofs[1].RuleSHA256 != ([32]byte{}) {
		t.Fatal("absent rule fabricated metadata")
	}
	info, err := os.Stat(filepath.Join(root, "a", "b", ".jeleeignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !proofs[2].RulePresent || proofs[2].RuleSHA256 != sha256.Sum256([]byte(text)) || proofs[2].RuleSize != int64(len(text)) || proofs[2].RuleModifiedNano != info.ModTime().UnixNano() {
		t.Fatal("present rule metadata does not match original bytes")
	}
	warm, err := r.Evaluate(context.Background(), root, "a/b/movie.tmp", ignore.File, ignore.Options{})
	if err != nil || !reflect.DeepEqual(proofs, warm.DirectoryProofs()) || first.Token() != warm.Token() {
		t.Fatal("warm observation changed stable evidence", err)
	}
	proofs[0].Directory = "tampered"
	proofs[0].Identity[0] ^= 0xff
	if !reflect.DeepEqual(first.DirectoryProofs(), warm.DirectoryProofs()) {
		t.Fatal("caller mutated retained or shared proof data")
	}
}

func TestDirectoryProofsExcludedSubtreeAndMissingDirectory(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "blocked/\n")
	r := NewResolver()
	o, err := r.Evaluate(context.Background(), root, "blocked/unopened/file.mkv", ignore.File, ignore.Options{})
	if err != nil || !o.Match.ParentBlocked || len(o.DirectoryProofs()) != 1 {
		t.Fatal("excluded descendant was given invented proofs", err)
	}
	o, err = r.Evaluate(context.Background(), root, "missing/file.mkv", ignore.File, ignore.Options{})
	if err == nil || o.DirectoryProofs() != nil || o.Token() != ([32]byte{}) {
		t.Fatal("missing directory treated as missing rule")
	}
	if (Observation{}).DirectoryProofs() != nil {
		t.Fatal("zero observation fabricated a root")
	}
}

func TestDirectoryProofsFailureDoesNotPublish(t *testing.T) {
	for _, failure := range []string{"changed", "close"} {
		t.Run(failure, func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeRule(t, root, ".jeleeignore", "*.tmp\n")
			h := &diskSourceHarness{}
			if failure == "changed" {
				h.beforeRule = func(_ string, pass int) error {
					if pass == 2 {
						return ErrChanged
					}
					return nil
				}
			} else {
				h.closeFailure = "directory"
			}
			o, err := NewResolver().evaluate(context.Background(), root, "x.tmp", ignore.File, ignore.Options{}, h.access())
			if err == nil || o.DirectoryProofs() != nil || o.Token() != ([32]byte{}) {
				t.Fatal("failed verification published partial proof")
			}
		})
	}
}

func TestDirectoryProofsRedacted(t *testing.T) {
	p := DirectoryProof{Directory: "private-path", RulePresent: true, RuleSize: 1234567, Identity: [32]byte{42}}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, p); strings.Contains(got, "private-path") || strings.Contains(got, "1234567") {
			t.Fatal("proof leaked through formatting")
		}
	}
	data, err := json.Marshal(p)
	if err != nil || string(data) != "{}" {
		t.Fatal("private worker proof exposed in JSON", err)
	}
}

func TestDirectoryProofsWarmCacheReobservesRuleBytes(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "*.tmp\n")
	name := filepath.Join(root, ".jeleeignore")
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver()
	before, err := r.Evaluate(context.Background(), root, "x.tmp", ignore.File, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Same size and timestamp cannot stand in for re-reading original bytes.
	if err := os.WriteFile(name, []byte("*.bak\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := r.Evaluate(context.Background(), root, "x.tmp", ignore.File, ignore.Options{})
	if err != nil || after.Match.Outcome == ignore.Exclude {
		t.Fatal("cached rule survived byte change", err)
	}
	oldProof, newProof := before.DirectoryProofs()[0], after.DirectoryProofs()[0]
	if oldProof.RuleSHA256 != sha256.Sum256([]byte("*.tmp\n")) || newProof.RuleSHA256 != sha256.Sum256([]byte("*.bak\n")) || before.Token() == after.Token() {
		t.Fatal("proof or token failed to bind exact observed bytes")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	absent, err := r.Evaluate(context.Background(), root, "x.tmp", ignore.File, ignore.Options{})
	if err != nil || absent.DirectoryProofs()[0].RulePresent || absent.Token() == after.Token() {
		t.Fatal("cache retained a deleted source", err)
	}
}
