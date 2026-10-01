package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const nfoPhaseColumns = `job_id::text,library_id::text,mode,phase,parser_version,summary_schema_version,fingerprint_version,max_source_bytes,encode(identity_digest,'hex'),library_generation,revision,COALESCE(cursor_inventory_id::text,''),processed,hits,negative_hits,parsed,valid,invalid,warning_files,changed,unavailable,rejected,error_code`

// Loading an old parser identity must remain possible for fenced recovery and
// abort. Executing it is separately rejected by checkNFOPhaseScope.
func loadNFOPhase(ctx context.Context, tx pgx.Tx, id string) (domain.NFOPhase, error) {
	var p domain.NFOPhase
	err := tx.QueryRow(ctx, `SELECT `+nfoPhaseColumns+` FROM nfo_job_state WHERE job_id=$1::uuid FOR UPDATE`, id).Scan(&p.JobID, &p.LibraryID, &p.Mode, &p.State, &p.Identity.ParserVersion, &p.Identity.SummarySchemaVersion, &p.Identity.FingerprintVersion, &p.Identity.MaxSourceBytes, &p.IdentityDigest, &p.LibraryGeneration, &p.Token.Revision, &p.Token.AfterID, &p.Progress.Processed, &p.Progress.Hits, &p.Progress.NegativeHits, &p.Progress.Parsed, &p.Progress.Valid, &p.Progress.Invalid, &p.Progress.WarningFiles, &p.Progress.Changed, &p.Progress.Unavailable, &p.Progress.Rejected, &p.ErrorCode)
	return p, storageError(err)
}
func frozenNFOOff(p domain.NFOPhase) bool {
	return p.Mode == domain.NFOModeOff && p.State == domain.NFOPhaseAborted && p.ErrorCode == domain.NFOPhaseDisabled
}
func checkNFOPhaseScope(ctx context.Context, tx pgx.Tx, p domain.NFOPhase) error {
	digest, err := domain.NFOIdentityDigest(p.Identity)
	if err != nil || digest != p.IdentityDigest {
		return domain.ErrNFOIdentityMismatch
	}
	policy, err := readNFOLibraryPolicy(ctx, tx, p.LibraryID)
	if err != nil {
		return err
	}
	if p.Mode != domain.NFOModeReadOnly || policy.Mode != domain.NFOModeReadOnly || policy.Generation != p.LibraryGeneration {
		return domain.ErrNFOInvalidated
	}
	return nil
}
func lockedNFOPhase(ctx context.Context, tx pgx.Tx, l domain.JobLease, execute bool) (domain.NFOPhase, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if current.Job.CancelRequested {
		return domain.NFOPhase{}, context.Canceled
	}
	if execute {
		if _, err = readNFOCachePolicy(ctx, tx); err != nil {
			return domain.NFOPhase{}, err
		}
	}
	p, err := loadNFOPhase(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if execute {
		if err = checkNFOPhaseScope(ctx, tx, p); err != nil {
			return domain.NFOPhase{}, err
		}
	}
	return p, nil
}
func commitNFOPhase(ctx context.Context, tx pgx.Tx, l domain.JobLease, p domain.NFOPhase) (domain.NFOPhase, error) {
	return commitNFOPhaseBefore(ctx, tx, l, p, nil)
}
func commitNFOPhaseBefore(ctx context.Context, tx pgx.Tx, l domain.JobLease, p domain.NFOPhase, expires *time.Time) (domain.NFOPhase, error) {
	if err := guardProbeParent(ctx, tx, l); err != nil {
		return domain.NFOPhase{}, err
	}
	// Check wall clock after the guarded UPDATE too: a DB trigger can itself wait.
	var live, cacheLive bool
	err := tx.QueryRow(ctx, `SELECT state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() AND NOT cancel_requested,($4::timestamptz IS NULL OR $4::timestamptz>clock_timestamp()) FROM jobs WHERE id=$1::uuid`, l.Job.ID, l.Owner, l.Generation, expires).Scan(&live, &cacheLive)
	if err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	if !live {
		return domain.NFOPhase{}, domain.ErrJobLeaseLost
	}
	if !cacheLive {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	return p, nil
}
func (s *Store) PrepareNFOPhase(parent context.Context, l domain.JobLease, identity domain.NFOIdentity) (domain.NFOPhase, error) {
	digest, err := domain.NFOIdentityDigest(identity)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if current.Job.CancelRequested {
		return domain.NFOPhase{}, context.Canceled
	}
	old, err := loadNFOPhase(ctx, tx, l.Job.ID)
	if err == nil {
		if old.Identity != identity || old.IdentityDigest != digest {
			return domain.NFOPhase{}, domain.ErrNFOIdentityMismatch
		}
		return commitNFOPhase(ctx, tx, l, old)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.NFOPhase{}, err
	}
	var started bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid) OR EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND (done OR parent_path IS NOT NULL))`, l.Job.ID).Scan(&started); err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	if started {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	policy, err := readNFOLibraryPolicy(ctx, tx, current.Job.LibraryID)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	state, code := domain.NFOPhaseWaiting, ""
	if policy.Mode == domain.NFOModeOff {
		state, code = domain.NFOPhaseAborted, string(domain.NFOPhaseDisabled)
	} else {
		if _, err = readNFOCachePolicy(ctx, tx); err != nil {
			return domain.NFOPhase{}, err
		}
		if err = ensureNFOLibrary(ctx, tx, policy.LibraryID); err != nil {
			return domain.NFOPhase{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO nfo_job_state(job_id,library_id,mode,phase,parser_version,summary_schema_version,fingerprint_version,max_source_bytes,identity_digest,library_generation,error_code) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, l.Job.ID, policy.LibraryID, policy.Mode, state, identity.ParserVersion, identity.SummarySchemaVersion, identity.FingerprintVersion, identity.MaxSourceBytes, probeBytes(digest), policy.Generation, code)
	if err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	p, err := loadNFOPhase(ctx, tx, l.Job.ID)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	return commitNFOPhase(ctx, tx, l, p)
}
func (s *Store) LoadNFOPhase(parent context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, false)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	return commitNFOPhase(ctx, tx, l, p)
}
func (s *Store) BeginNFOPhase(parent context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, true)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if p.State == domain.NFOPhaseAborted {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND NOT done)`, l.Job.ID).Scan(&pending); err != nil {
		return domain.NFOPhase{}, storageError(err)
	}
	if pending {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	if p.State == domain.NFOPhaseWaiting {
		if _, err = tx.Exec(ctx, `UPDATE nfo_job_state SET phase='running',revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
			return domain.NFOPhase{}, storageError(err)
		}
		p, err = loadNFOPhase(ctx, tx, l.Job.ID)
		if err != nil {
			return domain.NFOPhase{}, err
		}
	}
	return commitNFOPhase(ctx, tx, l, p)
}

type nfoEntry struct {
	domain.NFOEntry
	rootGeneration int64
}

const nfoPrefixSQL = `SELECT i.id::text,i.root_id::text,i.path,i.kind,i.size,i.modified_unix_nano,r.path,r.nfo_generation FROM job_inventory i JOIN library_roots r ON r.id=i.root_id AND r.library_id=$2::uuid WHERE i.job_id=$1::uuid AND i.kind='nfo' AND i.id>COALESCE(NULLIF($3,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY i.id LIMIT $4`

func nfoPrefix(ctx context.Context, tx pgx.Tx, p domain.NFOPhase, limit int) ([]nfoEntry, error) {
	rows, err := tx.Query(ctx, nfoPrefixSQL, p.JobID, p.LibraryID, p.Token.AfterID, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]nfoEntry, 0, limit)
	for rows.Next() {
		var e nfoEntry
		if err = rows.Scan(&e.Inventory.ID, &e.Inventory.RootID, &e.Inventory.Path, &e.Inventory.Kind, &e.Inventory.Size, &e.Inventory.ModifiedUnixNano, &e.Source.RootPath, &e.rootGeneration); err != nil {
			return nil, storageError(err)
		}
		e.Source.RelativePath = e.Inventory.Path
		result = append(result, e)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}
func (s *Store) NextNFOPage(parent context.Context, l domain.JobLease, limit int) (domain.NFOPage, error) {
	if limit < 1 || limit > domain.NFOPageMax {
		return domain.NFOPage{}, domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPage{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, true)
	if err != nil {
		return domain.NFOPage{}, err
	}
	if p.State != domain.NFOPhaseRunning {
		return domain.NFOPage{}, domain.ErrConflict
	}
	entries, err := nfoPrefix(ctx, tx, p, limit)
	if err != nil {
		return domain.NFOPage{}, err
	}
	if len(entries) == 0 {
		return domain.NFOPage{}, domain.ErrNotFound
	}
	result := domain.NFOPage{Token: p.Token, Entries: make([]domain.NFOEntry, 0, len(entries))}
	for _, e := range entries {
		result.Entries = append(result.Entries, e.NFOEntry)
	}
	if _, err = commitNFOPhase(ctx, tx, l, p); err != nil {
		return domain.NFOPage{}, err
	}
	return result, nil
}
func checkNFOPrefix(ctx context.Context, tx pgx.Tx, p domain.NFOPhase, token domain.NFOPageToken, candidates []domain.NFOCandidate, stamp bool) ([]nfoEntry, error) {
	if p.State != domain.NFOPhaseRunning || p.Token != token {
		return nil, domain.ErrConflict
	}
	entries, err := nfoPrefix(ctx, tx, p, len(candidates))
	if err != nil {
		return nil, err
	}
	if len(entries) != len(candidates) {
		return nil, domain.ErrConflict
	}
	for i, e := range entries {
		c := candidates[i]
		if c.InventoryID != e.Inventory.ID {
			return nil, domain.ErrConflict
		}
		if stamp && (c.Stamp.Size != e.Inventory.Size || c.Stamp.ModifiedUnixNano != e.Inventory.ModifiedUnixNano || c.Stamp.Size > p.Identity.MaxSourceBytes) {
			return nil, domain.ErrNFOInvalidated
		}
	}
	return entries, nil
}
func (s *Store) FinishNFOPhase(parent context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, true)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if p.State != domain.NFOPhaseRunning && p.State != domain.NFOPhaseDone {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	entries, err := nfoPrefix(ctx, tx, p, 1)
	if err != nil {
		return domain.NFOPhase{}, err
	}
	if len(entries) != 0 {
		return domain.NFOPhase{}, domain.ErrConflict
	}
	if p.State != domain.NFOPhaseDone {
		if _, err = tx.Exec(ctx, `UPDATE nfo_job_state SET phase='done',revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
			return domain.NFOPhase{}, storageError(err)
		}
		p, err = loadNFOPhase(ctx, tx, l.Job.ID)
		if err != nil {
			return domain.NFOPhase{}, err
		}
	}
	return commitNFOPhase(ctx, tx, l, p)
}
func (s *Store) AbortNFOPhase(parent context.Context, l domain.JobLease, code domain.NFOPhaseError) error {
	if !domain.ValidNFOPhaseError(code) {
		return domain.ErrInvalid
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	p, err := lockedNFOPhase(ctx, tx, l, false)
	if err != nil {
		return err
	}
	if p.State == domain.NFOPhaseDone {
		return domain.ErrConflict
	}
	if p.State == domain.NFOPhaseAborted {
		if p.ErrorCode != code {
			return domain.ErrConflict
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE nfo_job_state SET phase='aborted',error_code=$2,revision=revision+1,updated_at=clock_timestamp() WHERE job_id=$1::uuid`, l.Job.ID, string(code)); err != nil {
			return storageError(err)
		}
	}
	_, err = commitNFOPhase(ctx, tx, l, p)
	return err
}
func requireNFOInventoryPhase(ctx context.Context, tx pgx.Tx, id string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nfo_job_state WHERE job_id=$1::uuid AND phase<>'waiting' AND NOT(mode='off' AND phase='aborted' AND error_code='nfo_disabled'))`, id).Scan(&blocked); err != nil {
		return storageError(err)
	}
	if blocked {
		return domain.ErrConflict
	}
	return nil
}
func requireNFOFinished(ctx context.Context, tx pgx.Tx, id string) error {
	p, err := loadNFOPhase(ctx, tx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if frozenNFOOff(p) {
		return nil
	}
	if p.State != domain.NFOPhaseDone {
		return domain.ErrConflict
	}
	return checkNFOPhaseScope(ctx, tx, p)
}
