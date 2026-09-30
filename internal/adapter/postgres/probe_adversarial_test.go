package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func adversarialProbeFixture(t *testing.T, policy domain.ProbeCachePolicy) probeFixture {
	t.Helper()
	f := probeFixture{jobFixture: newJobFixture(t)}
	if err := f.s.EnsureProbePolicy(f.ctx, policy); err != nil {
		t.Fatal("initialize policy", err)
	}
	var err error
	f.identity, err = f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	if err != nil {
		t.Fatal("register identity", err)
	}
	return f
}

func adversarialProbeLibrary(t *testing.T, f probeFixture) probeFixture {
	t.Helper()
	r, err := f.s.RegisterLibrary(f.ctx, "secondary", t.TempDir())
	if err != nil {
		t.Fatal("register second library", err)
	}
	f.registration = r
	return f
}

func TestProbeAdversarialLastLeaseSlotAcrossParents(t *testing.T) {
	policy := domain.DefaultProbeCachePolicy()
	policy.MaxLeases = 1
	f := adversarialProbeFixture(t, policy)
	g := adversarialProbeLibrary(t, f)
	l1, _ := f.begin(t, "slot-one", "one.mkv")
	l2, _ := g.begin(t, "slot-two", "two.mkv")
	p1, c1 := f.page(t, l1)
	p2, c2 := g.page(t, l2)
	parents := []domain.JobLease{l1, l2}
	pages := []domain.ProbePage{p1, p2}
	candidates := []domain.ProbeCandidate{c1[0], c2[0]}
	type result struct {
		i     int
		lease domain.ProbeLease
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range parents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			lease, err := f.s.AcquireProbe(f.ctx, parents[i], pages[i].Token, candidates[i])
			results <- result{i, lease, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winner, loser := -1, -1
	var held domain.ProbeLease
	for r := range results {
		if r.err == nil {
			if winner != -1 {
				t.Fatal("two parents acquired the last global lease slot")
			}
			winner, held = r.i, r.lease
		} else if errors.Is(r.err, domain.ErrProbeCacheCapacity) {
			loser = r.i
		} else {
			t.Fatal("unexpected acquisition result", r.err)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatal("expected one admitted and one capacity-limited parent")
	}
	f.quota(t, 1, 1)
	if err := f.s.ReleaseProbeLease(f.ctx, parents[winner], held); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	next, err := f.s.AcquireProbe(f.ctx, parents[loser], pages[loser].Token, candidates[loser])
	if err != nil || next.Generation <= held.Generation {
		t.Fatal("released slot did not become available with a fresh fence", err)
	}
	f.quota(t, 1, 1)
}

func TestProbeAdversarialIdentityAndScopeCapacity(t *testing.T) {
	policy := domain.DefaultProbeCachePolicy()
	policy.MaxToolVersions = 2
	policy.MaxLibraries = 1
	f := adversarialProbeFixture(t, policy)
	l, _ := f.begin(t, "retain-identity")
	second := probeTestIdentity()
	second.VendorVersion += "-second"
	ref, err := f.s.RegisterProbeIdentity(f.ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	third := second
	third.VendorVersion += "-third"
	if _, err = f.s.RegisterProbeIdentity(f.ctx, third); err != domain.ErrProbeCacheCapacity {
		t.Fatal("tool limit did not reject a new identity", err)
	}
	if same, err := f.s.RegisterProbeIdentity(f.ctx, second); err != nil || same != ref {
		t.Fatal("existing identity consumed another quota slot", err)
	}
	g := adversarialProbeLibrary(t, f)
	g.submit(t, "scope-overflow")
	l2 := g.claim(t, "scope-owner")
	d := g.directory(t, l2)
	if err = g.s.SaveScanBatch(g.ctx, l2, d, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	start := domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeIncremental}
	if _, err = g.s.BeginProbePhase(g.ctx, l2, start); err != domain.ErrProbeCacheCapacity {
		t.Fatal("scope limit did not reject a new running scope", err)
	}
	var scopes, tools, phases int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes,tools_used,(SELECT count(*) FROM probe_job_state) FROM probe_cache_quota`).Scan(&scopes, &tools, &phases); err != nil || scopes != 1 || tools != 2 || phases != 1 {
		t.Fatal("rejected operation changed cardinality counters")
	}
	f.finish(t, l)
	if _, err = f.s.SweepProbeCache(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes,tools_used FROM probe_cache_quota`).Scan(&scopes, &tools); err != nil || scopes != 0 || tools != 1 {
		t.Fatal("sweep failed to free unused scope and identity while retaining historical identity")
	}
	if _, err = g.s.BeginProbePhase(g.ctx, l2, start); err != nil {
		t.Fatal("freed library scope was not reusable", err)
	}
	if _, err = f.s.RegisterProbeIdentity(f.ctx, third); err != nil {
		t.Fatal("freed identity slot was not reusable", err)
	}
}

func TestProbeAdversarialHeartbeatDoesNotReviveExpiredChild(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "heartbeat-child", "one.mkv")
	page, candidates := f.page(t, l)
	old, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET lease_until=clock_timestamp()+interval '5 seconds'`); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := f.s.HeartbeatJob(f.ctx, l, time.Minute); err != nil || cancelled {
		t.Fatal("heartbeat live parent", err)
	}
	var bounded bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT c.lease_until>clock_timestamp()+interval '10 seconds' AND c.lease_until<=j.lease_until FROM probe_cache c JOIN jobs j ON j.id=c.lease_job_id`).Scan(&bounded); err != nil || !bounded {
		t.Fatal("live child did not renew within the parent deadline")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.HeartbeatJob(f.ctx, l, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &old, Metadata: probeTestMetadata()}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion}); err != domain.ErrProbeLeaseLost {
		t.Fatal("heartbeat revived an expired child", err)
	}
	again, err := f.s.NextProbePage(f.ctx, l, 1)
	if err != nil || again.Token != page.Token {
		t.Fatal("expired child advanced the durable cursor")
	}
	current, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil || current.Generation <= old.Generation {
		t.Fatal("child recovery did not allocate a fresh fence", err)
	}
	if err = f.s.ReleaseProbeLease(f.ctx, l, old); err != domain.ErrProbeLeaseLost {
		t.Fatal("old lease released the replacement")
	}
	f.quota(t, 1, 1)
}

