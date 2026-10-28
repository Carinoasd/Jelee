package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ErrNFOExportYield ends a run that has handed its work to a child nfo_write
// job (or found one still outstanding). It is not a failure: the caller
// returns the job to the queue, from where it is claimed again once the child
// is terminal. The run never waits for the child inside a worker.
var ErrNFOExportYield = errors.New("nfo export yields to its write job")

// nfoExportScanBatch is how many item IDs one step reads.
const nfoExportScanBatch = 50

// nfoExportBackups is how many backups each prepared write keeps, like the
// interactive preparation.
const nfoExportBackups = 3

// NFOExportExecutionRepository is the durable side of an nfo_export run. Every
// method is fenced by the job lease.
type NFOExportExecutionRepository interface {
	// NextNFOExportStep folds a finished child job into the counters and
	// returns up to limit item IDs after the cursor, in ascending order, with
	// the job's actor. While the child is queued or running it only reports
	// Waiting. It answers context.Canceled once cancellation was requested and
	// domain.ErrForbidden when the actor is no longer an active administrator
	// with a live session.
	NextNFOExportStep(ctx context.Context, lease domain.JobLease, limit int) (domain.NFOExportStep, error)
	// RecordNFOExportBatch moves the cursor and adds counts of items that
	// needed no write.
	RecordNFOExportBatch(ctx context.Context, lease domain.JobLease, cursor string, counts domain.NFOExportCounts) error
	// SubmitNFOExportChunk creates the child nfo_write job of the chunk's
	// preparations and records it with the cursor and counts in one
	// transaction.
	SubmitNFOExportChunk(ctx context.Context, lease domain.JobLease, actor domain.Actor, chunk int, cursor string, counts domain.NFOExportCounts, preparations []string) error
	// YieldNFOExport returns the job to the queue without consuming an attempt.
	YieldNFOExport(ctx context.Context, lease domain.JobLease) error
	FinishNFOExport(ctx context.Context, lease domain.JobLease, state, code string) error
}

// NFOExportPreparer makes the controlled edit of one item, like the
// interactive preparation. Equal keys and requests return the first result.
type NFOExportPreparer interface {
	Prepare(ctx context.Context, actor domain.Actor, key string, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, bool, error)
}

var _ NFOExportPreparer = (*NFOWritePreparations)(nil)

// NFOExportWorker runs one nfo_export job. It reads each item's database
// metadata and its NFO, prepares a controlled edit for the fields that
// differ, and lets an ordinary nfo_write job write the files; it has no file
// access of its own.
type NFOExportWorker struct {
	metadata     *Metadata
	preparations NFOExportPreparer
	repo         NFOExportExecutionRepository
	dbTimeout    time.Duration
}

// NewNFOExportWorker requires a Metadata that can read item NFO. dbTimeout
// bounds each repository call; zero uses two seconds.
func NewNFOExportWorker(metadata *Metadata, preparations NFOExportPreparer, repository NFOExportExecutionRepository, dbTimeout time.Duration) (*NFOExportWorker, error) {
	if metadata == nil || metadata.nfoFields == nil || preparations == nil || repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if _, ok := metadata.items.(NFOItemScopeRepository); !ok || !metadata.HasItemMetadata() {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &NFOExportWorker{metadata: metadata, preparations: preparations, repo: repository, dbTimeout: dbTimeout}, nil
}

func (w *NFOExportWorker) db(ctx context.Context, call func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, w.dbTimeout)
	defer cancel()
	return call(c)
}

