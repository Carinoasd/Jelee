package domain

import "time"

// Batch NFO apply (G39.14): one nfo_apply job applies the NFO fields of every
// item of one library through the per-item apply path, in ascending item ID
// order, and records a resumable cursor with its counters.

// JobNFOApply is the job kind of a batch NFO apply.
const JobNFOApply = "nfo_apply"

// NFOApplyBatchSize is how many items one checkpoint covers.
const NFOApplyBatchSize = 50

// NFOApplyCounts classifies the items a run finished with.
type NFOApplyCounts struct {
	Applied   int
	Unchanged int
	Skipped   int
	Failed    int
}

// Add returns the sum of c and o.
func (c NFOApplyCounts) Add(o NFOApplyCounts) NFOApplyCounts {
	return NFOApplyCounts{Applied: c.Applied + o.Applied, Unchanged: c.Unchanged + o.Unchanged, Skipped: c.Skipped + o.Skipped, Failed: c.Failed + o.Failed}
}

// NFOApplyJobState is the public progress of an nfo_apply job. CursorItemID is
// null until the first checkpoint.
type NFOApplyJobState struct {
	JobID        string    `json:"jobId"`
	LibraryID    string    `json:"libraryId"`
	CursorItemID *string   `json:"cursorItemId"`
	Applied      int       `json:"applied"`
	Unchanged    int       `json:"unchanged"`
	Skipped      int       `json:"skipped"`
	Failed       int       `json:"failed"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// NFOApplyBatch is the next slice of a job's items after its cursor. Actor is
// the job's administrator, bound to one of their live sessions so the item
// apply path authorizes it like an interactive request.
type NFOApplyBatch struct {
	Actor Actor
	Items []string
}