func TestProbeAdversarialForeignAndSkippedPrefix(t *testing.T) {
	f := newProbeFixture(t)
	l, phase := f.begin(t, "prefix", "a.mkv", "b.mkv", "c.mkv")
	page, candidates := f.page(t, l)
	g := adversarialProbeLibrary(t, f)
	l2, _ := g.begin(t, "foreign-prefix", "other.mkv")
	_, foreign := g.page(t, l2)
	for _, tc := range []struct {
		name       string
		candidates []domain.ProbeCandidate
		want       error
	}{
		{"unordered", []domain.ProbeCandidate{candidates[1], candidates[0]}, domain.ErrInvalid},
		{"duplicate", []domain.ProbeCandidate{candidates[0], candidates[0]}, domain.ErrInvalid},
		{"skipped", candidates[1:], domain.ErrConflict},
		{"foreign", foreign, domain.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, tc.candidates); err != tc.want {
				t.Fatal("invalid lookup prefix accepted", err)
			}
			completions := make([]domain.ProbeCompletion, len(tc.candidates))
			for i, candidate := range tc.candidates {
				completions[i] = domain.ProbeCompletion{Candidate: candidate, Kind: domain.ProbeCompletionHit}
			}
			if _, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, completions); err != tc.want {
				t.Fatal("invalid commit prefix accepted", err)
			}
		})
	}
	forged := page.Token
	forged.AfterID = candidates[0].InventoryID
	if _, err := f.s.AcquireProbe(f.ctx, l, forged, candidates[1]); err != domain.ErrConflict {
		t.Fatal("caller forged checkpoint progress", err)
	}
	got, err := f.s.BeginProbePhase(f.ctx, l, phase.Start)
	if err != nil || got != phase {
		t.Fatal("rejected prefixes altered phase state")
	}
	f.quota(t, 0, 0)
}

