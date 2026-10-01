package domain

import "strings"

const (
	IgnoreBaselineMissing  = "included_missing"
	IgnoreBaselineExcluded = "excluded"
	IgnoreBaselineUnknown  = "unknown"
	IgnoreUnknownSource    = "source_unavailable"
	IgnoreChangedSource    = "source_changed"
	IgnoreUnknownCoverage  = "coverage_unknown"
)

// A token names the exact next raw-baseline prefix, independent of a worker
// lease. Repository methods separately require a live lease on the same job.
type IgnoreBaselineToken struct {
	JobID            string `json:"-"`
	BaselineRevision int64  `json:"-"`
	Sequence         int64  `json:"-"`
	AfterRootID      string `json:"-"`
	AfterPath        string `json:"-"`
}

func (IgnoreBaselineToken) String() string   { return "ignore baseline token (data redacted)" }
func (IgnoreBaselineToken) GoString() string { return "ignore baseline token (data redacted)" }

type IgnoreBaselineCandidate struct {
	RootID string `json:"-"`
	Path   string `json:"-"`
}

func (IgnoreBaselineCandidate) String() string   { return "ignore baseline candidate (data redacted)" }
func (IgnoreBaselineCandidate) GoString() string { return "ignore baseline candidate (data redacted)" }

type IgnoreBaselinePage struct {
	Token    IgnoreBaselineToken       `json:"-"`
	Unseen   []IgnoreBaselineCandidate `json:"-"`
	RawCount int                       `json:"-"`
	End      bool                      `json:"-"`
	Complete bool                      `json:"-"`
}

func (IgnoreBaselinePage) String() string   { return "ignore baseline page (data redacted)" }
func (IgnoreBaselinePage) GoString() string { return "ignore baseline page (data redacted)" }

// IgnoreBaselineDecision classifies an unseen old path. It is private worker
// data; validation checks shape and provenance relationships, not the truth of
// a filesystem observation. Persistence must bind it to the exact pending page
// and to a retained rule proof before accepting it.
type IgnoreBaselineDecision struct {
	RootID        string `json:"-"`
	Path          string `json:"-"`
	Outcome       string `json:"-"`
	RuleDirectory string `json:"-"`
	RuleLine      int    `json:"-"`
	MatchedPath   string `json:"-"`
	Reason        string `json:"-"`
}

func (IgnoreBaselineDecision) String() string   { return "ignore baseline decision (data redacted)" }
func (IgnoreBaselineDecision) GoString() string { return "ignore baseline decision (data redacted)" }

func ValidateIgnoreBaselineDecision(d IgnoreBaselineDecision) error {
	if !ValidID(d.RootID) || !ValidNFOObservationPath(d.Path) {
		return ErrInvalid
	}
	if d.Outcome != IgnoreBaselineExcluded && (d.RuleDirectory != "" || d.RuleLine != 0 || d.MatchedPath != "") {
		return ErrInvalid
	}
	switch d.Outcome {
	case IgnoreBaselineMissing:
		if d.Reason != "" {
			return ErrInvalid
		}
	case IgnoreBaselineExcluded:
		if d.Reason != "" || d.RuleLine < 1 || d.RuleLine > 4096 ||
			(d.RuleDirectory != "." && !ValidNFOObservationPath(d.RuleDirectory)) || !ValidNFOObservationPath(d.MatchedPath) {
			return ErrInvalid
		}
		// The rule belongs to a strict ancestor of its matched path. A rule
		// inside a directory cannot decide whether to enter that directory.
		if d.RuleDirectory != "." && !strings.HasPrefix(d.MatchedPath, d.RuleDirectory+"/") {
			return ErrInvalid
		}
		if d.Path != d.MatchedPath && !strings.HasPrefix(d.Path, d.MatchedPath+"/") {
			return ErrInvalid
		}
	case IgnoreBaselineUnknown:
		if d.Reason != IgnoreUnknownSource && d.Reason != IgnoreChangedSource && d.Reason != IgnoreUnknownCoverage {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// IgnoreComparisonCounts describes only old baseline rows. New inventory rows
// never enter these counts. It carries no authority to publish a baseline.
type IgnoreComparisonCounts struct {
	Observed int64
	Missing  int64
	Excluded int64
	Unknown  int64
}

type IgnoreComparisonResult struct {
	Missing            int64
	Denominator        int64
	ComparisonComplete bool
	ReviewRequired     bool
}

// CompareIgnoreBaseline keeps uncertainty separate from a complete new scope.
// A complete observation may reset an incomparable old scope without claiming
// missing files. Unknown current evidence always blocks publication, even on
// that reset. SQL must prove complete coverage and derive all old-row counts.
func CompareIgnoreBaseline(c IgnoreComparisonCounts, currentComplete, oldScopeComparable bool, countLimit, percentLimit int) (IgnoreComparisonResult, error) {
	if countLimit < 1 || countLimit > 500000 || percentLimit < 1 || percentLimit > 100 {
		return IgnoreComparisonResult{}, ErrInvalid
	}
	var total int64
	for _, n := range []int64{c.Observed, c.Missing, c.Excluded, c.Unknown} {
		if n < 0 || n > 500000 {
			return IgnoreComparisonResult{}, ErrInvalid
		}
		total += n
	}
	if total > 500000 {
		return IgnoreComparisonResult{}, ErrScanLimit
	}
	if !currentComplete || c.Unknown > 0 {
		return IgnoreComparisonResult{ReviewRequired: true}, nil
	}
	if !oldScopeComparable {
		return IgnoreComparisonResult{}, nil
	}
	denominator := c.Observed + c.Missing
	review := c.Missing > 0 && (c.Missing >= int64(countLimit) || c.Missing*100 >= denominator*int64(percentLimit))
	return IgnoreComparisonResult{Missing: c.Missing, Denominator: denominator, ComparisonComplete: true, ReviewRequired: review}, nil
}
