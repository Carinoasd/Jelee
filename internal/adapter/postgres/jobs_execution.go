package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const leaseColumns = jobColumns + `,owner,generation,lease_until,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit`

func scanLease(row pgx.Row) (domain.JobLease, error) {
	var l domain.JobLease
	targets := append(jobScanTargets(&l.Job), &l.Owner, &l.Generation, &l.ExpiresAt, &l.Policy.QueueLimit, &l.Policy.HistoryLimit, &l.Policy.MaxEntries, &l.Policy.MaxDirectories, &l.Policy.MaxAttempts, &l.Policy.MissingCountLimit, &l.Policy.MissingPercentLimit)
	err := row.Scan(targets...)
	return l, storageError(err)
}
func validLeaseDuration(ttl time.Duration) bool { return ttl >= time.Second && ttl <= time.Hour }
func validLease(l domain.JobLease) bool {
	return domain.ValidID(l.Job.ID) && validText(l.Owner, 128, false) && l.Generation > 0
}
func fencedJob(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, error) {
	if !validLease(l) {
		return domain.JobLease{}, domain.ErrJobLeaseLost
	}
	current, err := scanLease(tx.QueryRow(ctx, `SELECT `+leaseColumns+` FROM jobs WHERE id=$1::uuid AND state='running' AND owner=$2 AND generation=$3 AND lease_until>clock_timestamp() FOR UPDATE`, l.Job.ID, l.Owner, l.Generation))
	if errors.Is(err, domain.ErrNotFound) {
		err = domain.ErrJobLeaseLost
	}
	return current, err
}