func TestProbeAdversarialCancelledLockWaitDoesNotReserve(t *testing.T) {
	f := newProbeFixture(t)
	l, phase := f.begin(t, "cancel-wait", "one.mkv")
	page, candidates := f.page(t, l)
	holder, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(f.ctx)
	if err = lockJobs(f.ctx, holder); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, e := f.s.AcquireProbe(ctx, l, page.Token, candidates[0])
		result <- e
	}()
	// Observe the actual blocked PostgreSQL lock, rather than guessing whether
	// the goroutine reached the transaction from a sleep duration.
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=17481204)`).Scan(&waiting)
		if err != nil {
			t.Fatal("observe owned lock wait", err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not reach the held advisory lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("lock wait did not preserve caller cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled database wait did not return")
	}
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	got, err := f.s.BeginProbePhase(f.ctx, l, phase.Start)
	if err != nil || got != phase {
		t.Fatal("cancelled reservation mutated phase or cursor")
	}
	if _, err = f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0]); err != nil {
		t.Fatal("cancelled waiter stranded connection or quota", err)
	}
	f.quota(t, 1, 1)
}

func TestProbeAdversarialHistoryTrimPreservesActiveSibling(t *testing.T) {
	f := newProbeFixture(t)
	f.policy.HistoryLimit = 1
	g := adversarialProbeLibrary(t, f)
	l1, _ := f.begin(t, "trim-old", "one.mkv")
	l2, _ := g.begin(t, "trim-active", "two.mkv")
	p1, c1 := f.page(t, l1)
	p2, c2 := g.page(t, l2)
	if _, err := f.s.AcquireProbe(f.ctx, l1, p1.Token, c1[0]); err != nil {
		t.Fatal(err)
	}
	sibling, err := g.s.AcquireProbe(g.ctx, l2, p2.Token, c2[0])
	if err != nil {
		t.Fatal(err)
	}
	f.quota(t, 2, 2)
	if err = f.s.FinishJob(f.ctx, l1, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal("failed parent did not release its child", err)
	}
	f.quota(t, 1, 1)
	l3, _ := f.begin(t, "trim-new")
	f.finish(t, l3)
	if _, err = f.s.GetJob(f.ctx, f.a, l1.Job.ID); err != domain.ErrNotFound {
		t.Fatal("terminal history did not trim the oldest parent", err)
	}
	var phases, pending int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM probe_job_state),(SELECT count(*) FROM probe_cache WHERE lease_job_id=$1::uuid)`, l2.Job.ID).Scan(&phases, &pending); err != nil || phases != 2 || pending != 1 {
		t.Fatal("history trim left orphan phase or deleted active sibling")
	}
	f.quota(t, 1, 1)
	completion := domain.ProbeCompletion{Candidate: c2[0], Kind: domain.ProbeCompletionSucceeded, Lease: &sibling, Metadata: probeTestMetadata()}
	if _, err = g.s.CommitProbeBatch(g.ctx, l2, p2.Token, []domain.ProbeCompletion{completion}); err != nil {
		t.Fatal("history trim fenced unrelated active child", err)
	}
	g.finish(t, l2)
	f.quota(t, 1, 0)
}

func TestProbeAdversarialTerminalHistoryDoesNotPinEmptyScope(t *testing.T) {
	policy := domain.DefaultProbeCachePolicy()
	policy.MaxLibraries = 1
	f := adversarialProbeFixture(t, policy)
	l, _ := f.begin(t, "failed-scope", "one.mkv")
	page, candidates := f.page(t, l)
	if _, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	if _, err := f.s.SweepProbeCache(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	var scopes, historicalPhases int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes,(SELECT count(*) FROM probe_job_state WHERE job_id=$1::uuid) FROM probe_cache_quota`, l.Job.ID).Scan(&scopes, &historicalPhases); err != nil || scopes != 0 || historicalPhases != 1 {
		t.Fatalf("terminal history pinned an empty library scope: scopes=%d history=%d error=%v", scopes, historicalPhases, err)
	}
	g := adversarialProbeLibrary(t, f)
	l2, _ := g.begin(t, "reuse-terminal-scope")
	g.finish(t, l2)
}

func TestProbeAdversarialInvalidationRequiresLiveAdminAndRecordsActor(t *testing.T) {
	f := newProbeFixture(t)
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'invalidation fixture','HomeVideo') RETURNING id::text`, f.registration.Library.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	snapshot := func() [3]int64 {
		t.Helper()
		var got [3]int64
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT probe_generation FROM libraries WHERE id=$1::uuid),(SELECT probe_generation FROM items WHERE id=$2::uuid),(SELECT count(*) FROM audit_logs WHERE event IN ('probe.library_invalidated','probe.item_invalidated'))`, f.registration.Library.ID, item).Scan(&got[0], &got[1], &got[2]); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := snapshot()
	for _, mode := range []string{"regular", "revoked", "disabled", "demoted"} {
		t.Run(mode, func(t *testing.T) {
			input := accountInput("cache-" + mode)
			input.Admin = mode != "regular"
			u, _, err := f.s.CreateUser(f.ctx, f.a, input, "cache-"+mode)
			if err != nil {
				t.Fatal(err)
			}
			actor := accountActor(accountLogin(t, f.ctx, f.s, u.Name))
			want := domain.ErrForbidden
			switch mode {
			case "revoked":
				err = f.s.RevokeSession(f.ctx, f.a, u.ID, actor.SessionID)
				want = domain.ErrUnauthenticated
			case "disabled":
				input.Disabled = true
				_, err = f.s.UpdateUser(f.ctx, f.a, u.ID, input)
				want = domain.ErrUnauthenticated
			case "demoted":
				input.Admin = false
				_, err = f.s.UpdateUser(f.ctx, f.a, u.ID, input)
				want = domain.ErrUnauthenticated
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.InvalidateProbeLibrary(f.ctx, actor, f.registration.Library.ID); err != want {
				t.Fatal("library invalidation accepted stale authority", err)
			}
			if err = f.s.InvalidateProbeItem(f.ctx, actor, item); err != want {
				t.Fatal("item invalidation accepted stale authority", err)
			}
			if snapshot() != before {
				t.Fatal("rejected invalidation changed generation or audit log")
			}
		})
	}
	if err := f.s.InvalidateProbeLibrary(f.ctx, f.a, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.InvalidateProbeItem(f.ctx, f.a, item); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	if after != [3]int64{before[0] + 1, before[1] + 1, before[2] + 2} {
		t.Fatal("authorized invalidation did not atomically advance generation and audit")
	}
	var attributed int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event IN ('probe.library_invalidated','probe.item_invalidated') AND actor_id=$1::uuid`, f.a.UserID).Scan(&attributed); err != nil || attributed != 2 {
		t.Fatal("invalidation audit lost the authenticated actor")
	}
}

