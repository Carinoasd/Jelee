package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Fault tests use the fixture's private schema and actual PostgreSQL triggers.
// Snapshots remain private: failures never print metadata, roots, or credentials.
func probeFaultSnapshot(t *testing.T, f probeFixture) string {
	t.Helper()
	var state string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
	 'cache',(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY root_id,relative_path),'[]') FROM probe_cache c),
	 'quota',(SELECT COALESCE(jsonb_agg(to_jsonb(q)),'[]') FROM probe_cache_quota q),
	 'libraries',(SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY library_id),'[]') FROM probe_library_quota q),
	 'phases',(SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY job_id),'[]') FROM probe_job_state p),
	 'jobs',(SELECT COALESCE(jsonb_agg(to_jsonb(j)-'inventory_generation' ORDER BY id),'[]') FROM jobs j),
	 'identities',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id),'[]') FROM tool_versions t),
	 'library_generations',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,probe_generation) ORDER BY id),'[]') FROM libraries),
	 'item_generations',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,probe_generation) ORDER BY id),'[]') FROM items),
	 'audit',(SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM audit_logs a)
	)::text`).Scan(&state)
	if err != nil {
		t.Fatal("cannot read private fault snapshot")
	}
	return state
}

func requireProbeFault(t *testing.T, err, want error, value any) {
	t.Helper()
	// Equality with the fixed sentinel also excludes wrapped SQL, file paths,
	// connection strings, and PostgreSQL DETAIL/CONTEXT text.
	if err != want {
		t.Fatalf("unexpected fault classification: got %T; expected fixed sentinel", err)
	}
	if value != nil && !reflect.ValueOf(value).IsZero() {
		t.Fatal("failed operation returned a usable partial result")
	}
}

func requireProbeSnapshot(t *testing.T, f probeFixture, before string) {
	t.Helper()
	if probeFaultSnapshot(t, f) != before {
		t.Fatal("failed operation partially changed metadata, phase, quota, identity, audit, or parent")
	}
}

func TestProbeFaultCheckpointAndCommitDeniedRollBackWholeOutcome(t *testing.T) {
	for _, mode := range []string{"checkpoint_success", "checkpoint_negative", "deferred_commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeFixture(t)
			l, _ := f.begin(t, "denied-save", "one.mkv")
			page, candidates := f.page(t, l)
			lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
			if err != nil {
				t.Fatal(err)
			}
			// The exception deliberately includes diagnostics that must not escape
			// the storage boundary. These are synthetic values, never real secrets.
			_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION fault_deny_probe_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='42501',MESSAGE='denied UPDATE probe_job_state: /private/media/example.mkv',DETAIL='postgres://fixture:synthetic-secret@invalid/fixture'; END $$`)
			if err != nil {
				t.Fatal("create private failure trigger")
			}
			trigger := `CREATE TRIGGER fault_deny_probe_checkpoint AFTER UPDATE ON probe_job_state FOR EACH ROW EXECUTE FUNCTION fault_deny_probe_checkpoint()`
			if mode == "deferred_commit" {
				trigger = `CREATE CONSTRAINT TRIGGER fault_deny_probe_checkpoint AFTER UPDATE ON probe_job_state DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fault_deny_probe_checkpoint()`
			}
			if _, err = f.s.Pool.Exec(f.ctx, trigger); err != nil {
				t.Fatal("install private failure trigger")
			}
			before := probeFaultSnapshot(t, f)
			completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}
			if mode == "checkpoint_negative" {
				completion.Kind, completion.Metadata, completion.FailureCode = domain.ProbeCompletionFailed, nil, domain.ProbeFailureMedia
			}
			phase, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion})
			requireProbeFault(t, err, domain.ErrDatabase, phase)
			requireProbeSnapshot(t, f, before)
			f.quota(t, 1, 1)
			if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fault_deny_probe_checkpoint ON probe_job_state`); err != nil {
				t.Fatal("remove private failure trigger")
			}
			phase, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion})
			if err != nil || phase.Progress.Processed != 1 || phase.Token.Revision != page.Token.Revision+1 {
				t.Fatal("retry after storage recovery did not checkpoint exactly once")
			}
			f.quota(t, 1, 0)
		})
	}
}

func TestProbeFaultReservationWriteFailureReleasesProvisionalQuota(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "failed-reservation", "one.mkv")
	page, candidates := f.page(t, l)
	_, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION fault_probe_disk_full() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='53100',MESSAGE='private volume /private/media is full'; END $$; CREATE TRIGGER fault_probe_disk_full BEFORE INSERT ON probe_cache FOR EACH ROW EXECUTE FUNCTION fault_probe_disk_full()`)
	if err != nil {
		t.Fatal("install private reservation fault")
	}
	before := probeFaultSnapshot(t, f)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	requireProbeFault(t, err, domain.ErrDatabase, lease)
	requireProbeSnapshot(t, f, before)
	f.quota(t, 0, 0)
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fault_probe_disk_full ON probe_cache`); err != nil {
		t.Fatal("remove private reservation fault")
	}
	lease, err = f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil || lease.Generation < 1 {
		t.Fatal("failed reservation stranded a quota slot")
	}
	f.quota(t, 1, 1)
}

func TestProbeFaultCancellationAfterCacheWriteRollsBack(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "cancel-after-write", "one.mkv")
	page, candidates := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION fault_hold_probe_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema()),17481205); RETURN NEW; END $$; CREATE TRIGGER fault_hold_probe_checkpoint AFTER UPDATE ON probe_job_state FOR EACH ROW EXECUTE FUNCTION fault_hold_probe_checkpoint()`)
	if err != nil {
		t.Fatal("install private checkpoint latch")
	}
	holder, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(f.ctx)
	if _, err = holder.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481205)`); err != nil {
		t.Fatal("hold private checkpoint latch")
	}
	before := probeFaultSnapshot(t, f)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}
	type outcome struct {
		phase domain.ProbePhase
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		phase, e := f.s.CommitProbeBatch(ctx, l, page.Token, []domain.ProbeCompletion{completion})
		result <- outcome{phase, e}
	}()
	joined := false
	defer func() {
		cancel()
		_ = holder.Rollback(f.ctx)
		if !joined {
			select {
			case <-result:
			case <-time.After(3 * time.Second):
				t.Error("fault-test writer failed to join after cancellation and latch release")
			}
		}
	}()
	// This lock is requested only after cache metadata and quota SQL complete.
	// Observing the server wait proves cancellation occurs after those writes.
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=17481205)`).Scan(&waiting)
		if err != nil {
			t.Fatal("observe private checkpoint latch")
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("commit did not reach its checkpoint latch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case got := <-result:
		joined = true
		requireProbeFault(t, got.err, context.Canceled, got.phase)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled writer did not return")
	}
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal("release private checkpoint latch")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fault_hold_probe_checkpoint ON probe_job_state`); err != nil {
		t.Fatal("remove private checkpoint latch")
	}
	requireProbeSnapshot(t, f, before)
	f.quota(t, 1, 1)
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion}); err != nil {
		t.Fatal("cancelled writer retained a database lock or corrupted its lease")
	}
	f.quota(t, 1, 0)
}

func TestProbeFaultUnavailablePoolAndCancelledCallsAreAtomic(t *testing.T) {
	f := newProbeFixture(t)
	l, phase := f.begin(t, "unavailable-pool", "one.mkv")
	page, candidates := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	// Keep the fixture's live pool as an independent oracle. Close only this
	// separately opened pool; no shared server or other test process is stopped.
	unavailable, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("open private second pool")
	}
	unavailable.Pool.Close()
	before := probeFaultSnapshot(t, f)
	for _, mode := range []string{"cancelled", "pool_closed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			s, want := f.s, context.Canceled
			if mode == "cancelled" {
				cancel()
			} else {
				s, want = unavailable, domain.ErrDatabase
			}
			completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}
			calls := []struct {
				name string
				run  func() (any, error)
			}{
				{"policy", func() (any, error) { return nil, s.EnsureProbePolicy(ctx, domain.DefaultProbeCachePolicy()) }},
				{"register", func() (any, error) { return s.RegisterProbeIdentity(ctx, probeTestIdentity()) }},
				{"begin", func() (any, error) { return s.BeginProbePhase(ctx, l, phase.Start) }},
				{"page", func() (any, error) { return s.NextProbePage(ctx, l, 1) }},
				{"lookup", func() (any, error) { return s.LookupProbeBatch(ctx, l, page.Token, candidates) }},
				{"reserve", func() (any, error) { return s.AcquireProbe(ctx, l, page.Token, candidates[0]) }},
				{"commit", func() (any, error) {
					return s.CommitProbeBatch(ctx, l, page.Token, []domain.ProbeCompletion{completion})
				}},
				{"release", func() (any, error) { return nil, s.ReleaseProbeLease(ctx, l, lease) }},
				{"finish", func() (any, error) { return s.FinishProbePhase(ctx, l) }},
				{"abort", func() (any, error) { return nil, s.AbortProbePhase(ctx, l, domain.ProbePhaseRuntimeUnavailable) }},
				{"sweep", func() (any, error) { return s.SweepProbeCache(ctx, 1) }},
				{"invalidate_library", func() (any, error) { return nil, s.InvalidateProbeLibrary(ctx, f.a, f.registration.Library.ID) }},
				{"invalidate_item", func() (any, error) { return nil, s.InvalidateProbeItem(ctx, f.a, candidates[0].InventoryID) }},
			}
			for _, call := range calls {
				t.Run(call.name, func(t *testing.T) {
					value, err := call.run()
					requireProbeFault(t, err, want, value)
				})
			}
			requireProbeSnapshot(t, f, before)
			f.quota(t, 1, 1)
		})
	}
}

func TestProbeFaultPersistedIdentityMismatchCannotBeReused(t *testing.T) {
	f := newProbeFixture(t)
	l := f.scanned(t, "untrusted-identity", "one.mkv")
	wrong := f.identity
	wrong.Digest = strings.Repeat("f", 64)
	before := probeFaultSnapshot(t, f)
	phase, err := f.s.BeginProbePhase(f.ctx, l, domain.ProbePhaseStart{Identity: wrong, Scope: domain.ProbeScopeIncremental})
	requireProbeFault(t, err, domain.ErrProbeIdentityMismatch, phase)
	requireProbeSnapshot(t, f, before)
	// Simulate an inconsistent restored row in this private schema. Production
	// writers cannot update identities because of the immutable-row trigger.
	_, err = f.s.Pool.Exec(f.ctx, `ALTER TABLE tool_versions DISABLE TRIGGER probe_identity_immutable; UPDATE tool_versions SET vendor_version='untrusted-restored-row'; ALTER TABLE tool_versions ENABLE TRIGGER probe_identity_immutable`)
	if err != nil {
		t.Fatal("prepare private inconsistent identity fixture")
	}
	before = probeFaultSnapshot(t, f)
	ref, err := f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	requireProbeFault(t, err, domain.ErrProbeIdentityMismatch, ref)
	requireProbeSnapshot(t, f, before)
	f.quota(t, 0, 0)
}

func TestProbeFaultDeleteDenialRestoresQuotaAndLease(t *testing.T) {
	for _, mode := range []string{"sweep_expired", "release_child", "capacity_eviction"} {
		t.Run(mode, func(t *testing.T) {
			policy := domain.DefaultProbeCachePolicy()
			policy.MaxRows, policy.LibraryMaxRows = 1000, 1000
			f := newProbeFixture(t, policy)
			l, _ := f.begin(t, "delete-denied", "incoming.mkv")
			page, candidates := f.page(t, l)
			var operation func() (any, error)
			var rowsBefore, leasesBefore int64
			if mode == "capacity_eviction" {
				// Real retained rows fill the policy limit; eviction must delete a
				// row before a new reservation can be admitted.
				seedAdversarialProbeRows(t, f, f.registration.RootID, 1000)
				rowsBefore = 1000
				operation = func() (any, error) {
					return f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
				}
			} else {
				lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
				if err != nil {
					t.Fatal(err)
				}
				rowsBefore, leasesBefore = 1, 1
				operation = func() (any, error) { return nil, f.s.ReleaseProbeLease(f.ctx, l, lease) }
				if mode == "sweep_expired" {
					completion := domain.ProbeCompletion{Candidate: candidates[0], Kind: domain.ProbeCompletionFailed, Lease: &lease, FailureCode: domain.ProbeFailureMedia}
					if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion}); err != nil {
						t.Fatal(err)
					}
					if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET expires_at=clock_timestamp()-interval '1 second',retry_after=clock_timestamp()-interval '1 second'`); err != nil {
						t.Fatal("expire private negative-cache fixture")
					}
					leasesBefore = 0
					operation = func() (any, error) { return f.s.SweepProbeCache(f.ctx, 1) }
				}
			}
			f.quota(t, rowsBefore, leasesBefore)
			_, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION fault_deny_probe_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='42501',MESSAGE='DELETE denied for private cache cleanup'; END $$; CREATE TRIGGER fault_deny_probe_delete BEFORE DELETE ON probe_cache FOR EACH ROW EXECUTE FUNCTION fault_deny_probe_delete()`)
			if err != nil {
				t.Fatal("install private deletion fault")
			}
			before := probeFaultSnapshot(t, f)
			value, err := operation()
			requireProbeFault(t, err, domain.ErrDatabase, value)
			requireProbeSnapshot(t, f, before)
			f.quota(t, rowsBefore, leasesBefore)
			if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fault_deny_probe_delete ON probe_cache`); err != nil {
				t.Fatal("remove private deletion fault")
			}
			value, err = operation()
			if err != nil {
				t.Fatal("cleanup could not retry after deletion permission recovered")
			}
			if mode == "capacity_eviction" {
				if value.(domain.ProbeLease).Generation < 1 {
					t.Fatal("recovered eviction returned no reservation")
				}
				var remaining int64
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM probe_cache`).Scan(&remaining); err != nil || remaining > 1000 || remaining < 1001-domain.ProbeSweepMax {
					t.Fatal("recovered eviction did not respect the row and batch limits")
				}
				f.quota(t, remaining, 1)
			} else {
				if mode == "sweep_expired" {
					result := value.(domain.ProbeSweepResult)
					if result.Deleted != 1 || result.ReleasedLeases != 0 || result.FreedBytes != domain.ProbeRowAllowanceBytes {
						t.Fatal("recovered sweep did not report the actual removed row")
					}
				}
				f.quota(t, 0, 0)
			}
		})
	}
}
