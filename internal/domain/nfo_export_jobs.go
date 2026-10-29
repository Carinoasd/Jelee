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

// The modes of an nfo_export job. NFOExportAll writes the stored values into
// existing NFOs; NFOExportMissing creates the NFO of every item that has none
// and leaves existing NFOs alone; NFOExportFix repairs the few validation
// issues that can be repaired without guessing (G39.11) and touches nothing
// else.
const (
	NFOExportAll     = "all"
	NFOExportMissing = "missing"
	NFOExportFix     = "fix"
)

// ValidNFOExportMode reports whether mode names an export mode.
func ValidNFOExportMode(mode string) bool {
	return mode == NFOExportAll || mode == NFOExportMissing || mode == NFOExportFix
}

// NFOExportChunkSize is how many preparations one child nfo_write job
// carries. It stays below half of the per-actor preparation limit (32), so an
// administrator's own preparations never starve the export.
const NFOExportChunkSize = 16

// NFOExportCounts classifies the items a run finished with. Written counts
// entries a child job replaced; Failed also covers prepared entries the child
// did not replace. A fix-mode run counts the entries its child replaced as
// Fixed instead (Written stays zero), and the items that have a validation
// issue it does not repair as Unfixable.
type NFOExportCounts struct {
	Written         int
	Fixed           int
	Unfixable       int
	Unchanged       int
	SkippedExisting int
	SkippedMissing  int
	SkippedInvalid  int
	SkippedOther    int
	Failed          int
}

// Add returns the sum of c and o.
func (c NFOExportCounts) Add(o NFOExportCounts) NFOExportCounts {
	return NFOExportCounts{Written: c.Written + o.Written, Fixed: c.Fixed + o.Fixed, Unfixable: c.Unfixable + o.Unfixable, Unchanged: c.Unchanged + o.Unchanged, SkippedExisting: c.SkippedExisting + o.SkippedExisting, SkippedMissing: c.SkippedMissing + o.SkippedMissing,
		SkippedInvalid: c.SkippedInvalid + o.SkippedInvalid, SkippedOther: c.SkippedOther + o.SkippedOther, Failed: c.Failed + o.Failed}
}

// Valid reports whether no counter is negative.
func (c NFOExportCounts) Valid() bool {
	return c.Written >= 0 && c.Fixed >= 0 && c.Unfixable >= 0 && c.Unchanged >= 0 && c.SkippedExisting >= 0 && c.SkippedMissing >= 0 && c.SkippedInvalid >= 0 && c.SkippedOther >= 0 && c.Failed >= 0
}

// NFOExportJobState is the public progress of an nfo_export job. CursorItemID
// is null before the first checkpoint and ChildJobID while no nfo_write job is
// outstanding. Chunks counts the child jobs submitted so far.
type NFOExportJobState struct {
	JobID           string    `json:"jobId"`
	LibraryID       string    `json:"libraryId"`
	Mode            string    `json:"mode"`
	CursorItemID    *string   `json:"cursorItemId"`
	ChildJobID      *string   `json:"childJobId"`
	Chunks          int       `json:"chunks"`
	Written         int       `json:"written"`
	Fixed           int       `json:"fixed"`
	Unfixable       int       `json:"unfixable"`
	Unchanged       int       `json:"unchanged"`
	SkippedExisting int       `json:"skippedExisting"`
	SkippedMissing  int       `json:"skippedMissing"`
	SkippedInvalid  int       `json:"skippedInvalid"`
	SkippedOther    int       `json:"skippedOther"`
	Failed          int       `json:"failed"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// NFOExportStep is what a run needs to continue. Waiting means the child
// nfo_write job has not finished and the run must yield its worker. Otherwise
// Items are the next item IDs after the cursor and Chunk is the number of
// child jobs already submitted. Actor is the job's administrator, bound to one
// of their live sessions.
//
// Mode is the job's export mode. In missing mode a finished child that left
// entries unreplaced is not folded in by the repository alone: Settle then
// names the candidates whose target the run must look at (see
// NFOExportSettle) and no item is offered until it settled the child.
type NFOExportStep struct {
	Actor   Actor
	Items   []string
	Chunk   int
	Waiting bool
	Mode    string
	Settle  *NFOExportSettle
}

// NFOExportSettle is a terminal child of a missing-mode export that has
// unreplaced entries. Candidates are the items of those entries whose create
// may have been refused because the target already existed: the journal shows
// no created file of this job (no backup phase, or the token was rolled
// back). The run reports how many of them have an NFO now; the rest of the
// unreplaced entries are failures.
type NFOExportSettle struct {
	Candidates []string
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
