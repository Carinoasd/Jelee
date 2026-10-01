package domain

import (
	"path"
	"strings"
)

const (
	IgnoreManifestMaxRows  = 16384
	IgnoreManifestMaxBytes = 64 << 20
	IgnoreRuleMaxBytes     = 256 << 10
	IgnoreProofPageSize    = 128
)

// IgnoreDirectoryProof is private worker evidence. It does not grant access to
// files. MissingDirectory requires proof of child absence under an opened
// parent; an unreadable directory is never represented by this value.
type IgnoreDirectoryProof struct {
	RootID           string   `json:"-"`
	Directory        string   `json:"-"`
	ParentIdentity   [32]byte `json:"-"`
	Identity         [32]byte `json:"-"`
	MissingDirectory bool     `json:"-"`
	RulePresent      bool     `json:"-"`
	RuleIdentity     [32]byte `json:"-"`
	RuleSize         int64    `json:"-"`
	RuleModifiedNano int64    `json:"-"`
	RuleSHA256       [32]byte `json:"-"`
}

func (IgnoreDirectoryProof) String() string   { return "ignore proof (data redacted)" }
func (IgnoreDirectoryProof) GoString() string { return "ignore proof (data redacted)" }

func ValidateIgnoreDirectoryProof(p IgnoreDirectoryProof) error {
	zero := [32]byte{}
	if !ValidID(p.RootID) || p.Directory != "." && !ValidNFOObservationPath(p.Directory) || strings.Count(p.Directory, "/") >= 128 {
		return ErrInvalid
	}
	if (p.Directory == ".") != (p.ParentIdentity == zero) || p.MissingDirectory && (p.Directory == "." || p.Identity != zero || p.RulePresent) || !p.MissingDirectory && p.Identity == zero {
		return ErrInvalid
	}
	if p.RulePresent {
		if p.RuleIdentity == zero || p.RuleSHA256 == zero || p.RuleSize < 0 || p.RuleSize > IgnoreRuleMaxBytes {
			return ErrInvalid
		}
	} else if p.RuleIdentity != zero || p.RuleSHA256 != zero || p.RuleSize != 0 || p.RuleModifiedNano != 0 {
		return ErrInvalid
	}
	return nil
}

func IgnoreProofParentMatches(child, parent IgnoreDirectoryProof) bool {
	return ValidateIgnoreDirectoryProof(child) == nil && ValidateIgnoreDirectoryProof(parent) == nil &&
		child.Directory != "." && child.RootID == parent.RootID && path.Dir(child.Directory) == parent.Directory &&
		!parent.MissingDirectory && child.ParentIdentity == parent.Identity
}

// Charge is deterministic accounting, not a measurement of PostgreSQL disk use.
func (p IgnoreDirectoryProof) Charge() int64 { return int64(256 + len(p.Directory)) }

type IgnoreProofCursor struct {
	RootID    string `json:"-"`
	Directory string `json:"-"`
}

func (IgnoreProofCursor) String() string   { return "ignore cursor (data redacted)" }
func (IgnoreProofCursor) GoString() string { return "ignore cursor (data redacted)" }