// Run advances the export by one child job at most. It returns nil when every
// item is done, ErrNFOExportYield after it submitted a child job or found one
// outstanding, and any other error when the run must stop; the cursor then
// stays at the last committed checkpoint. An item that fails locally never
// ends the run; storage, lease, authorization and context errors do.
//
// The items of a chunk are exported from the cursor on until the chunk holds
// NFOExportChunkSize preparations. Items that need no write are recorded in
// the same transaction that submits the child, or on their own when no child
// is needed, so a resumed run never counts an item twice.
func (w *NFOExportWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobNFOExport {
		return domain.ErrInvalid
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var step domain.NFOExportStep
		if err := w.db(ctx, func(c context.Context) (err error) {
			step, err = w.repo.NextNFOExportStep(c, lease, nfoExportScanBatch)
			return err
		}); err != nil {
			return err
		}
		if step.Waiting {
			return ErrNFOExportYield
		}
		if len(step.Items) == 0 {
			return nil
		}
		var counts domain.NFOExportCounts
		var prepared []string
		last := ""
		var stop error
		for _, item := range step.Items {
			if len(prepared) >= domain.NFOExportChunkSize {
				break
			}
			if stop = ctx.Err(); stop != nil {
				break
			}
			outcome, id, err := w.exportItem(ctx, step.Actor, lease.Job.ID, step.Chunk+1, item)
			if errors.Is(err, domain.ErrNFOCacheCapacity) && len(prepared) > 0 {
				// The preparation store is full: submit what is ready and
				// come back for this item.
				break
			}
			if err != nil {
				stop = err
				break
			}
			switch outcome {
			case nfoExportPrepared:
				prepared = append(prepared, id)
			case nfoExportUnchanged:
				counts.Unchanged++
			case nfoExportMissing:
				counts.SkippedMissing++
			case nfoExportInvalid:
				counts.SkippedInvalid++
			case nfoExportSkipped:
				counts.SkippedOther++
			default:
				counts.Failed++
			}
			last = item
		}
		if stop == nil && len(prepared) > 0 {
			if err := w.db(ctx, func(c context.Context) error {
				return w.repo.SubmitNFOExportChunk(c, lease, step.Actor, step.Chunk+1, last, counts, prepared)
			}); err != nil {
				return err
			}
			return ErrNFOExportYield
		}
		if last != "" && len(prepared) == 0 {
			// Finished items are recorded even when the run stops, so a
			// cancelled job keeps an exact count and a resume never repeats
			// them. Items of an abandoned chunk are not: their preparations
			// are replayed by key when the run resumes.
			recordCtx := context.WithoutCancel(ctx)
			if err := w.db(recordCtx, func(c context.Context) error {
				return w.repo.RecordNFOExportBatch(c, lease, last, counts)
			}); err != nil {
				return err
			}
		}
		if stop != nil {
			return stop
		}
	}
}

type nfoExportOutcome int

const (
	nfoExportPrepared nfoExportOutcome = iota
	nfoExportUnchanged
	nfoExportMissing
	nfoExportInvalid
	nfoExportSkipped
	nfoExportFailed
)

// exportItem reads the item's database metadata and its NFO and, when they
// differ, prepares the edit. A revision or source conflict is retried once
// with fresh state. The error is non-nil only when the run must stop (or the
// preparation store is full, which Run handles).
func (w *NFOExportWorker) exportItem(ctx context.Context, actor domain.Actor, job string, chunk int, item string) (nfoExportOutcome, string, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var before domain.ItemMetadata
		if before, err = w.metadata.ItemFields(ctx, actor, item); err != nil {
			break
		}
		var scope domain.NFOItemScope
		var read nfoItemRead
		if scope, read, err = w.metadata.readItemNFOStates(ctx, actor, item, before.Revision); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			break
		}
		outcome, request := planNFOExport(before, scope, read)
		if request == nil {
			return outcome, "", nil
		}
		var preparation domain.NFOWritePreparation
		preparation, _, err = w.preparations.Prepare(ctx, actor, domain.NFOExportPreparationKey(job, chunk, item), *request)
		if err == nil {
			return nfoExportPrepared, preparation.ID, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			break
		}
	}
	return classifyNFOExportError(ctx, err)
}

// classifyNFOExportError maps an error of reading or preparing one item. Items
// without a usable scope (no media source, unsupported kind, deleted) are
// skipped, other item-local errors are failures, and anything that is not
// about this item stops the run.
func classifyNFOExportError(ctx context.Context, err error) (nfoExportOutcome, string, error) {
	switch {
	case ctx.Err() != nil:
		return nfoExportFailed, "", ctx.Err()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, domain.ErrJobLeaseLost), errors.Is(err, domain.ErrDatabase),
		errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, domain.ErrForbidden),
		errors.Is(err, domain.ErrNFOCacheCapacity):
		return nfoExportFailed, "", err
	case errors.Is(err, domain.ErrMetadataUnavailable), errors.Is(err, domain.ErrNotFound):
		return nfoExportSkipped, "", nil
	}
	return nfoExportFailed, "", nil
}

// nfoExportField pairs a database field with the NFO element the controlled
// edit writes it to.
type nfoExportField struct {
	field string
	fact  bool
	edit  string
}