// Seed complete negative rows, as if retained from older runs. Both global and
// per-library counters are derived from the actual stored rows; no synthetic
// counter inflation is used to manufacture a capacity boundary.
func seedAdversarialProbeRows(t *testing.T, f probeFixture, root string, count int) {
	t.Helper()
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	_, err = tx.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,error_code,expires_at,retry_after,failure_count,charge_bytes)
	SELECT r.id,'retained-'||n||'.mkv',r.library_id,7,123456789,decode(repeat('d',64),'hex'),'edge-sha256-v1',$2::uuid,l.probe_generation,r.probe_generation,'failed','probe_failed',clock_timestamp()+interval '1 hour',clock_timestamp()+interval '1 hour',1,2048
	FROM library_roots r JOIN libraries l ON l.id=r.library_id CROSS JOIN generate_series(1,$3::integer) n WHERE r.id=$1::uuid`, root, f.identity.ID, count)
	if err != nil {
		t.Fatal("seed valid retained rows", err)
	}
	_, err = tx.Exec(f.ctx, `UPDATE probe_cache_quota SET rows_used=(SELECT count(*) FROM probe_cache),bytes_used=(SELECT sum(charge_bytes) FROM probe_cache); UPDATE probe_library_quota q SET rows_used=(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id),bytes_used=(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache c WHERE c.library_id=q.library_id)`)
	if err != nil {
		t.Fatal("derive fixture counters", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProbeAdversarialBoundedEvictionAtQuotaBoundary(t *testing.T) {
	for _, scope := range []string{"global_rows", "library_rows", "global_bytes", "library_bytes"} {
		t.Run(scope, func(t *testing.T) {
			policy := domain.DefaultProbeCachePolicy()
			rows := 1000
			switch scope {
			case "global_rows":
				policy.MaxRows, policy.LibraryMaxRows = 1000, 1000
				rows-- // One real row resides in the other library.
			case "library_rows":
				policy.MaxRows, policy.LibraryMaxRows = 2000, 1000
			case "global_bytes":
				policy.MaxBytes, policy.LibraryMaxBytes = 16<<20, 16<<20
				rows = (16<<20)/domain.ProbeRowAllowanceBytes - 1
			case "library_bytes":
				policy.MaxBytes, policy.LibraryMaxBytes = 32<<20, 16<<20
				rows = (16 << 20) / domain.ProbeRowAllowanceBytes
			}
			f := adversarialProbeFixture(t, policy)
			g := adversarialProbeLibrary(t, f)
			l, _ := f.begin(t, "capacity-one", "new.mkv")
			l2, _ := g.begin(t, "capacity-two", "other.mkv")
			page, candidates := f.page(t, l)
			page2, _ := g.page(t, l2)
			seedAdversarialProbeRows(t, f, page.Entries[0].Inventory.RootID, rows)
			seedAdversarialProbeRows(t, g, page2.Entries[0].Inventory.RootID, 1)
			f.quota(t, int64(rows+1), 0)
			lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
			if err != nil {
				t.Fatal("bounded eviction failed to admit a maximum-size reservation", err)
			}
			var remaining, other int64
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE library_id=$1::uuid) FROM probe_cache`, g.registration.Library.ID).Scan(&remaining, &other); err != nil {
				t.Fatal(err)
			}
			deleted := int64(rows+2) - remaining
			if deleted < 1 || deleted > domain.ProbeSweepMax || other != 1 {
				t.Fatalf("eviction crossed its bounded/local candidate set: deleted=%d other=%d", deleted, other)
			}
			f.quota(t, remaining, 1)
			completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}
			if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion}); err != nil {
				t.Fatal(err)
			}
			f.quota(t, remaining, 0)
			var exact bool
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT charge_bytes=2048+octet_length(metadata::text) FROM probe_cache WHERE root_id=$1::uuid AND relative_path=$2`, lease.RootID, lease.Path).Scan(&exact); err != nil || !exact {
				t.Fatal("reservation was not replaced with the exact PostgreSQL JSONB charge")
			}
			t.Log(fmt.Sprintf("retained=%d deleted=%d; exact counters and JSONB charge verified", rows+1, deleted))
		})
	}
}
