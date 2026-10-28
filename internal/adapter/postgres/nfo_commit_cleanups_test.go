package postgres

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// resolvedPlanBatch journals count tokens with plans (so each holds a
// reservation) and resolves the job. A token without ready evidence was never
// renamed, so the original lease resolves the job directly.
func resolvedPlanBatch(t *testing.T, f jobFixture, base domain.JobLease, prepared domain.NFOWritePreparation, batch, count, start int) []string {
	t.Helper()
	lease, plans, tokens := makeAttemptQuotaBatch(t, f, base, prepared, batch, count, start, make([][2]int, start+count))
	for i := range plans {
		if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, i+1, tokens[i], plans[i]); err != nil {
			t.Fatal("owned cleanup plan refused", err)
		}
	}
	resolved, err := f.s.FinishNFOWriteJob(f.ctx, lease, domain.JobFailed, "nfo_write_failed")
	if err != nil || !resolved {
		t.Fatal("owned cleanup batch not resolved", err)
	}
	return tokens
}

func cleanupCount(t *testing.T, f jobFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNFOCommitCleanupEndToEndRemovesRetainedFiles(t *testing.T) {
	s := newNFOWriteJob(t, 2)
	startNFOWriteRunner(t, s.f.s, nfoWriteTestWriter(t))
	s.await(t, domain.JobSucceeded)
	deadline := time.Now().Add(45 * time.Second)
	for cleanupCount(t, s.f, `SELECT count(*) FROM nfo_write_commit_cleanups c JOIN nfo_write_commit_journal w ON w.token=c.token WHERE w.job_id=$1::uuid AND c.outcome=1`, s.job.ID) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("resolved tokens were not cleaned")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i, p := range s.prepared {
		if string(s.target(t, i)) != string(p.Replacement) || string(s.backup(t, i)) != string(p.Original) {
			t.Fatal("cleanup changed the target or its backup", i)
		}
		entries, err := os.ReadDir(filepath.Dir(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath))))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".jelee-nfo-commit-") {
				t.Fatal("retained commit object left after cleanup")
			}
		}
	}
	if cleanupCount(t, s.f, `SELECT count(*) FROM nfo_write_commit_attempt_reservations r WHERE NOT EXISTS(SELECT 1 FROM nfo_write_commit_cleanups c WHERE c.token=r.token)`) != 0 {
		t.Fatal("cleaned tokens still hold attempt capacity")
	}
}

// More than 256 writes over time must keep working: the global attempt
// capacity is released as resolved tokens are cleaned.
func TestNFOCommitCleanupReleasesGlobalAttemptCapacity(t *testing.T) {
	f, base, prepared := nfoCommitFixture(t)
	stopAttemptQuotaJob(t, f, base)
	total, batch := 0, 0
	for total < 256 {
		count := min(256-total, 100)
		batch++
		resolvedPlanBatch(t, f, base, prepared, batch, count, total)
		total += count
	}
	if cleanupCount(t, f, `SELECT count(*) FROM nfo_write_commit_attempt_reservations`) != 256 {
		t.Fatal("owned capacity fixture differs")
	}
	lease, plans, tokens := makeAttemptQuotaBatch(t, f, base, prepared, batch+1, 1, total, make([][2]int, total+1))
	if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, tokens[0], plans[0]); err == nil {
		t.Fatal("plan beyond the global capacity admitted")
	}
	cleaner, err := app.NewNFOWriteCleaner(f.s, nfoWriteTestWriter(t), "33333333-3333-4333-8333-333333333333", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for cleaned := 0; cleaned < 256; {
		n, err := cleaner.CleanPending(f.ctx, 100)
		if err != nil || n == 0 {
			t.Fatal("cleanup pass failed", n, err)
		}
		cleaned += n
	}
	if cleanupCount(t, f, `SELECT count(*) FROM nfo_write_commit_cleanups`) != 256 {
		t.Fatal("not every resolved token was cleaned")
	}
	if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, tokens[0], plans[0]); err != nil {
		t.Fatal("released capacity not admitted", err)
	}
	if cleanupCount(t, f, `SELECT count(*) FROM nfo_write_commit_attempt_reservations`) != 257 {
		t.Fatal("reservation history changed")
	}
}

