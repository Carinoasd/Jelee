package domain

import "errors"

var ErrInventoryInvalidated = errors.New("inventory_invalidated")

// ImageProgress compares inventory kind/size/mtime, never decoded pixels or
// content hashes. Missing is zero when enumeration was incomplete; in that case
// ComparisonComplete is false and other observations may be partial.
type ImageProgress struct {
	Added              int64 `json:"added"`
	Changed            int64 `json:"changed"`
	Unchanged          int64 `json:"unchanged"`
	Missing            int64 `json:"missing"`
	Uncompared         int64 `json:"uncompared"`
	ComparisonComplete bool  `json:"comparisonComplete"`
}

type ImageJobSummary struct {
	JobID     string `json:"jobId"`
	LibraryID string `json:"libraryId"`
	ImageProgress
}

func ValidateImageProgress(v ImageProgress) error {
	for _, count := range []int64{v.Added, v.Changed, v.Unchanged, v.Missing, v.Uncompared} {
		if count < 0 || count > 500000 {
			return ErrInvalid
		}
	}
	if v.Added+v.Changed+v.Unchanged+v.Uncompared > 500000 || v.ComparisonComplete && v.Uncompared != 0 || !v.ComparisonComplete && v.Missing != 0 {
		return ErrInvalid
	}
	return nil
}

// Parent-state checks belong to the repository because this public DTO omits
// that state. Missing is deliberately suppressed for every incomplete comparison,
// including unknown old attributes even if enumeration itself completed.
func ValidateImageJobSummary(v ImageJobSummary) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) {
		return ErrInvalid
	}
	return ValidateImageProgress(v.ImageProgress)
}