// Recheck the lease in the final UPDATE, after any batch/baseline work. A lease
// that expires during a transaction must roll back every provisional write.
func guardedJobUpdate(ctx context.Context, tx pgx.Tx, l domain.JobLease, query string, args ...any) error {
	query += fmt.Sprintf(" AND state='running' AND owner=$%d AND generation=$%d AND lease_until>clock_timestamp()", len(args)+1, len(args)+2)
	args = append(args, l.Owner, l.Generation)
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrJobLeaseLost
	}
	return nil
}
func (s *Store) ClaimJob(ctx context.Context, owner string, preferBackground bool, ttl time.Duration) (domain.JobLease, error) {
	return s.ClaimJobWithProbe(ctx, owner, preferBackground, ttl, false)
}
func (s *Store) ClaimJobWithProbe(ctx context.Context, owner string, preferBackground bool, ttl time.Duration, probeCapable bool) (domain.JobLease, error) {
	return s.ClaimJobWithCapabilities(ctx, owner, preferBackground, ttl, domain.ScanCapabilities{Probe: probeCapable})
}
func (s *Store) ClaimJobWithCapabilities(ctx context.Context, owner string, preferBackground bool, ttl time.Duration, capabilities domain.ScanCapabilities) (domain.JobLease, error) {
	if !validText(owner, 128, false) || !validLeaseDuration(ttl) {
		return domain.JobLease{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.JobLease{}, err
	}
	defer tx.Rollback(ctx)
	// Recovery is transactional with claiming. An expired worker can never save
	// after another owner reclaims the run, even if it still holds the old lease.
	if _, err = releaseExpiredProbeLeases(ctx, tx, domain.ProbeSweepMax); err != nil {
		return domain.JobLease{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE jobs SET state=CASE WHEN cancel_requested THEN 'cancelled' WHEN attempts>=max_attempts THEN 'failed' ELSE 'queued' END,error_code=CASE WHEN NOT cancel_requested AND attempts>=max_attempts THEN 'job_attempts_exhausted' ELSE '' END,finished_at=CASE WHEN cancel_requested OR attempts>=max_attempts THEN clock_timestamp() ELSE NULL END,owner=NULL,lease_until=NULL WHERE state='running' AND lease_until<=clock_timestamp() AND kind IN ('inventory_scan','catalog_import')`)
	if err != nil {
		return domain.JobLease{}, storageError(err)
	}
	// An expired NFO write that holds a commit journal cannot be requeued or
	// reassigned (schema49). Stop it so a recovery lease can settle its tokens.
	// Historical schemas without the journal table have nothing to stop.
	var journaled bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('nfo_write_commit_journal') IS NOT NULL`).Scan(&journaled); err != nil {
		return domain.JobLease{}, storageError(err)
	}
	if journaled {
		if _, err = tx.Exec(ctx, `UPDATE jobs SET state=CASE WHEN cancel_requested THEN 'cancelled' ELSE 'failed' END,error_code=CASE WHEN cancel_requested THEN '' ELSE 'job_timeout' END,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE state='running' AND lease_until<=clock_timestamp() AND kind='nfo_write' AND EXISTS(SELECT 1 FROM nfo_write_commit_journal w WHERE w.job_id=jobs.id)`); err != nil {
			return domain.JobLease{}, storageError(err)
		}
	}
	var retention int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(min(history_limit),1) FROM jobs`).Scan(&retention); err != nil {
		return domain.JobLease{}, storageError(err)
	}
	if err = trimJobs(ctx, tx, retention); err != nil {
		return domain.JobLease{}, err
	}
	priority := domain.JobPriorityManual
	if preferBackground {
		priority = domain.JobPriorityBackground
	}
	// Only workers with ignore capability may claim matching enabled contracts.
	l, err := scanLease(tx.QueryRow(ctx, `UPDATE jobs SET state='running',owner=$1,generation=generation+1,attempts=attempts+1,lease_until=clock_timestamp()+$2*interval '1 microsecond',started_at=COALESCE(started_at,clock_timestamp()) WHERE id=(SELECT id FROM jobs WHERE state='queued' AND (kind='inventory_scan' OR ($10 AND kind='catalog_import')) AND ((NOT ignore_requested AND NOT EXISTS(SELECT 1 FROM job_ignore_requests g WHERE g.job_id=jobs.id)) OR ($6 AND ignore_requested AND EXISTS(SELECT 1 FROM job_ignore_requests g WHERE g.job_id=jobs.id AND g.library_id=jobs.library_id AND g.mode='jeleeignore' AND g.case_mode IN ('sensitive','ascii-insensitive') AND g.program_version=$7 AND g.proof_version=$8)) OR ($9 AND ignore_requested AND EXISTS(SELECT 1 FROM job_ignore_requests g WHERE g.job_id=jobs.id AND g.library_id=jobs.library_id AND g.mode='jeleeignore-legacy-v1' AND g.case_mode IN ('sensitive','ascii-insensitive') AND g.program_version='jeleeignore-legacy-v1' AND g.proof_version='jeleeignore-legacy-proof-v1'))) AND ($4 OR NOT EXISTS(SELECT 1 FROM probe_requests r WHERE r.job_id=jobs.id)) AND ($5 OR NOT EXISTS(SELECT 1 FROM nfo_job_requests n WHERE n.job_id=jobs.id AND n.requested)) AND NOT EXISTS(SELECT 1 FROM nfo_job_state n WHERE n.job_id=jobs.id AND n.mode='read-only' AND NOT EXISTS(SELECT 1 FROM nfo_job_requests r WHERE r.job_id=jobs.id)) ORDER BY CASE WHEN priority=$3 THEN 0 ELSE 1 END,created_at,id LIMIT 1 FOR UPDATE) RETURNING `+leaseColumns, owner, ttl.Microseconds(), priority, capabilities.Probe, capabilities.NFO, capabilities.Ignore, domain.IgnoreProgramVersion, domain.IgnoreProofVersion, capabilities.FamilyIgnore, capabilities.CatalogImport))
	if errors.Is(err, domain.ErrNotFound) {
		if e := tx.Commit(ctx); e != nil {
			return l, storageError(e)
		}
		return l, domain.ErrNotFound
	}
	if err != nil {
		return l, err
	}
	if err = trimJobs(ctx, tx, l.Policy.HistoryLimit); err != nil {
		return l, err
	}
	return l, storageError(tx.Commit(ctx))
}
func (s *Store) HeartbeatJob(ctx context.Context, l domain.JobLease, ttl time.Duration) (bool, error) {
	if !validLeaseDuration(ttl) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return false, err
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET lease_until=clock_timestamp()+$2*interval '1 microsecond' WHERE id=$1::uuid`, l.Job.ID, ttl.Microseconds()); err != nil {
		return false, err
	}
	if _, err = releaseExpiredProbeLeases(ctx, tx, domain.ProbeSweepMax); err != nil {
		return false, err
	}
	if !current.Job.CancelRequested {
		_, err = tx.Exec(ctx, `UPDATE probe_cache c SET lease_until=LEAST(j.lease_until,clock_timestamp()+q.lease_seconds*interval '1 second') FROM jobs j,probe_cache_quota q WHERE q.singleton AND j.id=$1::uuid AND c.lease_job_id=j.id AND c.lease_job_generation=j.generation AND c.lease_owner=j.owner AND c.lease_until>clock_timestamp()`, l.Job.ID)
		if err != nil {
			return false, storageError(err)
		}
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return false, err
	}
	return current.Job.CancelRequested, storageError(tx.Commit(ctx))
}
func (s *Store) ReleaseJob(ctx context.Context, l domain.JobLease) error {
	return s.releaseJob(ctx, l, false)
}

// PauseJob refunds only the current claim, after its owner has joined all work.
// Fencing prevents replay from refunding an earlier failure or a newer claim.
func (s *Store) PauseJob(ctx context.Context, l domain.JobLease) error {
	return s.releaseJob(ctx, l, true)
}

func (s *Store) releaseJob(ctx context.Context, l domain.JobLease, planned bool) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.Kind == domain.JobNFOWrite {
		return domain.ErrInvalid
	}
	if err = releaseParentProbeLeases(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET state=CASE WHEN cancel_requested THEN 'cancelled' WHEN NOT $2 AND attempts>=max_attempts THEN 'failed' ELSE 'queued' END,error_code=CASE WHEN NOT cancel_requested AND NOT $2 AND attempts>=max_attempts THEN 'job_attempts_exhausted' ELSE '' END,finished_at=CASE WHEN cancel_requested OR (NOT $2 AND attempts>=max_attempts) THEN clock_timestamp() ELSE NULL END,attempts=CASE WHEN $2 AND NOT cancel_requested THEN GREATEST(attempts-1,0) ELSE attempts END,owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, planned)
	if err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
func (s *Store) NextScanDirectory(ctx context.Context, l domain.JobLease) (domain.ScanDirectory, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	if current.Job.CancelRequested {
		return domain.ScanDirectory{}, context.Canceled
	}
	if err = requireInventoryPhase(ctx, tx, l.Job.ID); err != nil {
		return domain.ScanDirectory{}, err
	}
	d, err := nextScanDirectory(ctx, tx, current)
	if err != nil {
		return d, err
	}
	return d, storageError(tx.Commit(ctx))
}

func nextScanDirectory(ctx context.Context, tx pgx.Tx, current domain.JobLease) (domain.ScanDirectory, error) {
	l := current
	var err error
	var d domain.ScanDirectory
	err = tx.QueryRow(ctx, `SELECT d.root_id::text,r.path,d.path FROM job_directories d JOIN library_roots r ON r.id=d.root_id WHERE d.job_id=$1::uuid AND NOT d.done AND (d.parent_path IS NULL OR EXISTS(SELECT 1 FROM job_directories p WHERE p.job_id=d.job_id AND p.root_id=d.root_id AND p.path=d.parent_path AND p.done)) ORDER BY d.root_id,d.path LIMIT 1 FOR UPDATE OF d`, l.Job.ID).Scan(&d.RootID, &d.RootPath, &d.Path)
	if err != nil {
		return d, storageError(err)
	}
	// Restart a directory from its beginning. Remove its incomplete observation,
	// including children never visited while their parent was unfinished. This
	// avoids keeping files that disappeared between attempts. Completed siblings
	// remain durable and are not scanned again.
	var files, bytes int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(size),0)::bigint FROM job_inventory WHERE job_id=$1::uuid AND root_id=$2::uuid AND parent_path=$3`, l.Job.ID, d.RootID, d.Path).Scan(&files, &bytes); err != nil {
		return d, storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM job_inventory WHERE job_id=$1::uuid AND root_id=$2::uuid AND parent_path=$3`, l.Job.ID, d.RootID, d.Path); err != nil {
		return d, storageError(err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM job_directories WHERE job_id=$1::uuid AND root_id=$2::uuid AND parent_path=$3 AND NOT done`, l.Job.ID, d.RootID, d.Path)
	if err != nil {
		return d, storageError(err)
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET files=files-$2,bytes=bytes-$3,directory_total=directory_total-$4 WHERE id=$1::uuid`, l.Job.ID, files, bytes, tag.RowsAffected()); err != nil {
		return d, err
	}
	return d, nil
}
func validScanPath(value string, root bool) bool {
	if value == "." {
		return root
	}
	return validText(value, domain.ScanPathMaxBytes, false) && !strings.ContainsAny(value, `\:`) && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}
