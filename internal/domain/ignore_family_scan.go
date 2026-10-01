package domain

const IgnoreModeFamily = "jeleeignore-legacy-v1"
const (
	IgnoreFamilyCustom  = "jeleeignore"
	IgnoreFamilyLegacy  = "legacy-ignore-021"
	IgnoreReasonRule    = "rule"
	IgnoreReasonBlank   = "blank-source"
	IgnoreReasonInvalid = "invalid-source"
)

// FamilyIgnoreExclusion retains the rule family and the empty/all-invalid
// source policies whose provenance has no individual matching line.
type FamilyIgnoreExclusion struct {
	Path          string `json:"-"`
	Kind          string `json:"-"`
	Family        string `json:"-"`
	Reason        string `json:"-"`
	RuleDirectory string `json:"-"`
	RuleLine      int    `json:"-"`
	MatchedPath   string `json:"-"`
}

func (FamilyIgnoreExclusion) String() string   { return "family exclusion (data redacted)" }
func (FamilyIgnoreExclusion) GoString() string { return "family exclusion (data redacted)" }

// Intermediate batches remain provisional until Done and job-wide evidence
// verification. Neither family may lose the held enumeration identity.
type FamilyIgnoreScanBatch struct {
	Inventory             ScanBatch                 `json:"-"`
	CustomProofs          []IgnoreDirectoryProof    `json:"-"`
	LegacyObservations    []LegacyIgnoreObservation `json:"-"`
	HeldDirectoryIdentity [32]byte                  `json:"-"`
	Excluded              []FamilyIgnoreExclusion   `json:"-"`
}

func (FamilyIgnoreScanBatch) String() string   { return "family scan batch (data redacted)" }
func (FamilyIgnoreScanBatch) GoString() string { return "family scan batch (data redacted)" }
