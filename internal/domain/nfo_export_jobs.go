package domain

import (
	"strconv"
	"time"
)

// Batch NFO export (G39.14): one nfo_export job writes the database metadata of
// every item of one library into the item's existing NFO. It adds no write
// path of its own: each chunk of items is prepared like an interactive edit
// and handed to an ordinary child nfo_write job, and the export job waits for
// that child without holding a worker.

// JobNFOExport is the job kind of a batch NFO export.
const JobNFOExport = "nfo_export"

// NFOExportChunkSize is how many preparations one child nfo_write job
// carries. It stays below half of the per-actor preparation limit (32), so an
// administrator's own preparations never starve the export.
const NFOExportChunkSize = 16

// NFOExportCounts classifies the items a run finished with. Written counts
// entries a child job replaced; Failed also covers prepared entries the child
// did not replace.
type NFOExportCounts struct {
	Written        int
	Unchanged      int
	SkippedMissing int
	SkippedInvalid int
	SkippedOther   int
	Failed         int
}

// Add returns the sum of c and o.
func (c NFOExportCounts) Add(o NFOExportCounts) NFOExportCounts {
	return NFOExportCounts{Written: c.Written + o.Written, Unchanged: c.Unchanged + o.Unchanged, SkippedMissing: c.SkippedMissing + o.SkippedMissing,
		SkippedInvalid: c.SkippedInvalid + o.SkippedInvalid, SkippedOther: c.SkippedOther + o.SkippedOther, Failed: c.Failed + o.Failed}
}

// Valid reports whether no counter is negative.
func (c NFOExportCounts) Valid() bool {
	return c.Written >= 0 && c.Unchanged >= 0 && c.SkippedMissing >= 0 && c.SkippedInvalid >= 0 && c.SkippedOther >= 0 && c.Failed >= 0
}

// NFOExportJobState is the public progress of an nfo_export job. CursorItemID
// is null before the first checkpoint and ChildJobID while no nfo_write job is
// outstanding. Chunks counts the child jobs submitted so far.
type NFOExportJobState struct {
	JobID          string    `json:"jobId"`
	LibraryID      string    `json:"libraryId"`
	CursorItemID   *string   `json:"cursorItemId"`
	ChildJobID     *string   `json:"childJobId"`
	Chunks         int       `json:"chunks"`
	Written        int       `json:"written"`
	Unchanged      int       `json:"unchanged"`
	SkippedMissing int       `json:"skippedMissing"`
	SkippedInvalid int       `json:"skippedInvalid"`
	SkippedOther   int       `json:"skippedOther"`
	Failed         int       `json:"failed"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// NFOExportStep is what a run needs to continue. Waiting means the child
// nfo_write job has not finished and the run must yield its worker. Otherwise
// Items are the next item IDs after the cursor and Chunk is the number of
// child jobs already submitted. Actor is the job's administrator, bound to one
// of their live sessions.
type NFOExportStep struct {
	Actor   Actor
	Items   []string
	Chunk   int
	Waiting bool
}

// NFOExportPreparationKeyPrefix is the idempotency key prefix of every
// preparation the chunk of an export job makes. A key names the export job,
// the chunk and the item, so a resumed run replays its own preparations
// instead of making new ones.
func NFOExportPreparationKeyPrefix(job string, chunk int) string {
	return "nfo-export:" + job + ":" + strconv.Itoa(chunk) + ":"
}

// NFOExportPreparationKey is the idempotency key of one item's preparation.
func NFOExportPreparationKey(job string, chunk int, item string) string {
	return NFOExportPreparationKeyPrefix(job, chunk) + item
}

// NFOExportChildKey is the idempotency key of the nfo_write job of one chunk.
func NFOExportChildKey(job string, chunk int) string {
	return "nfo-export:" + job + ":" + strconv.Itoa(chunk)
}
