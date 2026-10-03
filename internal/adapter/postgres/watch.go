package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func validWatchLease(v domain.WatchLease) bool {
	return domain.ValidID(v.LibraryID) && domain.ValidID(v.Owner) && v.Generation > 0 && v.Revision > 0 && v.InventoryGeneration >= 0
}

// All callers already hold the jobs lock; this checks the database clock and
// live definition/root generations again after writes that could have waited.
func watchFence(ctx context.Context, tx pgx.Tx, v domain.WatchLease) error {
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scan_watch_state w JOIN scan_schedules s USING(library_id) JOIN libraries l ON l.id=w.library_id JOIN users u ON u.id=s.owner_id WHERE w.library_id=$1::uuid AND w.lease_owner=$2::uuid AND w.lease_generation=$3 AND w.definition_revision=$4 AND w.inventory_generation=$5 AND w.lease_until>clock_timestamp() AND s.watch_enabled AND s.revision=w.definition_revision AND l.inventory_generation=w.inventory_generation AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL)`, v.LibraryID, v.Owner, v.Generation, v.Revision, v.InventoryGeneration).Scan(&live)
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrJobLeaseLost
	}
	return nil
}

func (s *Store) GetWatchStatus(ctx context.Context, a domain.Actor, library string) (domain.WatchStatus, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.WatchStatus{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return domain.WatchStatus{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.WatchStatus
	err = tx.QueryRow(ctx, `SELECT s.library_id::text,s.watch_enabled,COALESCE(s.watch_enabled AND w.observing AND w.lease_until>clock_timestamp() AND w.definition_revision=s.revision AND w.inventory_generation=l.inventory_generation AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL,false),COALESCE(w.dirty_generation>w.accepted_generation,false),COALESCE(w.last_job_id::text,''),CASE WHEN s.watch_enabled AND (NOT u.is_admin OR u.disabled OR u.deleted_at IS NOT NULL) THEN 'owner_unavailable' ELSE COALESCE(w.last_error,'') END FROM scan_schedules s JOIN libraries l ON l.id=s.library_id JOIN users u ON u.id=s.owner_id LEFT JOIN scan_watch_state w USING(library_id) WHERE s.library_id=$1::uuid`, library).Scan(&v.LibraryID, &v.Enabled, &v.Observing, &v.Pending, &v.LastJobID, &v.LastError)
	if err != nil {
		return v, storageError(err)
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return domain.WatchStatus{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) ClaimWatch(ctx context.Context, owner string, ttl time.Duration) (domain.WatchLease, error) {
	if ctx == nil || !domain.ValidID(owner) || !validLeaseDuration(ttl) {
		return domain.WatchLease{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.WatchLease{}, err
	}
	defer tx.Rollback(ctx)
	var v domain.WatchLease
	err = tx.QueryRow(ctx, `SELECT s.library_id::text,s.revision,l.inventory_generation FROM scan_schedules s JOIN scan_watch_state w USING(library_id) JOIN libraries l ON l.id=s.library_id JOIN users u ON u.id=s.owner_id WHERE s.watch_enabled AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL AND (w.observe_after IS NULL OR w.observe_after<=statement_timestamp()) AND (w.lease_until IS NULL OR w.lease_until<=statement_timestamp() OR w.definition_revision<>s.revision OR w.inventory_generation<>l.inventory_generation) ORDER BY w.observe_after NULLS FIRST,s.library_id LIMIT 1 FOR UPDATE OF w`).Scan(&v.LibraryID, &v.Revision, &v.InventoryGeneration)
	if err != nil {
		return v, storageError(err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text,path FROM library_roots WHERE library_id=$1::uuid ORDER BY id LIMIT 33`, v.LibraryID)
	if err != nil {
		return v, storageError(err)
	}
	for rows.Next() {
		var root domain.ScanDirectory
		root.Path = "."
		if err = rows.Scan(&root.RootID, &root.RootPath); err != nil {
			rows.Close()
			return domain.WatchLease{}, storageError(err)
		}
		v.Roots = append(v.Roots, root)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.WatchLease{}, storageError(err)
	}
	if len(v.Roots) == 0 || len(v.Roots) > 32 {
		if _, err = tx.Exec(ctx, `UPDATE scan_watch_state SET observe_after=clock_timestamp()+interval '30 seconds',last_error='resource_limit' WHERE library_id=$1::uuid`, v.LibraryID); err != nil {
			return domain.WatchLease{}, storageError(err)
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.WatchLease{}, storageError(err)
		}
		return domain.WatchLease{}, domain.ErrScanLimit
	}
	v.Owner = owner
	err = tx.QueryRow(ctx, `UPDATE scan_watch_state SET observing=false,lease_owner=$2::uuid,lease_generation=lease_generation+1,definition_revision=$3,inventory_generation=$4,lease_until=clock_timestamp()+$5*interval '1 millisecond',observe_after=NULL WHERE library_id=$1::uuid RETURNING lease_generation,lease_until`, v.LibraryID, owner, v.Revision, v.InventoryGeneration, ttl.Milliseconds()).Scan(&v.Generation, &v.ExpiresAt)
	if err != nil {
		return domain.WatchLease{}, storageError(err)
	}
	if err = watchFence(ctx, tx, v); err != nil {
		return domain.WatchLease{}, err
	}
	return v, storageError(tx.Commit(ctx))
}

