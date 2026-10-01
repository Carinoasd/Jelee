package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func validFamilyIgnoreScanBatch(d domain.ScanDirectory, b domain.FamilyIgnoreScanBatch) bool {
	if len(b.Inventory.Entries)+len(b.Inventory.Directories)+len(b.Excluded) > domain.ScanBatchMaxEntries || len(b.LegacyObservations) == 0 || len(b.LegacyObservations) > legacyObservationBatchLimit {
		return false
	}
	custom := domain.IgnoreScanBatch{Inventory: b.Inventory, Proofs: b.CustomProofs, HeldDirectoryIdentity: b.HeldDirectoryIdentity}
	for _, e := range b.Excluded {
		if e.Family == domain.IgnoreFamilyCustom {
			if e.Reason != domain.IgnoreReasonRule {
				return false
			}
			custom.Excluded = append(custom.Excluded, domain.IgnoreScanExclusion{Path: e.Path, Kind: e.Kind, RuleDirectory: e.RuleDirectory, RuleLine: e.RuleLine, MatchedPath: e.MatchedPath})
		}
	}
	if !validIgnoreScanBatch(d, custom) {
		return false
	}
	seen := map[string]bool{}
	dirs := map[string]bool{}
	for _, e := range b.Inventory.Entries {
		seen[e.Path] = true
	}
	for _, p := range b.Inventory.Directories {
		seen[p] = true
		dirs[p] = true
	}
	for _, e := range b.Excluded {
		if !scanChild(d.Path, e.Path) || seen[e.Path] || e.Kind != "directory" && !validInventoryKind(e.Kind) || e.MatchedPath != e.Path {
			return false
		}
		seen[e.Path] = true
		if e.Kind == "directory" {
			dirs[e.Path] = true
		}
	}
	queries := map[string]domain.LegacyIgnoreObservation{}
	for _, o := range b.LegacyObservations {
		if domain.ValidateLegacyIgnoreObservation(o) != nil || o.Proofs[0].RootID != d.RootID {
			return false
		}
		if _, exists := queries[o.Directory]; exists {
			return false
		}
		expected := len(b.CustomProofs)
		if o.Directory != d.Path {
			if !dirs[o.Directory] || !scanChild(d.Path, o.Directory) {
				return false
			}
			expected++
		}
		if len(o.Proofs) != expected {
			return false
		}
		for i, p := range b.CustomProofs {
			q := o.Proofs[i]
			if p.Directory != q.Directory || p.Identity != q.Identity || p.ParentIdentity != q.ParentIdentity {
				return false
			}
		}
		queries[o.Directory] = o
	}
	if _, exists := queries[d.Path]; !exists {
		return false
	}
	for _, e := range b.Excluded {
		switch e.Family {
		case domain.IgnoreFamilyCustom:
			// The original validator checked custom rule provenance above.
		case domain.IgnoreFamilyLegacy:
			switch e.Reason {
			case domain.IgnoreReasonRule:
				if e.RuleLine < 1 || e.RuleLine > 4096 {
					return false
				}
			case domain.IgnoreReasonBlank, domain.IgnoreReasonInvalid:
				if e.RuleLine != 0 {
					return false
				}
			default:
				return false
			}
			query := d.Path
			if e.Kind == "directory" {
				query = e.Path
			}
			o, exists := queries[query]
			if !exists {
				return false
			}
			found := false
			for _, p := range o.Proofs {
				if p.Checked && p.RulePresent && p.Directory == e.RuleDirectory {
					found = true
				}
			}
			if !found {
				return false
			}
		default:
			return false
		}
	}
	return true
}