func scanChild(parent, child string) bool {
	return validScanPath(child, false) && path.Dir(child) == parent
}
func validInventoryKind(kind string) bool {
	return kind == "video" || kind == "nfo" || kind == "image" || kind == "other"
}
func validScanBatch(d domain.ScanDirectory, b domain.ScanBatch) bool {
	if !domain.ValidID(d.RootID) || !validScanPath(d.Path, true) || len(b.Entries)+len(b.Directories) > domain.ScanBatchMaxEntries || b.Skipped < 0 || (!b.Done && b.Skipped != 0) {
		return false
	}
	seen := map[string]bool{}
	for _, e := range b.Entries {
		if e.RootID != d.RootID || !scanChild(d.Path, e.Path) || !validInventoryKind(e.Kind) || e.Size < 0 || seen[e.Path] {
			return false
		}
		seen[e.Path] = true
	}
	for _, p := range b.Directories {
		if !scanChild(d.Path, p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
func (s *Store) SaveScanBatch(ctx context.Context, l domain.JobLease, d domain.ScanDirectory, b domain.ScanBatch) error {
	if !validScanBatch(d, b) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.CancelRequested {
		return context.Canceled
	}
	if err = requireInventoryPhase(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	if err = saveScanBatch(ctx, tx, current, d, b); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func saveScanBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, d domain.ScanDirectory, b domain.ScanBatch) error {
	l := current
	var err error
	var done bool
	var rootPath string
	var totalDirs int64
	err = tx.QueryRow(ctx, `SELECT d.done,r.path FROM job_directories d JOIN library_roots r ON r.id=d.root_id WHERE d.job_id=$1::uuid AND d.root_id=$2::uuid AND d.path=$3 AND (d.parent_path IS NULL OR EXISTS(SELECT 1 FROM job_directories p WHERE p.job_id=d.job_id AND p.root_id=d.root_id AND p.path=d.parent_path AND p.done)) FOR UPDATE OF d`, l.Job.ID, d.RootID, d.Path).Scan(&done, &rootPath)
	if err != nil {
		return storageError(err)
	}
	if rootPath != d.RootPath {
		return domain.ErrInvalid
	}
	if done {
		return nil
	}
	if err = tx.QueryRow(ctx, `SELECT directory_total FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&totalDirs); err != nil {
		return storageError(err)
	}
	files, bytes := current.Job.Files, current.Job.Bytes
	if len(b.Entries) > 0 {
		paths := make([]string, len(b.Entries))
		kinds := make([]string, len(b.Entries))
		sizes := make([]int64, len(b.Entries))
		modified := make([]int64, len(b.Entries))
		for i, entry := range b.Entries {
			paths[i], kinds[i], sizes[i], modified[i] = entry.Path, entry.Kind, entry.Size, entry.ModifiedUnixNano
		}
		// Read only this bounded batch. The jobs transaction lock keeps the
		// observation stable until the upsert and fenced counter update commit.
		type previousEntry struct {
			exists, directory bool
			size              int64
		}
		previous := make(map[string]previousEntry, len(paths))
		rows, err := tx.Query(ctx, `SELECT requested.path,i.path IS NOT NULL,COALESCE(i.size,0),EXISTS(SELECT 1 FROM job_directories d WHERE d.job_id=$1::uuid AND d.root_id=$2::uuid AND d.path=requested.path) FROM unnest($3::text[]) requested(path) LEFT JOIN job_inventory i ON i.job_id=$1::uuid AND i.root_id=$2::uuid AND i.path=requested.path`, l.Job.ID, d.RootID, paths)
		if err != nil {
			return storageError(err)
		}
		for rows.Next() {
			var name string
			var old previousEntry
			if err = rows.Scan(&name, &old.exists, &old.size, &old.directory); err != nil {
				rows.Close()
				return storageError(err)
			}
			previous[name] = old
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return storageError(err)
		}
		if len(previous) != len(paths) {
			return domain.ErrConflict
		}
		for _, entry := range b.Entries {
			old := previous[entry.Path]
			if !old.exists {
				files++
			}
			if files > int64(current.Policy.MaxEntries) || old.size > bytes || entry.Size > math.MaxInt64-(bytes-old.size) {
				return domain.ErrScanLimit
			}
			bytes = bytes - old.size + entry.Size
			if old.directory {
				return domain.ErrConflict
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano) SELECT $1::uuid,$2::uuid,$3,e.path,e.kind,e.size,e.modified FROM unnest($4::text[],$5::text[],$6::bigint[],$7::bigint[]) e(path,kind,size,modified) ON CONFLICT(job_id,root_id,path) DO UPDATE SET kind=EXCLUDED.kind,size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano`, l.Job.ID, d.RootID, d.Path, paths, kinds, sizes, modified); err != nil {
			return storageError(err)
		}
	}
	if len(b.Directories) > 0 {
		var conflicts bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[]))`, l.Job.ID, d.RootID, b.Directories).Scan(&conflicts); err != nil {
			return storageError(err)
		}
		if conflicts {
			return domain.ErrConflict
		}
		tag, err := tx.Exec(ctx, `INSERT INTO job_directories(job_id,root_id,path,parent_path) SELECT $1::uuid,$2::uuid,p.path,$4 FROM unnest($3::text[]) p(path) ON CONFLICT DO NOTHING`, l.Job.ID, d.RootID, b.Directories, d.Path)
		if err != nil {
			return storageError(err)
		}
		totalDirs += tag.RowsAffected()
		if totalDirs > int64(current.Policy.MaxDirectories) {
			return domain.ErrScanLimit
		}
	}
	skipped, dirs := current.Job.Skipped, current.Job.Directories
	if b.Done {
		if b.Skipped > math.MaxInt64-skipped {
			return domain.ErrScanLimit
		}
		skipped += b.Skipped
		dirs++
		if _, err = tx.Exec(ctx, `UPDATE job_directories SET done=true,skipped=$4 WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=$3`, l.Job.ID, d.RootID, d.Path, b.Skipped); err != nil {
			return storageError(err)
		}
	}
	if err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET files=$2,bytes=$3,directory_total=$4,directories=$5,skipped=$6 WHERE id=$1::uuid`, l.Job.ID, files, bytes, totalDirs, dirs, skipped); err != nil {
		return err
	}
	return nil
}
func validJobError(state, code string) bool {
	if state == domain.JobSucceeded || state == domain.JobCancelled {
		return code == ""
	}
	if state != domain.JobFailed {
		return false
	}
	return code == "scan_unavailable" || code == "scan_io" || code == "scan_limit" || code == "scan_failed" || code == "job_timeout" || code == "job_attempts_exhausted"
}
func (s *Store) FinishJob(ctx context.Context, l domain.JobLease, state, code string) error {
	if l.Job.Kind == domain.JobCatalogImport {
		return s.FinishCatalogImport(ctx, l, state, code)
	}
	if l.Job.Kind == domain.JobNFOWrite {
		return domain.ErrInvalid
	}

	if !validJobError(state, code) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.Kind != "inventory_scan" {
		return domain.ErrInvalid
	}
	epoch, currentEpoch, err := inventoryEpoch(ctx, tx, current)
	if err != nil {
		return err
	}
	if state == domain.JobSucceeded && epoch != nil && *epoch != currentEpoch {
		return domain.ErrInventoryInvalidated
	}
	imageEpoch := epoch
	if epoch != nil && *epoch != currentEpoch {
		imageEpoch = nil
	}
	var missing int64
	review := false
	inventoryComplete := false
	if state == domain.JobSucceeded {
		if current.Job.CancelRequested {
			return domain.ErrConflict
		}
		if err = requireNFOFinished(ctx, tx, l.Job.ID); err != nil {
			return err
		}
		phase, phaseErr := loadProbePhase(ctx, tx, l.Job.ID)
		request, requestErr := loadProbeRequest(ctx, tx, l.Job.ID)
		if requestErr != nil {
			return requestErr
		}
		if request != nil {
			if request.ErrorCode != "" || errors.Is(phaseErr, domain.ErrNotFound) {
				return domain.ErrConflict
			}
			if phaseErr == nil && !requestMatchesPhase(request, phase) {
				return domain.ErrProbeIdentityMismatch
			}
		}
		if phaseErr == nil {
			if phase.State != domain.ProbePhaseDone {
				return domain.ErrConflict
			}
			if err = checkProbePhaseScope(ctx, tx, phase); err != nil {
				return err
			}
		} else if !errors.Is(phaseErr, domain.ErrNotFound) {
			return phaseErr
		}
		covered, skipped, coverageErr := inventoryCoverage(ctx, tx, current)
		if coverageErr != nil {
			return coverageErr
		}
		if !covered {
			return domain.ErrConflict
		}
		inventoryComplete = !skipped && current.Job.Skipped == 0
		review = !inventoryComplete || epoch == nil
		if !review {
			var baseline, unknown int64
			if err = tx.QueryRow(ctx, inventoryMissingCountsSQL, current.Job.LibraryID, l.Job.ID, epoch).Scan(&baseline, &unknown, &missing); err != nil {
				return storageError(err)
			}
			if baseline > 500000 {
				return domain.ErrScanLimit
			}
			// Old scope uncertainty cannot declare files missing. A complete
			// current observation may still establish a fresh baseline, allowing
			// the next scan to compare without a permanent review loop.
			if unknown > 0 {
				missing = 0
			}
			review = missing > 0 && (missing >= int64(current.Policy.MissingCountLimit) || missing*100 >= baseline*int64(current.Policy.MissingPercentLimit))
			// Keep unresolved missing files visible on later scans. A review
			// result cannot acknowledge or replace the accepted baseline.
			if !review && epoch != nil {
				if err = saveImageProgress(ctx, tx, current, imageEpoch, true); err != nil {
					return err
				}
				published, publishErr := publishPreparedInventory(ctx, tx, current, "", 0)
				if publishErr != nil {
					return publishErr
				}
				if !published {
					if _, err = tx.Exec(ctx, `DELETE FROM library_inventory_baseline WHERE library_id=$1::uuid`, current.Job.LibraryID); err != nil {
						return storageError(err)
					}
					// Image comparison must observe the previous baseline before it is
					// atomically replaced by this complete inventory.
					if _, err = tx.Exec(ctx, `WITH revision AS (UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid RETURNING inventory_baseline_revision) INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision) SELECT $1::uuid,root_id,path,true,kind,size,modified_unix_nano,$3,revision.inventory_baseline_revision FROM job_inventory CROSS JOIN revision WHERE job_id=$2::uuid`, current.Job.LibraryID, l.Job.ID, *epoch); err != nil {
						return storageError(err)
					}
				}
			}
		}
	}
	if state != domain.JobSucceeded || review || epoch == nil {
		if err = saveImageProgress(ctx, tx, current, imageEpoch, state == domain.JobSucceeded && inventoryComplete); err != nil {
			return err
		}
	}
	if err = releaseParentProbeLeases(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	err = guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET state=$2,error_code=$3,missing=$4,review_required=$5,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code, missing, review)
	if err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "job.finished", l.Job.ID, nil, map[string]any{"state": state, "errorCode": code, "missing": missing, "reviewRequired": review}); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	if err = guardInventoryFinish(ctx, tx, current, epoch, state == domain.JobSucceeded); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
