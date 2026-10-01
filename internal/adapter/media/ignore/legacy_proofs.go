package ignoresource

const LegacySourceProofVersion = "legacy-nearest-source-v1"

// LegacyDirectoryProof distinguishes an unchecked shadowed ancestor from a
// checked missing leaf. Both carry directory identity, but only Checked=true
// asserts an observation of .ignore. The version is separate from custom rules.
// This is an internal observation projection, not an execution authorization.
type LegacyDirectoryProof struct {
	DirectoryProof `json:"-"`
	Checked        bool   `json:"-"`
	Version        string `json:"-"`
}

func (LegacyDirectoryProof) String() string   { return "legacy directory proof (data redacted)" }
func (LegacyDirectoryProof) GoString() string { return "legacy directory proof (data redacted)" }

func (o LegacyObservation) DirectoryProofs() []LegacyDirectoryProof {
	if len(o.chain) == 0 {
		return nil
	}
	proofs := make([]LegacyDirectoryProof, len(o.chain))
	for i, entry := range o.chain {
		p := DirectoryProof{Directory: entry.path, Identity: entry.identity,
			RulePresent: entry.source.present, RuleIdentity: entry.source.state.identity,
			RuleSize: entry.source.state.size, RuleModifiedNano: entry.source.state.modifiedUnixNano,
			RuleSHA256: entry.source.digest}
		if i > 0 {
			p.ParentIdentity = o.chain[i-1].identity
		}
		proofs[i] = LegacyDirectoryProof{DirectoryProof: p, Checked: entry.checked, Version: LegacySourceProofVersion}
	}
	return proofs
}
