package postgres

import (
	"sort"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const legacyObservationBatchLimit = 129

type legacyObservationBatch struct {
	root       string
	names      []string
	proofs     map[string]domain.LegacyIgnoreDirectoryProof
	queries    map[string]*string
	queryNames []string
	conflict   bool
}

// Merge shared ancestors before accessing storage. Query boundaries are kept
// separately: checking an ancestor for one query must not unshadow another.
func prepareLegacyObservations(observations []domain.LegacyIgnoreObservation) (legacyObservationBatch, error) {
	b := legacyObservationBatch{proofs: map[string]domain.LegacyIgnoreDirectoryProof{}, queries: map[string]*string{}}
	if len(observations) == 0 || len(observations) > legacyObservationBatchLimit {
		return b, domain.ErrInvalid
	}
	for _, o := range observations {
		if err := domain.ValidateLegacyIgnoreObservation(o); err != nil {
			return b, err
		}
		root := o.Proofs[0].RootID
		if b.root == "" {
			b.root = root
		} else if b.root != root {
			return b, domain.ErrInvalid
		}
		var selected *string
		for _, p := range o.Proofs {
			if p.Checked && p.RulePresent {
				name := p.Directory
				selected = &name
			}
			old, exists := b.proofs[p.Directory]
			if exists && !domain.LegacyIgnoreProofsCompatible(old, p) {
				b.conflict = true
			}
			if !exists {
				b.names = append(b.names, p.Directory)
			}
			if !exists || !old.Checked && p.Checked {
				b.proofs[p.Directory] = p
			}
		}
		if old, exists := b.queries[o.Directory]; exists {
			if !sameLegacySelection(old, selected) {
				b.conflict = true
			}
		} else {
			b.queries[o.Directory] = selected
			b.queryNames = append(b.queryNames, o.Directory)
		}
	}
	// Prefix order places parents before children for the foreign key checks.
	sort.Strings(b.names)
	sort.Strings(b.queryNames)
	return b, nil
}

func sameLegacySelection(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
