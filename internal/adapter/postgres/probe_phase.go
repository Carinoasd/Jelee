package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const probePhaseColumns = `p.job_id::text,p.library_id::text,p.phase,p.tool_version_id::text,encode(t.identity_digest,'hex'),p.scope,COALESCE(p.target_item_id::text,''),p.library_generation,COALESCE(p.target_item_generation,0),p.revision,COALESCE(p.cursor_inventory_id::text,''),p.processed,p.hits,p.negative_hits,p.succeeded,p.failed,p.changed,p.unavailable,p.error_code`

func scanProbePhase(row pgx.Row) (domain.ProbePhase, error) {
	var p domain.ProbePhase
	err := row.Scan(&p.JobID, &p.LibraryID, &p.State, &p.Start.Identity.ID, &p.Start.Identity.Digest, &p.Start.Scope, &p.Start.TargetItemID, &p.LibraryGeneration, &p.TargetItemGeneration, &p.Token.Revision, &p.Token.AfterID, &p.Progress.Processed, &p.Progress.Hits, &p.Progress.NegativeHits, &p.Progress.Succeeded, &p.Progress.Failed, &p.Progress.Changed, &p.Progress.Unavailable, &p.ErrorCode)
	return p, storageError(err)
}
func loadProbePhase(ctx context.Context, tx pgx.Tx, id string) (domain.ProbePhase, error) {
	return scanProbePhase(tx.QueryRow(ctx, `SELECT `+probePhaseColumns+` FROM probe_job_state p JOIN tool_versions t ON t.id=p.tool_version_id WHERE p.job_id=$1::uuid FOR UPDATE OF p`, id))
}
func checkProbePhaseScope(ctx context.Context, tx pgx.Tx, p domain.ProbePhase) error {
	var gen int64
	if err := tx.QueryRow(ctx, `SELECT probe_generation FROM libraries WHERE id=$1::uuid FOR UPDATE`, p.LibraryID).Scan(&gen); err != nil {
		return storageError(err)
	}
	if gen != p.LibraryGeneration {
		return domain.ErrProbeInvalidated
	}
	if p.Start.TargetItemID != "" {
		if err := tx.QueryRow(ctx, `SELECT probe_generation FROM items WHERE id=$1::uuid AND library_id=$2::uuid FOR UPDATE`, p.Start.TargetItemID, p.LibraryID).Scan(&gen); err != nil {
			return storageError(err)
		}
		if gen != p.TargetItemGeneration {
			return domain.ErrProbeInvalidated
		}
	}
	return nil
}
func lockedProbePhase(ctx context.Context, tx pgx.Tx, l domain.JobLease, allowInvalidated bool) (domain.ProbePhase, error) {
	if _, err := readProbePolicy(ctx, tx); err != nil {
		return domain.ProbePhase{}, err
	}
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if current.Job.CancelRequested {
		return domain.ProbePhase{}, context.Canceled
	}
	if !allowInvalidated {
		if err = requireNFOFinished(ctx, tx, l.Job.ID); err != nil {
			return domain.ProbePhase{}, err
		}
	}
	p, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err != nil {
		return p, err
	}
	if !allowInvalidated {
		err = checkProbePhaseScope(ctx, tx, p)
	}
	return p, err
}
func guardProbeParent(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	return guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid AND NOT cancel_requested`, l.Job.ID)
}
func (s *Store) BeginProbePhase(parent context.Context, l domain.JobLease, start domain.ProbePhaseStart) (domain.ProbePhase, error) {
	if err := domain.ValidateProbePhaseStart(start); err != nil {
		return domain.ProbePhase{}, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readProbePolicy(ctx, tx); err != nil {
		return domain.ProbePhase{}, err
	}
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if current.Job.CancelRequested {
		return domain.ProbePhase{}, context.Canceled
	}
	request, requestErr := loadProbeRequest(ctx, tx, l.Job.ID)
	if err = requireNFOFinished(ctx, tx, l.Job.ID); err != nil {
		return domain.ProbePhase{}, err
	}
	if requestErr != nil {
		return domain.ProbePhase{}, requestErr
	}
	if request != nil {
		return domain.ProbePhase{}, domain.ErrConflict
	}
	old, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err == nil {
		if old.Start != start {
			return domain.ProbePhase{}, domain.ErrConflict
		}
		if err = checkProbePhaseScope(ctx, tx, old); err != nil {
			return domain.ProbePhase{}, err
		}
		if err = guardProbeParent(ctx, tx, l); err != nil {
			return domain.ProbePhase{}, err
		}
		return old, storageError(tx.Commit(ctx))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.ProbePhase{}, err
	}
	var pending, identity bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND NOT done),EXISTS(SELECT 1 FROM tool_versions WHERE id=$2::uuid AND identity_digest=$3)`, l.Job.ID, start.Identity.ID, probeBytes(start.Identity.Digest)).Scan(&pending, &identity); err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	if pending {
		return domain.ProbePhase{}, domain.ErrConflict
	}
	if !identity {
		return domain.ProbePhase{}, domain.ErrProbeIdentityMismatch
	}
	if err = ensureProbeLibrary(ctx, tx, current.Job.LibraryID); err != nil {
		return domain.ProbePhase{}, err
	}
	var libraryGeneration, itemGeneration int64
	if start.Scope == domain.ProbeScopeLibraryRebuild {
		err = tx.QueryRow(ctx, `UPDATE libraries SET probe_generation=probe_generation+1 WHERE id=$1::uuid RETURNING probe_generation`, current.Job.LibraryID).Scan(&libraryGeneration)
	} else {
		err = tx.QueryRow(ctx, `SELECT probe_generation FROM libraries WHERE id=$1::uuid FOR UPDATE`, current.Job.LibraryID).Scan(&libraryGeneration)
	}
	if err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	if start.TargetItemID != "" {
		err = tx.QueryRow(ctx, `UPDATE items SET probe_generation=probe_generation+1 WHERE id=$1::uuid AND library_id=$2::uuid RETURNING probe_generation`, start.TargetItemID, current.Job.LibraryID).Scan(&itemGeneration)
		if err != nil {
			return domain.ProbePhase{}, storageError(err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO probe_job_state(job_id,library_id,tool_version_id,phase,scope,library_generation,target_item_id,target_item_generation) VALUES($1::uuid,$2::uuid,$3::uuid,'running',$4,$5,NULLIF($6,'')::uuid,NULLIF($7,0))`, l.Job.ID, current.Job.LibraryID, start.Identity.ID, start.Scope, libraryGeneration, start.TargetItemID, itemGeneration)
	if err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	p, err := loadProbePhase(ctx, tx, l.Job.ID)
	if err != nil {
		return p, err
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return domain.ProbePhase{}, err
	}
	return p, storageError(tx.Commit(ctx))
}

type probeEntry struct {
	domain.ProbeEntry
	libraryGeneration, rootGeneration, itemGeneration int64
	itemID                                            string
}

func requireInventoryPhase(ctx context.Context, tx pgx.Tx, id string) error {
	var begun bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM probe_job_state WHERE job_id=$1::uuid) OR EXISTS(SELECT 1 FROM probe_requests WHERE job_id=$1::uuid AND error_code<>'')`, id).Scan(&begun); err != nil {
		return storageError(err)
	}
	if begun {
		return domain.ErrConflict
	}
	return requireNFOInventoryPhase(ctx, tx, id)
}
func probePrefix(ctx context.Context, tx pgx.Tx, p domain.ProbePhase, limit int) ([]probeEntry, error) {
	rows, err := tx.Query(ctx, `SELECT i.id::text,i.root_id::text,i.path,i.kind,i.size,i.modified_unix_nano,r.path,r.probe_generation,COALESCE(m.item_id::text,''),COALESCE(it.probe_generation,0) FROM job_inventory i JOIN library_roots r ON r.id=i.root_id AND r.library_id=$2::uuid LEFT JOIN media_sources m ON m.root_id=i.root_id AND m.relative_path=i.path LEFT JOIN items it ON it.id=m.item_id WHERE i.job_id=$1::uuid AND i.kind='video' AND i.id>COALESCE(NULLIF($3,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND ($4='' OR m.item_id=NULLIF($4,'')::uuid) ORDER BY i.id LIMIT $5`, p.JobID, p.LibraryID, p.Token.AfterID, p.Start.TargetItemID, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]probeEntry, 0, limit)
	for rows.Next() {
		var e probeEntry
		e.libraryGeneration = p.LibraryGeneration
		if err = rows.Scan(&e.Inventory.ID, &e.Inventory.RootID, &e.Inventory.Path, &e.Inventory.Kind, &e.Inventory.Size, &e.Inventory.ModifiedUnixNano, &e.Source.RootPath, &e.rootGeneration, &e.itemID, &e.itemGeneration); err != nil {
			return nil, storageError(err)
		}
		e.Source.RelativePath = e.Inventory.Path
		result = append(result, e)
	}
	return result, storageError(rows.Err())
}
func (s *Store) NextProbePage(parent context.Context, l domain.JobLease, limit int) (domain.ProbePage, error) {
	if limit < 1 || limit > domain.ProbePageMax {
		return domain.ProbePage{}, domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbePage{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, false)
	if err != nil {
		return domain.ProbePage{}, err
	}
	if p.State != domain.ProbePhaseRunning {
		return domain.ProbePage{}, domain.ErrConflict
	}
	entries, err := probePrefix(ctx, tx, p, limit)
	if err != nil {
		return domain.ProbePage{}, err
	}
	if len(entries) == 0 {
		return domain.ProbePage{}, domain.ErrNotFound
	}
	result := domain.ProbePage{Token: p.Token, Entries: make([]domain.ProbeEntry, 0, len(entries))}
	for _, e := range entries {
		result.Entries = append(result.Entries, e.ProbeEntry)
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return domain.ProbePage{}, err
	}
	return result, storageError(tx.Commit(ctx))
}
func checkProbePrefix(ctx context.Context, tx pgx.Tx, p domain.ProbePhase, token domain.ProbePageToken, candidates []domain.ProbeCandidate, requireStamp bool) ([]probeEntry, error) {
	if p.State != domain.ProbePhaseRunning || p.Token != token {
		return nil, domain.ErrConflict
	}
	entries, err := probePrefix(ctx, tx, p, len(candidates))
	if err != nil {
		return nil, err
	}
	if len(entries) != len(candidates) {
		return nil, domain.ErrConflict
	}
	for i, e := range entries {
		c := candidates[i]
		if e.Inventory.ID != c.InventoryID {
			return nil, domain.ErrConflict
		}
		if requireStamp && (e.Inventory.Size != c.Stamp.Size || e.Inventory.ModifiedUnixNano != c.Stamp.ModifiedUnixNano) {
			return nil, domain.ErrProbeInvalidated
		}
	}
	return entries, nil
}
func (s *Store) FinishProbePhase(parent context.Context, l domain.JobLease) (domain.ProbePhase, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, false)
	if err != nil {
		return p, err
	}
	if p.State == domain.ProbePhaseAborted {
		return domain.ProbePhase{}, domain.ErrConflict
	}
	entries, err := probePrefix(ctx, tx, p, 1)
	if err != nil {
		return domain.ProbePhase{}, err
	}
	if len(entries) != 0 {
		return domain.ProbePhase{}, domain.ErrConflict
	}
	var held bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM probe_cache WHERE lease_job_id=$1::uuid)`, l.Job.ID).Scan(&held); err != nil {
		return domain.ProbePhase{}, storageError(err)
	}
	if held {
		return domain.ProbePhase{}, domain.ErrProbeBusy
	}
	if p.State != domain.ProbePhaseDone {
		_, err = tx.Exec(ctx, `UPDATE probe_job_state SET phase='done',revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID)
		if err != nil {
			return domain.ProbePhase{}, storageError(err)
		}
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return domain.ProbePhase{}, err
	}
	p, err = loadProbePhase(ctx, tx, l.Job.ID)
	if err != nil {
		return p, err
	}
	return p, storageError(tx.Commit(ctx))
}
func (s *Store) AbortProbePhase(parent context.Context, l domain.JobLease, code domain.ProbePhaseError) error {
	if !domain.ValidProbePhaseError(code) {
		return domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedProbePhase(ctx, tx, l, true)
	if err != nil {
		return err
	}
	if p.State == domain.ProbePhaseDone {
		return domain.ErrConflict
	}
	if err = releaseParentProbeLeases(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE probe_job_state SET phase='aborted',error_code=$2,revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID, string(code))
	if err != nil {
		return storageError(err)
	}
	if err = guardProbeParent(ctx, tx, l); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
