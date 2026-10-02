package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type jobFixture struct {
	ctx          context.Context
	s            *Store
	a            domain.Actor
	registration domain.LibraryRegistration
	policy       domain.JobPolicy
}

func newJobFixture(t *testing.T) jobFixture {
	t.Helper()
	ctx, s, _ := accountTestStore(t)
	if _, err := s.BootstrapAdmin(ctx, accountInput("job-admin")); err != nil {
		t.Fatal(err)
	}
	a := accountActor(accountLogin(t, ctx, s, "job-admin"))
	r, err := s.RegisterLibrary(ctx, "primary", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return jobFixture{ctx: ctx, s: s, a: a, registration: r, policy: jobTestPolicy()}
}
func (f jobFixture) submit(t *testing.T, key string) domain.Job {
	t.Helper()
	j, replay, err := f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, f.policy)
	if err != nil || replay {
		t.Fatalf("submit: %v replay=%t", err, replay)
	}
	return j
}
func (f jobFixture) claim(t *testing.T, owner string) domain.JobLease {
	t.Helper()
	l, err := f.s.ClaimJob(f.ctx, owner, false, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func (f jobFixture) directory(t *testing.T, l domain.JobLease) domain.ScanDirectory {
	t.Helper()
	d, err := f.s.NextScanDirectory(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func (f jobFixture) get(t *testing.T, id string) domain.Job {
	t.Helper()
	j, err := f.s.GetJob(f.ctx, f.a, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func scanEntry(d domain.ScanDirectory, name string, size int64) domain.InventoryEntry {
	p := name
	if d.Path != "." {
		p = d.Path + "/" + name
	}
	return domain.InventoryEntry{RootID: d.RootID, Path: p, Kind: "video", Size: size, ModifiedUnixNano: 123456789}
}
func (f jobFixture) complete(t *testing.T, key string, names []string, skipped int64) domain.Job {
	t.Helper()
	j := f.submit(t, key)
	l := f.claim(t, "completion-worker")
	d := f.directory(t, l)
	entries := make([]domain.InventoryEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, scanEntry(d, name, 7))
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: entries, Skipped: skipped, Done: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	return f.get(t, j.ID)
}

func TestJobsSubmissionIdempotencyAndLiveAdministration(t *testing.T) {
	f := newJobFixture(t)
	type result struct {
		j      domain.Job
		replay bool
		err    error
	}
	results := make(chan result, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, r, e := f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, "same-key", domain.JobPriorityManual, f.policy)
			results <- result{j, r, e}
		}()
	}
	wg.Wait()
	close(results)
	id := ""
	created := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id == "" {
			id = r.j.ID
		}
		if r.j.ID != id {
			t.Fatal("concurrent replay created another run")
		}
		if !r.replay {
			created++
		}
	}
	if created != 1 {
		t.Fatal("idempotent create count differs")
	}
	if _, _, err := f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, "same-key", domain.JobPriorityBackground, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed priority reused key")
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "same-key", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed library reused key")
	}
	if _, _, err = f.s.RetryJob(f.ctx, f.a, id, "same-key", f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("submit key reused by retry")
	}
	if _, _, err = f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, "another-key", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("same library overlapping job accepted")
	}
	regular := createAccount(t, f.ctx, f.s, f.a, "job-viewer")
	if err = f.s.ReplaceLibraryAccess(f.ctx, f.a, regular.ID, []string{f.registration.Library.ID}); err != nil {
		t.Fatal(err)
	}
	userActor := accountActor(accountLogin(t, f.ctx, f.s, "job-viewer"))
	for name, call := range map[string]func() error{
		"get": func() error { _, e := f.s.GetJob(f.ctx, userActor, id); return e }, "list": func() error { _, e := f.s.ListJobs(f.ctx, userActor, "", 10, ""); return e }, "inventory": func() error { _, e := f.s.ListInventory(f.ctx, userActor, id, "", 10); return e }, "libraries": func() error { _, e := f.s.ListLibraries(f.ctx, userActor, "", 10); return e }, "cancel": func() error { _, e := f.s.CancelJob(f.ctx, userActor, id); return e }, "retry": func() error { _, _, e := f.s.RetryJob(f.ctx, userActor, id, "user-retry", f.policy); return e }, "submit": func() error {
			_, _, e := f.s.SubmitJob(f.ctx, userActor, other.Library.ID, "user-submit", domain.JobPriorityManual, f.policy)
			return e
		},
	} {
		if e := call(); !errors.Is(e, domain.ErrForbidden) {
			t.Fatalf("ACL user reached admin %s: %v", name, e)
		}
	}
	libs, err := f.s.ListLibraries(f.ctx, f.a, "", 1)
	if err != nil || len(libs) != 1 {
		t.Fatal("library page bound")
	}
	next, err := f.s.ListLibraries(f.ctx, f.a, libs[0].ID, 1)
	if err != nil || len(next) != 1 || next[0].ID <= libs[0].ID {
		t.Fatal("library cursor ordering")
	}
	body, _ := json.Marshal(libs)
	if strings.Contains(string(body), "path") || strings.Contains(string(body), filepath.Base(f.registration.RootID)) {
		t.Fatal("library projection exposed root path")
	}
	if err = f.s.RevokeSession(f.ctx, f.a, f.a.UserID, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitJob(f.ctx, f.a, f.registration.Library.ID, "same-key", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("revoked admin replay accepted")
	}
}

func TestJobsCapacityAndPriorityAcrossCompetingWorkers(t *testing.T) {
	f := newJobFixture(t)
	f.policy.QueueLimit = 2
	ids := []string{f.registration.Library.ID}
	for i := 0; i < 7; i++ {
		r, e := f.s.RegisterLibrary(f.ctx, fmt.Sprintf("capacity-%d", i), t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, r.Library.ID)
	}
	errs := make(chan error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, _, e := f.s.SubmitJob(f.ctx, f.a, id, fmt.Sprintf("capacity-%d", i), domain.JobPriorityManual, f.policy)
			errs <- e
		}(i, id)
	}
	wg.Wait()
	close(errs)
	success, full := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, domain.ErrJobQueueFull) {
			full++
		} else {
			t.Fatal(e)
		}
	}
	if success != 2 || full != 6 {
		t.Fatalf("capacity race success=%d full=%d", success, full)
	}
	leases := make(chan domain.JobLease, 2)
	claimErrors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, e := f.s.ClaimJob(f.ctx, fmt.Sprintf("worker-%d", i), false, time.Minute)
			if e != nil {
				claimErrors <- e
				return
			}
			leases <- l
		}(i)
	}
	wg.Wait()
	close(leases)
	close(claimErrors)
	for e := range claimErrors {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for l := range leases {
		if seen[l.Job.ID] {
			t.Fatal("two workers claimed same job")
		}
		seen[l.Job.ID] = true
		if e := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 2 {
		t.Fatal("competing claims failed")
	}
	f.policy.QueueLimit = 16
	manual, _, err := f.s.SubmitJob(f.ctx, f.a, ids[0], "manual-first", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	background, _, err := f.s.SubmitJob(f.ctx, f.a, ids[1], "background", domain.JobPriorityBackground, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	l, err := f.s.ClaimJob(f.ctx, "fair-worker", true, time.Minute)
	if err != nil || l.Job.ID != background.ID {
		t.Fatal("background preference ignored")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	l = f.claim(t, "manual-worker")
	if l.Job.ID != manual.ID {
		t.Fatal("manual priority fallback lost queued work")
	}
}

func TestJobsCancellationRecoveryAndFencing(t *testing.T) {
	f := newJobFixture(t)
	queued := f.submit(t, "queued-cancel")
	cancelled, err := f.s.CancelJob(f.ctx, f.a, queued.ID)
	if err != nil || cancelled.State != domain.JobCancelled || cancelled.FinishedAt == nil {
		t.Fatal("queued cancellation not durable")
	}
	retried, replayed, err := f.s.RetryJob(f.ctx, f.a, queued.ID, "retry", f.policy)
	if err != nil || replayed || retried.ID == queued.ID {
		t.Fatal("retry did not create new run")
	}
	old := f.claim(t, "old-owner")
	d := f.directory(t, old)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, old.Job.ID); err != nil {
		t.Fatal(err)
	}
	current := f.claim(t, "new-owner")
	if current.Job.ID != old.Job.ID || current.Generation <= old.Generation || current.Job.Attempts != 2 {
		t.Fatal("expired lease not recovered")
	}
	for name, call := range map[string]func() error{
		"heartbeat": func() error { _, e := f.s.HeartbeatJob(f.ctx, old, time.Minute); return e }, "next": func() error { _, e := f.s.NextScanDirectory(f.ctx, old); return e }, "save": func() error { return f.s.SaveScanBatch(f.ctx, old, d, domain.ScanBatch{Done: true}) }, "finish": func() error { return f.s.FinishJob(f.ctx, old, domain.JobFailed, "scan_io") }, "release": func() error { return f.s.ReleaseJob(f.ctx, old) },
	} {
		if e := call(); !errors.Is(e, domain.ErrJobLeaseLost) {
			t.Fatalf("stale %s accepted: %v", name, e)
		}
	}
	current.ExpiresAt = time.Unix(0, 0)
	if flag, e := f.s.HeartbeatJob(f.ctx, current, time.Minute); e != nil || flag {
		t.Fatal("heartbeat relied on stale caller expiry")
	}
	d = f.directory(t, current)
	if err = f.s.SaveScanBatch(f.ctx, current, d, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, current.Job.ID); err != nil {
		t.Fatal(err)
	}
	if flag, e := f.s.HeartbeatJob(f.ctx, current, time.Minute); e != nil || !flag {
		t.Fatal("cancel request invisible to owner")
	}
	if err = f.s.FinishJob(f.ctx, current, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("cancelled run became succeeded")
	}
	if err = f.s.SaveScanBatch(f.ctx, current, d, domain.ScanBatch{Done: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("save ignored cancel flag")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, current.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ClaimJob(f.ctx, "recovery", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("expired cancel was reclaimed")
	}
	if j := f.get(t, current.Job.ID); j.State != domain.JobCancelled || j.Missing != 0 {
		t.Fatal("expired cancellation not finalized safely")
	}
	f.policy.MaxAttempts = 1
	j := f.submit(t, "attempt-limit")
	l := f.claim(t, "one-attempt")
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ClaimJob(f.ctx, "after-limit", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("attempt bound ignored")
	}
	if end := f.get(t, j.ID); end.State != domain.JobFailed || end.ErrorCode != "job_attempts_exhausted" || end.Missing != 0 {
		t.Fatal("attempt exhaustion not finalized")
	}
}

func TestJobsPartialDirectoryRestartAndInventoryPagination(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "partial")
	old := f.claim(t, "before-restart")
	d := f.directory(t, old)
	partial := domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "vanished.mkv", 11)}, Directories: []string{"vanished-folder"}}
	if err := f.s.SaveScanBatch(f.ctx, old, d, partial); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveScanBatch(f.ctx, old, d, partial); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, j.ID); got.Files != 1 || got.Bytes != 11 {
		t.Fatal("replayed partial batch double counted")
	}
	if err := f.s.ReleaseJob(f.ctx, old); err != nil {
		t.Fatal(err)
	}
	l := f.claim(t, "after-restart")
	d = f.directory(t, l)
	if got := f.get(t, j.ID); got.Files != 0 || got.Bytes != 0 {
		t.Fatal("restart retained incomplete observation")
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "root.mkv", 5)}, Directories: []string{"child"}, Done: true}); err != nil {
		t.Fatal(err)
	}
	child := f.directory(t, l)
	if child.Path != "child" {
		t.Fatal("durable frontier restarted completed root or stale child")
	}
	if err := f.s.SaveScanBatch(f.ctx, l, child, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(child, "two.mkv", 7), scanEntry(child, "three.mkv", 9)}, Done: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.NextScanDirectory(f.ctx, l); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("completed frontier not empty")
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	end := f.get(t, j.ID)
	if end.Files != 3 || end.Bytes != 21 || end.Directories != 2 {
		t.Fatalf("checkpoint counts differ: %+v", end)
	}
	var ids, paths []string
	cursor := ""
	for {
		entries, err := f.s.ListInventory(f.ctx, f.a, j.ID, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			break
		}
		if len(entries) != 1 || entries[0].ID <= cursor {
			t.Fatal("inventory cursor ordering violated")
		}
		ids = append(ids, entries[0].ID)
		paths = append(paths, entries[0].Path)
		cursor = entries[0].ID
	}
	if len(ids) != 3 || !sort.StringsAreSorted(ids) {
		t.Fatal("inventory page repeated or lost rows")
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "child/three.mkv,child/two.mkv,root.mkv" {
		t.Fatal("partial restart retained stale observation")
	}
}