func TestNFOCommitCleanupLeaseIsExclusive(t *testing.T) {
	f, base, prepared := nfoCommitFixture(t)
	stopAttemptQuotaJob(t, f, base)
	lease, plans, tokens := makeAttemptQuotaBatch(t, f, base, prepared, 1, 1, 0, make([][2]int, 1))
	if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, 1, tokens[0], plans[0]); err != nil {
		t.Fatal(err)
	}
	token := tokens[0]
	if _, err := f.s.AcquireNFOWriteCommitCleanup(f.ctx, token, "early", time.Minute); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("cleanup started before the job was resolved", err)
	}
	if resolved, err := f.s.FinishNFOWriteJob(f.ctx, lease, domain.JobFailed, "nfo_write_failed"); err != nil || !resolved {
		t.Fatal("owned job not resolved", err)
	}
	// Concurrent cleaners: exactly one holds the token.
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = f.s.AcquireNFOWriteCommitCleanup(f.ctx, token, "cleaner-"+string(rune('a'+i)), 2*time.Second)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, domain.ErrConflict):
			t.Fatal("unexpected concurrent cleanup error", err)
		}
	}
	if won != 1 {
		t.Fatal("concurrent cleanup leases granted", won)
	}
	var first domain.NFOWriteCommitCleanupLease
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT owner,epoch FROM nfo_write_commit_cleanup_leases WHERE token=$1::uuid`, token).Scan(&first.Owner, &first.Epoch); err != nil || first.Epoch != 1 {
		t.Fatal("cleanup lease differs", err)
	}
	first.Token = token
	if _, err := f.s.RenewNFOWriteCommitCleanup(f.ctx, domain.NFOWriteCommitCleanupLease{Token: token, Owner: "intruder", Epoch: 1}, time.Minute); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("foreign owner renewed the lease", err)
	}
	// The resolved job has no live lease, so no write can touch the token.
	if _, err := f.s.SaveNFOWriteCommitSettlement(f.ctx, lease, 1, token, 0, domain.NFOWriteCommitRolledBack); err == nil {
		t.Fatal("settlement written during cleanup")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_commit_cleanup_leases WHERE token=$1::uuid`, token); err == nil {
		t.Fatal("cleanup lease deleted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_commit_cleanup_leases SET owner='intruder',lease_until=clock_timestamp()+interval '1 minute' WHERE token=$1::uuid`, token); err == nil {
		t.Fatal("live cleanup lease taken over")
	}
	// An expired lease passes to the next owner with the next epoch; the old
	// holder can neither read nor record any more.
	time.Sleep(2100 * time.Millisecond)
	second, err := f.s.AcquireNFOWriteCommitCleanup(f.ctx, token, "successor", time.Minute)
	if err != nil || second.Epoch != 2 {
		t.Fatal("expired cleanup lease not taken over", err)
	}
	if _, err := f.s.GetNFOWriteCommitCleanupInput(f.ctx, first); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale holder read cleanup input", err)
	}
	if err := f.s.CompleteNFOWriteCommitCleanup(f.ctx, first, domain.NFOWriteCommitCleanupRemoved, 0); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale holder recorded cleanup", err)
	}
	input, err := f.s.GetNFOWriteCommitCleanupInput(f.ctx, second)
	if err != nil || input.Plan.TargetName != plans[0].TargetName || input.Plan.TargetIdentity != plans[0].TargetIdentity || len(input.Namespaces) != 1 || input.Namespaces[0].Attempt != 0 {
		t.Fatal("cleanup input differs", err)
	}
	if err := f.s.CompleteNFOWriteCommitCleanup(f.ctx, second, domain.NFOWriteCommitCleanupRetained, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("inconsistent outcome accepted", err)
	}
	if err := f.s.CompleteNFOWriteCommitCleanup(f.ctx, second, domain.NFOWriteCommitCleanupRetained, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CompleteNFOWriteCommitCleanup(f.ctx, second, domain.NFOWriteCommitCleanupRetained, 2); err != nil {
		t.Fatal("same holder replay refused", err)
	}
	if err := f.s.CompleteNFOWriteCommitCleanup(f.ctx, second, domain.NFOWriteCommitCleanupRemoved, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("recorded outcome changed", err)
	}
	if _, err := f.s.RenewNFOWriteCommitCleanup(f.ctx, second, time.Minute); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("lease renewed after cleanup", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_write_commit_cleanups SET outcome=1,retained=0 WHERE token=$1::uuid`, token); err == nil {
		t.Fatal("recorded cleanup changed")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM nfo_write_commit_cleanups WHERE token=$1::uuid`, token); err == nil {
		t.Fatal("recorded cleanup deleted")
	}
	if ids, err := f.s.ListNFOWriteCommitCleanups(f.ctx, 10); err != nil || len(ids) != 0 {
		t.Fatal("cleaned token listed again", ids, err)
	}
}

func cleanupMigration(t *testing.T, suffix string) string {
	t.Helper()
	matches, err := fs.Glob(migrationFiles, "migrations/*_nfo_commit_cleanups."+suffix+".sql")
	if err != nil || len(matches) != 1 {
		t.Fatal("cleanup migration missing")
	}
	return strings.TrimPrefix(matches[0], "migrations/")
}

func TestNFOCommitCleanupMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "nfo_commit_cleanups")-1)
	var removed, restored bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_write_commit_cleanups') IS NULL AND to_regclass('nfo_write_commit_cleanup_leases') IS NULL AND to_regprocedure('guard_nfo_commit_cleanup()') IS NULL`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left cleanup objects", err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT position('nfo_write_commit_cleanups' IN prosrc)=0 FROM pg_proc WHERE oid='guard_nfo_commit_attempt()'::regprocedure`).Scan(&restored); err != nil || !restored {
		t.Fatal("downgrade did not restore the attempt capacity guard", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
}

func TestNFOCommitCleanupMigrationRefusesRetainedData(t *testing.T) {
	f, base, prepared := nfoCommitFixture(t)
	stopAttemptQuotaJob(t, f, base)
	tokens := resolvedPlanBatch(t, f, base, prepared, 1, 1, 0)
	if _, err := f.s.AcquireNFOWriteCommitCleanup(f.ctx, tokens[0], "cleaner", time.Minute); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f, "nfo_commit_cleanups")
	nfoMigrationDenied(t, f, cleanupMigration(t, "down"))
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
		t.Fatal("retained cleanup lease downgraded")
	}
	version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
	if err != nil || version != want-1 || !dirty {
		t.Fatal("refused cleanup downgrade lost dirty status", err)
	}
	if cleanupCount(t, f, `SELECT count(*) FROM nfo_write_commit_cleanup_leases`) != 1 {
		t.Fatal("refused downgrade removed cleanup leases")
	}
}