func (s *Store) RenewWatch(ctx context.Context, v domain.WatchLease, ttl time.Duration) (bool, error) {
	if ctx == nil || !validWatchLease(v) || !validLeaseDuration(ttl) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = watchFence(ctx, tx, v); err != nil {
		if errors.Is(err, domain.ErrJobLeaseLost) {
			return false, nil
		}
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE scan_watch_state SET lease_until=clock_timestamp()+$2*interval '1 millisecond' WHERE library_id=$1::uuid`, v.LibraryID, ttl.Milliseconds()); err != nil {
		return false, storageError(err)
	}
	if err = watchFence(ctx, tx, v); err != nil {
		return false, err
	}
	return true, storageError(tx.Commit(ctx))
}

func (s *Store) MarkWatchDirty(ctx context.Context, v domain.WatchLease) error {
	if ctx == nil || !validWatchLease(v) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = watchFence(ctx, tx, v); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE scan_watch_state SET observing=true,dirty_generation=dirty_generation+1,last_error=CASE WHEN last_error IN ('observer_unavailable','resource_limit') THEN '' ELSE last_error END WHERE library_id=$1::uuid`, v.LibraryID); err != nil {
		return storageError(err)
	}
	if err = watchFence(ctx, tx, v); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) ReleaseWatch(ctx context.Context, v domain.WatchLease, code string) error {
	if ctx == nil || !validWatchLease(v) || (code != "" && code != "observer_unavailable" && code != "resource_limit") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE scan_watch_state SET observing=false,lease_owner=NULL,lease_until=NULL,observe_after=CASE WHEN $4='' THEN NULL ELSE clock_timestamp()+interval '30 seconds' END,last_error=CASE WHEN $4='' THEN last_error ELSE $4 END WHERE library_id=$1::uuid AND lease_owner=$2::uuid AND lease_generation=$3`, v.LibraryID, v.Owner, v.Generation, code)
	if err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) DispatchWatch(parent context.Context, v domain.WatchLease, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity, caps app.IgnoreAdmissionCapabilities) (bool, error) {
	if parent == nil || !validWatchLease(v) || !validJobPolicy(p) {
		return false, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err = lockJobs(ctx, tx); err != nil {
		return false, err
	}
	guard := func() error { return watchFence(ctx, tx, v) }
	if err = guard(); err != nil {
		return false, err
	}
	var dirty, accepted int64
	var ready bool
	if err = tx.QueryRow(ctx, `SELECT dirty_generation,accepted_generation,retry_after IS NULL OR retry_after<=clock_timestamp() FROM scan_watch_state WHERE library_id=$1::uuid FOR UPDATE`, v.LibraryID).Scan(&dirty, &accepted, &ready); err != nil {
		return false, storageError(err)
	}
	if dirty == accepted || !ready {
		return false, nil
	}
	definition, err := readSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM scan_schedules WHERE library_id=$1::uuid`, v.LibraryID))
	if err != nil {
		return false, err
	}
	var owner string
	if err = tx.QueryRow(ctx, `SELECT owner_id::text FROM scan_schedules WHERE library_id=$1::uuid`, v.LibraryID).Scan(&owner); err != nil {
		return false, storageError(err)
	}
	intent := domain.ScanIntent{NFO: definition.NFO, Ignore: domain.IgnoreIntent(definition.Ignore)}
	if definition.Probe {
		intent.Probe.Scope = domain.ProbeScopeIncremental
	}
	work, err := tx.Begin(ctx)
	if err != nil {
		return false, storageError(err)
	}
	key := fmt.Sprintf("watch:%s:%d:%d", v.LibraryID, v.Revision, dirty)
	job, _, admissionErr := s.submitScanInTransaction(ctx, work, domain.Actor{UserID: owner}, v.LibraryID, "", key, domain.JobPriorityBackground, intent, p, probe, nfo, caps, true, guard)
	if admissionErr != nil {
		if err = work.Rollback(ctx); err != nil {
			return false, storageError(err)
		}
		if !scheduleRetryable(admissionErr) {
			return false, admissionErr
		}
		_, err = tx.Exec(ctx, `UPDATE scan_watch_state SET retry_after=clock_timestamp()+interval '30 seconds',last_error='admission_unavailable' WHERE library_id=$1::uuid`, v.LibraryID)
	} else {
		if err = work.Commit(ctx); err != nil {
			return false, storageError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE scan_watch_state SET accepted_generation=$2,retry_after=NULL,last_job_id=$3::uuid,last_error='' WHERE library_id=$1::uuid`, v.LibraryID, dirty, job.ID)
	}
	if err != nil {
		return false, storageError(err)
	}
	if err = guard(); err != nil {
		return false, err
	}
	return true, storageError(tx.Commit(ctx))
}
