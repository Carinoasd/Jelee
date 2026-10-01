package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestIgnoreDirectoryProofShapeAndParents(t *testing.T) {
	root := IgnoreDirectoryProof{RootID: ignoreTestJobID, Directory: ".", Identity: [32]byte{1}}
	child := IgnoreDirectoryProof{RootID: root.RootID, Directory: "a_%", ParentIdentity: root.Identity, Identity: [32]byte{2}}
	if ValidateIgnoreDirectoryProof(root) != nil || !IgnoreProofParentMatches(child, root) {
		t.Fatal("valid root/child rejected")
	}
	absent := child
	absent.MissingDirectory = true
	absent.Identity = [32]byte{}
	if ValidateIgnoreDirectoryProof(absent) != nil {
		t.Fatal("explicit child absence rejected")
	}
	nested := child
	nested.Directory = "a_%/b"
	nested.ParentIdentity = child.Identity
	if IgnoreProofParentMatches(nested, absent) || IgnoreProofParentMatches(nested, root) {
		t.Fatal("missing or non-immediate parent accepted")
	}
	for name, change := range map[string]func(*IgnoreDirectoryProof){
		"root_id":              func(p *IgnoreDirectoryProof) { p.RootID = "invalid" },
		"path":                 func(p *IgnoreDirectoryProof) { p.Directory = "../secret" },
		"control":              func(p *IgnoreDirectoryProof) { p.Directory = "bad\x00path" },
		"utf8":                 func(p *IgnoreDirectoryProof) { p.Directory = string([]byte{0xff}) },
		"depth":                func(p *IgnoreDirectoryProof) { p.Directory = strings.Repeat("a/", 128) + "b" },
		"parent":               func(p *IgnoreDirectoryProof) { p.ParentIdentity = [32]byte{} },
		"identity":             func(p *IgnoreDirectoryProof) { p.Identity = [32]byte{} },
		"absent_metadata":      func(p *IgnoreDirectoryProof) { p.RuleSize = 1 },
		"present_no_identity":  func(p *IgnoreDirectoryProof) { p.RulePresent = true },
		"missing_has_identity": func(p *IgnoreDirectoryProof) { p.MissingDirectory = true },
	} {
		t.Run(name, func(t *testing.T) {
			p := child
			change(&p)
			if ValidateIgnoreDirectoryProof(p) != ErrInvalid {
				t.Fatal("invalid shape accepted")
			}
		})
	}
	present := child
	present.RulePresent = true
	present.RuleIdentity = [32]byte{3}
	present.RuleSHA256 = [32]byte{4}
	present.RuleSize = IgnoreRuleMaxBytes
	present.RuleModifiedNano = -1
	if ValidateIgnoreDirectoryProof(present) != nil {
		t.Fatal("valid present proof rejected")
	}
	present.RuleSize++
	if ValidateIgnoreDirectoryProof(present) != ErrInvalid {
		t.Fatal("oversized rule accepted")
	}
	root.MissingDirectory = true
	root.Identity = [32]byte{}
	if ValidateIgnoreDirectoryProof(root) != ErrInvalid {
		t.Fatal("missing root accepted")
	}
}

func TestIgnoreManifestValuesArePrivate(t *testing.T) {
	for _, value := range []any{IgnoreDirectoryProof{Directory: "private-path"}, IgnoreProofCursor{Directory: "private-path"}} {
		data, err := json.Marshal(value)
		if err != nil || string(data) != "{}" {
			t.Fatal("proof or cursor JSON leak", err)
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "private-path") {
				t.Fatal("format leak")
			}
		}
	}
}
