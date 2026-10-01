package domain

// IgnoreScanExclusion is private provenance for one enumerated child. It does
// not assert anything about old baseline attributes or an unvisited subtree.
type IgnoreScanExclusion struct {
	Path          string `json:"-"`
	Kind          string `json:"-"`
	RuleDirectory string `json:"-"`
	RuleLine      int    `json:"-"`
	MatchedPath   string `json:"-"`
}

func (IgnoreScanExclusion) String() string   { return "ignore scan exclusion (data redacted)" }
func (IgnoreScanExclusion) GoString() string { return "ignore scan exclusion (data redacted)" }

type IgnoreScanBatch struct {
	Inventory             ScanBatch              `json:"-"`
	Proofs                []IgnoreDirectoryProof `json:"-"`
	HeldDirectoryIdentity [32]byte               `json:"-"`
	Excluded              []IgnoreScanExclusion  `json:"-"`
}

func (IgnoreScanBatch) String() string   { return "ignore scan batch (data redacted)" }
func (IgnoreScanBatch) GoString() string { return "ignore scan batch (data redacted)" }