func TestJobsBatchLimitsRollbackAndSafeMissingBaseline(t *testing.T) {
	t.Run("entry_directory_and_overflow_limits", func(t *testing.T) {
		f := newJobFixture(t)
		f.policy.MaxEntries = 100
		f.policy.MaxDirectories = 1
		j := f.submit(t, "limits")
		l := f.claim(t, "limits-worker")
		d := f.directory(t, l)
		overCapacity := make([]domain.InventoryEntry, 101)
		for i := range overCapacity {
			overCapacity[i] = scanEntry(d, fmt.Sprintf("entry-%03d", i), 1)
		}
		for name, batch := range map[string]domain.ScanBatch{"entries": {Entries: overCapacity, Done: true}, "directories": {Entries: []domain.InventoryEntry{scanEntry(d, "a", 1)}, Directories: []string{"child"}, Done: true}} {
			if err := f.s.SaveScanBatch(f.ctx, l, d, batch); !errors.Is(err, domain.ErrScanLimit) {
				t.Fatalf("%s limit ignored: %v", name, err)
			}
			if got := f.get(t, j.ID); got.Files != 0 || got.Bytes != 0 || got.Directories != 0 {
				t.Fatal("rejected batch partly committed")
			}
		}
		if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "large", math.MaxInt64)}}); err != nil {
			t.Fatal(err)
		}
		if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "overflow", 1)}}); !errors.Is(err, domain.ErrScanLimit) {
			t.Fatal("byte sum overflow accepted")
		}
		if got := f.get(t, j.ID); got.Files != 1 || got.Bytes != math.MaxInt64 {
			t.Fatal("overflow changed counters")
		}
		if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("unfinished frontier succeeded")
		}
		if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "private filesystem error"); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("untrusted error persisted")
		}
	})
	t.Run("history_retention_baseline_and_partial_fail_closed", func(t *testing.T) {
		f := newJobFixture(t)
		f.policy.HistoryLimit = 1
		f.policy.MissingCountLimit = 2
		f.policy.MissingPercentLimit = 50
		first := f.complete(t, "baseline", []string{"one", "two", "three"}, 0)
		second := f.complete(t, "partial", []string{"one"}, 1)
		if second.Missing != 0 || !second.ReviewRequired {
			t.Fatal("partial traversal reported missing files")
		}
		if _, err := f.s.GetJob(f.ctx, f.a, first.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("history retained expired terminal")
		}
		third := f.complete(t, "complete", []string{"one"}, 0)
		if third.Missing != 2 || !third.ReviewRequired {
			t.Fatal("purge or partial scan replaced complete baseline")
		}
		repeated := f.complete(t, "still-missing", []string{"one"}, 0)
		if repeated.Missing != 2 || !repeated.ReviewRequired {
			t.Fatal("repeated missing scan cleared unresolved baseline")
		}
		failed := f.submit(t, "failed")
		l := f.claim(t, "failure-worker")
		if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
			t.Fatal(err)
		}
		if got := f.get(t, failed.ID); got.Missing != 0 {
			t.Fatal("failed scan inferred missing")
		}
		again := f.complete(t, "baseline", []string{"one"}, 0)
		if again.ID == first.ID || again.Missing != 2 || !again.ReviewRequired {
			t.Fatal("expired idempotency retention or baseline incorrect")
		}
		var history, baseline int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM jobs WHERE state IN ('succeeded','failed','cancelled')`).Scan(&history); err != nil || history != 1 {
			t.Fatal("terminal history unbounded")
		}
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline`).Scan(&baseline); err != nil || baseline != 3 {
			t.Fatal("baseline size incorrect")
		}
	})
	t.Run("complete_scan_below_missing_threshold_replaces_baseline", func(t *testing.T) {
		f := newJobFixture(t)
		f.policy.MissingCountLimit = 3
		f.policy.MissingPercentLimit = 100
		f.complete(t, "baseline", []string{"one", "two", "three"}, 0)
		changed := f.complete(t, "small-change", []string{"one", "two"}, 0)
		if changed.Missing != 1 || changed.ReviewRequired {
			t.Fatal("below-threshold complete scan required review")
		}
		again := f.complete(t, "same-inventory", []string{"one", "two"}, 0)
		if again.Missing != 0 || again.ReviewRequired {
			t.Fatal("accepted complete scan did not replace baseline")
		}
		var baseline int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline`).Scan(&baseline); err != nil || baseline != 2 {
			t.Fatal("accepted baseline size incorrect")
		}
	})
}

func TestJobsLeaseExpiryDuringWritesRollsBackProvisionalData(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "expiry-during-write")
	l, err := f.s.ClaimJob(f.ctx, "slow-write", false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d := f.directory(t, l)
	// Fault injection exists only in this test's private schema. No filesystem
	// scan or process sleep is held inside a production database transaction.
	if _, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION delay_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER slow_inventory BEFORE INSERT ON job_inventory FOR EACH ROW EXECUTE FUNCTION delay_job_insert()`); err != nil {
		t.Fatal("install isolated write delay")
	}
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "delayed.mkv", 1)}, Done: true}); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatalf("expired batch committed: %v", err)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_inventory WHERE job_id=$1::uuid`, j.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired transaction left inventory rows")
	}
	if got := f.get(t, j.ID); got.Files != 0 || got.Directories != 0 {
		t.Fatal("expired transaction left counters")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER slow_inventory ON job_inventory`); err != nil {
		t.Fatal("remove isolated write delay")
	}
	l, err = f.s.ClaimJob(f.ctx, "slow-finish", false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	d = f.directory(t, l)
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "current.mkv", 2)}, Done: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `CREATE TRIGGER slow_baseline BEFORE INSERT ON library_inventory_baseline FOR EACH ROW EXECUTE FUNCTION delay_job_insert()`); err != nil {
		t.Fatal("install isolated completion delay")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatalf("expired completion committed: %v", err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline`).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired completion replaced baseline")
	}
	if got := f.get(t, j.ID); got.State != domain.JobRunning || got.FinishedAt != nil {
		t.Fatal("expired completion changed state")
	}
}

func TestJobsThousandEntriesRemainBoundedAndCountsUseCurrentSizes(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "thousand")
	l := f.claim(t, "batch-worker")
	d := f.directory(t, l)
	for start := 0; start < 1000; start += domain.ScanBatchMaxEntries {
		end := start + domain.ScanBatchMaxEntries
		if end > 1000 {
			end = 1000
		}
		batch := domain.ScanBatch{}
		for i := start; i < end; i++ {
			batch.Entries = append(batch.Entries, scanEntry(d, fmt.Sprintf("file-%04d.mkv", i), 7))
		}
		if err := f.s.SaveScanBatch(f.ctx, l, d, batch); err != nil {
			t.Fatal(err)
		}
	}
	// Re-observing a path updates bytes without incrementing file count.
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "file-0000.mkv", 10)}, Done: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, j.ID); got.Files != 1000 || got.Bytes != 7003 || got.Directories != 1 {
		t.Fatal("1000-entry counts incorrect")
	}
	page, err := f.s.ListInventory(f.ctx, f.a, j.ID, "", 100)
	if err != nil || len(page) != 100 {
		t.Fatal("large inventory page was not bounded")
	}
	var baseline int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid`, j.LibraryID).Scan(&baseline); err != nil || baseline != 1000 {
		t.Fatal("complete baseline differs from inventory")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `ANALYZE job_inventory`); err != nil {
		t.Fatal("analyze isolated inventory fixture")
	}
	var planJSON []byte
	if err = f.s.Pool.QueryRow(f.ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+listInventorySQL, j.ID, "", 100).Scan(&planJSON); err != nil {
		t.Fatal("explain actual inventory paging query")
	}
	var plans []struct {
		Plan struct {
			NodeType   string `json:"Node Type"`
			ActualRows int    `json:"Actual Rows"`
			Plans      []struct {
				NodeType   string `json:"Node Type"`
				IndexName  string `json:"Index Name"`
				ActualRows int    `json:"Actual Rows"`
			} `json:"Plans"`
		} `json:"Plan"`
	}
	if err = json.Unmarshal(planJSON, &plans); err != nil || len(plans) != 1 || plans[0].Plan.NodeType != "Limit" || plans[0].Plan.ActualRows != 100 || len(plans[0].Plan.Plans) != 1 {
		t.Fatal("inventory page plan no longer has a bounded index result")
	}
	index := plans[0].Plan.Plans[0]
	if index.NodeType != "Index Scan" || index.IndexName != "job_inventory_page_idx" || index.ActualRows != 100 {
		t.Fatal("inventory page sorted or scanned beyond its requested 100 rows")
	}
	t.Log("1000-row inventory EXPLAIN: Limit -> job_inventory_page_idx; 100 index rows visited for page size 100 (single fixture, not scale acceptance)")
}

