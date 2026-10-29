package domain

import "slices"

// The outcomes of validating the NFO an item selects (G39.11).
const (
	// NFOItemValidationMissing: the item has no NFO file.
	NFOItemValidationMissing = "missing"
	// NFOItemValidationPath: the item's NFO location is illegal (PathCode).
	NFOItemValidationPath = "path"
	// NFOItemValidationChecked: the NFO was read and validated (Summary).
	NFOItemValidationChecked = "checked"
)

// NFOItemValidation is the result of reading the NFO of one item the way the
// library validation job reads a file: the same parser and the same summary.
// It carries no field values. IssueCodes lists every distinct issue code of
// the whole document, not only the stored prefix of at most NFOIssuesMax, so a
// repair never misses an issue the summary truncated. LockData and
// LockedFields are the NFO's own edit locks.
type NFOItemValidation struct {
	Status       string
	PathCode     string
	Summary      NFOValidationSummary
	IssueCodes   []string
	LockData     bool
	LockedFields []string
}

func (NFOItemValidation) String() string   { return "nfo item validation (data redacted)" }
func (NFOItemValidation) GoString() string { return "nfo item validation (data redacted)" }

// Has reports whether the document has an issue with code.
func (v NFOItemValidation) Has(code string) bool { return slices.Contains(v.IssueCodes, code) }
