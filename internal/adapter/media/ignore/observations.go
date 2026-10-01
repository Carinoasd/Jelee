package ignoresource

import (
	"crypto/sha256"
	"encoding/binary"
)

// DirectoryProof describes an opened directory and its fixed .jeleeignore
// leaf, or a confirmed missing child directory under the preceding opened
// parent. MissingDirectory carries no identity or rule fields of its own.
//
// These are private worker values, not public API DTOs or filesystem authority.
// The root is named "."; all other paths are root-relative. ParentIdentity is
// zero only for the root. No rule text or absolute path is retained.
type DirectoryProof struct {
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

func (DirectoryProof) String() string   { return "ignore directory proof (data redacted)" }
func (DirectoryProof) GoString() string { return "ignore directory proof (data redacted)" }

// DirectoryProofs returns an owned copy of this observation's reachable chain,
// ordered root first. The resolver has re-opened and verified every element
// before publication, even on a cache hit. It does not observe excluded
// descendants, prove a later ReadDir uses the same handle, or seal a whole scan.
// EvaluateBaseline can append one confirmed missing ancestor after the chain.
// A failed or zero observation returns nil. The chain is bounded by the same
// path-component limit as Evaluate; calling this method performs no I/O.
func (o Observation) DirectoryProofs() []DirectoryProof {
	if len(o.chain) == 0 {
		return nil
	}
	proofs := make([]DirectoryProof, len(o.chain))
	for i, entry := range o.chain {
		name := entry.path
		if name == "" {
			name = "."
		}
		p := DirectoryProof{Directory: name, Identity: entry.identity,
			RulePresent: entry.source.present, RuleIdentity: entry.source.state.identity,
			RuleSize: entry.source.state.size, RuleModifiedNano: entry.source.state.modifiedUnixNano,
			RuleSHA256: entry.source.digest}
		if i > 0 {
			p.ParentIdentity = o.chain[i-1].identity
		}
		proofs[i] = p
	}
	if o.missing != nil {
		proofs = append(proofs, *o.missing)
	}
	return proofs
}

func missingObservationToken(prior [32]byte, p DirectoryProof) [32]byte {
	b := append([]byte("jelee-ignore-missing-directory-v1"), prior[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(len(p.Directory)))
	b = append(b, p.Directory...)
	b = append(b, p.ParentIdentity[:]...)
	return sha256.Sum256(b)
}