func TestJobsMigrationRollbackPreservesAccountsAndLibraryConfiguration(t *testing.T) {
	f := newJobFixture(t)
	f.complete(t, "rollback", []string{"observed.mkv"}, 0)
	dsn := f.s.Pool.Config().ConnString()
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 31 {
		t.Fatalf("down31 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 30 {
		t.Fatalf("down30 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 29 {
		t.Fatalf("down29 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 28 {
		t.Fatalf("down28 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 27 {
		t.Fatalf("down28 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 26 {
		t.Fatalf("down27 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 25 {
		t.Fatalf("down26 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 24 {
		t.Fatalf("down25 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 23 {
		t.Fatalf("down24 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 22 {
		t.Fatalf("down23 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 21 {
		t.Fatalf("down22 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 20 {
		t.Fatalf("down21 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 19 {
		t.Fatal("image preference downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 18 {
		t.Fatal("metadata preference downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 17 {
		t.Fatal("baseline verification downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 16 {
		t.Fatal("baseline verification downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 15 {
		t.Fatal("legacy baseline downgrade failed", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 14 {
		t.Fatal("family scan downgrade failed", version, dirty, e)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 13 {
		t.Fatal("legacy verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 12 {
		t.Fatal("legacy ignore downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 11 {
		t.Fatal("ignore scan downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 10 {
		t.Fatal("verification downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 9 {
		t.Fatal("baseline comparison downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 8 {
		t.Fatal("rollback ignore manifest schema", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 7 {
		t.Fatal("rollback ignore intent schema", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 6 {
		t.Fatal("rollback NFO worker schema", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 5 {
		t.Fatal("rollback NFO cache schema", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 4 {
		t.Fatal("rollback probe requests")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 3 {
		t.Fatal("rollback probe cache migration before jobs migration")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 2 {
		t.Fatal("rollback jobs migration")
	}
	if err := f.s.Ready(f.ctx); err == nil {
		t.Fatal("schema 2 passed schema 5 readiness")
	}
	var accounts, roots int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM library_roots)`).Scan(&accounts, &roots); err != nil || accounts != 1 || roots != 1 {
		t.Fatal("jobs rollback changed account or library configuration")
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatal("reapply jobs migration")
	}
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("reapplied schema not ready")
	}
	jobs, err := f.s.ListJobs(f.ctx, f.a, "", 100, "")
	if err != nil || len(jobs) != 0 {
		t.Fatal("down/up retained discarded job state")
	}
}