// nfoExportFields are the fields the preparation edits support, in the order
// they are edited: the text fields of every kind, and the two kind specific
// text facts (an episode's show title, a series' status).
var nfoExportFields = []nfoExportField{
	{"title", false, "title"}, {"originalTitle", false, "originaltitle"}, {"overview", false, "plot"}, {"sortTitle", false, "sorttitle"},
	{"tagline", false, "tagline"}, {"outline", false, "outline"}, {"mpaa", false, "mpaa"}, {"certification", false, "certification"},
	{"showTitle", true, "showtitle"}, {"seriesStatus", true, "status"},
}

// planNFOExport decides what to do with one item. It returns the outcome and,
// for a prepared outcome, the preparation request. Only stored values are
// exported: a title that is merely the item's scanned name (source "existing")
// and any empty value are never written, so an export cannot replace NFO
// content with a placeholder or blank it. Fields the NFO locks (lockdata or a
// lock of the element) are left as they are.
func planNFOExport(before domain.ItemMetadata, scope domain.NFOItemScope, read nfoItemRead) (nfoExportOutcome, *domain.NFOWritePrepareRequest) {
	if read.state == nil {
		return nfoExportSkipped, nil
	}
	switch read.state.Status {
	case domain.NFOItemObservedMissing:
		return nfoExportMissing, nil
	case domain.NFOItemObservedInvalid:
		return nfoExportInvalid, nil
	case domain.NFOItemObservedValid:
	default:
		return nfoExportSkipped, nil
	}
	current := read.state.Selection.Fields
	if current.LockData {
		return nfoExportSkipped, nil
	}
	locked := map[string]bool{}
	for _, name := range current.LockedFields {
		if edit := nfoExportLockedEdit(name); edit != "" {
			locked[edit] = true
		}
	}
	var edits []domain.NFOWriteTextEdit
	skippedLocked := false
	for _, f := range nfoExportFields {
		want, ok := nfoExportDatabaseValue(before, f)
		if !ok || want == "" || want == nfoExportNFOValue(current, f) {
			continue
		}
		if locked[f.edit] {
			skippedLocked = true
			continue
		}
		edits = append(edits, domain.NFOWriteTextEdit{Field: f.edit, Value: want})
	}
	if len(edits) == 0 {
		if skippedLocked {
			return nfoExportSkipped, nil
		}
		return nfoExportUnchanged, nil
	}
	return nfoExportPrepared, &domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: scope.Revision, Edits: edits, CreateMissing: true,
		BOM: "preserve", MaxBytes: domain.NFODefaultSourceBytes, Backups: nfoExportBackups}
}

// nfoExportDatabaseValue is the stored text of f. Fields still on their
// scanned fallback (source "existing") count as absent.
func nfoExportDatabaseValue(m domain.ItemMetadata, f nfoExportField) (string, bool) {
	if !f.fact {
		for _, field := range m.Fields {
			if field.Field == f.field {
				return field.Value, field.Source != "existing"
			}
		}
		return "", false
	}
	for _, fact := range m.Facts {
		if fact.Field != f.field {
			continue
		}
		var value *string
		if json.Unmarshal(fact.Value, &value) != nil || value == nil {
			return "", false
		}
		return *value, fact.Source != "existing"
	}
	return "", false
}

// nfoExportNFOValue is the text the NFO holds for f.
func nfoExportNFOValue(n domain.NFOItemFields, f nfoExportField) string {
	if f.fact {
		switch {
		case f.field == "showTitle" && n.EpisodeDetails != nil:
			return n.EpisodeDetails.ShowTitle
		case f.field == "seriesStatus" && n.SeriesDetails != nil:
			return n.SeriesDetails.Status
		}
		return ""
	}
	for _, field := range n.Fields {
		if field.Field == f.field {
			return field.Value
		}
	}
	return ""
}

// nfoExportLockedEdit names the controlled edit a lock element blocks, like
// the edit itself checks: the Overview, Name and SortName locks map to plot,
// title and sorttitle.
func nfoExportLockedEdit(lock string) string {
	switch lock = strings.ToLower(strings.TrimSpace(lock)); lock {
	case "overview":
		return "plot"
	case "title", "name", "localtitle", "seasonname":
		return "title"
	case "sorttitle", "sortname":
		return "sorttitle"
	case "originaltitle", "plot", "outline", "tagline", "showtitle", "status", "mpaa", "certification":
		return lock
	}
	return ""
}
