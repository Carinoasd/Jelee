package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func ignoreTestIntent() domain.IgnoreIntent {
	return domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}
}

func ignoreSubmit(t *testing.T, f jobFixture, key string) domain.Job {
	t.Helper()
	j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
	if err != nil || replay {
		t.Fatal("enabled ignore submission", err)
	}
	return j
}

func ignoreRequest(t *testing.T, f jobFixture, id string) domain.IgnoreRequest {
	t.Helper()
	var r domain.IgnoreRequest
	var bit bool
	err := f.s.Pool.QueryRow(f.ctx, `SELECT j.ignore_requested,r.job_id::text,r.library_id::text,r.mode,r.case_mode,r.program_version,r.proof_version FROM jobs j JOIN job_ignore_requests r ON r.job_id=j.id WHERE j.id=$1::uuid`, id).Scan(&bit, &r.JobID, &r.LibraryID, &r.Intent.Mode, &r.Intent.CaseMode, &r.Identity.ProgramVersion, &r.Identity.ProofVersion)
	if err != nil || !bit || domain.ValidateIgnoreRequest(r) != nil {
		t.Fatal("enabled marker and retained request disagree", err)
	}
	return r
}

// This test-only SQL manufactures a live lease for an otherwise unclaimable
// job, to prove direct repository methods cannot bypass capability admission.
func ignoreManufacturedLease(t *testing.T, f jobFixture, id string) domain.JobLease {
	t.Helper()
	l, err := scanLease(f.s.Pool.QueryRow(f.ctx, `UPDATE jobs SET state='running',owner='ignore-contract-worker',generation=generation+1,attempts=attempts+1,lease_until=clock_timestamp()+interval '1 minute',started_at=COALESCE(started_at,clock_timestamp()) WHERE id=$1::uuid AND state='queued' RETURNING `+leaseColumns, id))
	if err != nil {
		t.Fatal("manufacture isolated test lease", err)
	}
	return l
}

func ignoreSnapshot(t *testing.T, f jobFixture) string {
	t.Helper()
	var out string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),
 'ignore',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM job_ignore_requests r),
 'directories',(SELECT jsonb_agg(to_jsonb(d) ORDER BY job_id,root_id,path) FROM job_directories d),
 'inventory',(SELECT jsonb_agg(to_jsonb(i) ORDER BY job_id,root_id,path) FROM job_inventory i),
 'baseline',(SELECT jsonb_agg(to_jsonb(b) ORDER BY library_id,root_id,path) FROM library_inventory_baseline b),
 'images',(SELECT jsonb_agg(to_jsonb(i) ORDER BY job_id) FROM image_job_state i),
 'libraries',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM libraries l),
 'nfoRequests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM nfo_job_requests r),
 'nfoPhase',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM nfo_job_state p),
 'nfoQuota',(SELECT to_jsonb(q) FROM nfo_cache_quota q),
 'nfoScopes',(SELECT jsonb_agg(to_jsonb(q) ORDER BY library_id) FROM nfo_library_quota q),
 'probeRequests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM probe_requests r),
 'probePhase',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM probe_job_state p),
 'probeQuota',(SELECT to_jsonb(q) FROM probe_cache_quota q),
 'probeScopes',(SELECT jsonb_agg(to_jsonb(q) ORDER BY library_id) FROM probe_library_quota q),
 'tools',(SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM tool_versions v),
 'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_logs a))::text`).Scan(&out)
	if err != nil {
		t.Fatal("snapshot isolated ignore test state", err)
	}
	return out
}

func TestIgnoreRequestConcurrentReplayAndLiveAdministration(t *testing.T) {
	f := newJobFixture(t)
	type result struct {
		job    domain.Job
		replay bool
		err    error
	}
	const callers = 8
	results := make(chan result, callers)
	var joined sync.WaitGroup
	for range callers {
		joined.Go(func() {
			j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "same-key", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
			results <- result{j, replay, err}
		})
	}
	joined.Wait()
	close(results)
	id, created := "", 0
	for result := range results {
		if result.err != nil {
			t.Fatal("concurrent ignore submission", result.err)
		}
		if id == "" {
			id = result.job.ID
		}
		if result.job.ID != id {
			t.Fatal("same key created multiple jobs")
		}
		if !result.replay {
			created++
		}
	}
	if created != 1 || ignoreRequest(t, f, id).Identity != domain.DefaultIgnoreIdentity() {
		t.Fatal("submission did not retain one trusted identity")
	}
	var jobs, requests, audit int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM jobs),(SELECT count(*) FROM job_ignore_requests),(SELECT count(*) FROM audit_logs WHERE event='job.submitted')`).Scan(&jobs, &requests, &audit); err != nil {
		t.Fatal("read submission cardinality", err)
	}
	if jobs != 1 || requests != 1 || audit != 1 {
		t.Fatal("replay repeated submission effects")
	}
	regular := createAccount(t, f.ctx, f.s, f.a, "ignore-viewer")
	if err := f.s.ReplaceLibraryAccess(f.ctx, f.a, regular.ID, []string{f.registration.Library.ID}); err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, f.ctx, f.s, "ignore-viewer"))
	for _, key := range []string{"same-key", "new-key"} {
		j, replay, err := f.s.SubmitScanWithStages(f.ctx, actor, f.registration.Library.ID, key, domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
		if !errors.Is(err, domain.ErrForbidden) || j != (domain.Job{}) || replay {
			t.Fatal("ordinary library member submitted or replayed ignore job", err)
		}
	}
	if err := f.s.RevokeSession(f.ctx, f.a, f.a.UserID, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "same-key", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
	if !errors.Is(err, domain.ErrUnauthenticated) || j != (domain.Job{}) || replay {
		t.Fatal("revoked admin replay bypassed live authorization", err)
	}
}

func TestIgnoreRequestReplayComparesModeCaseAndOff(t *testing.T) {
	f := newJobFixture(t)
	j := ignoreSubmit(t, f, "enabled")
	for _, intent := range []domain.IgnoreIntent{{}, {Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseASCIIInsensitive}} {
		got, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "enabled", domain.JobPriorityManual, domain.ScanIntent{Ignore: intent}, f.policy, nil, nil)
		if !errors.Is(err, domain.ErrConflict) || got != (domain.Job{}) || replay {
			t.Fatal("changed ignore intent replayed", err)
		}
	}
	if _, _, err := f.s.SubmitJob(f.ctx, f.a, j.LibraryID, "enabled", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old off wrapper replayed enabled intent", err)
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	off := f.submit(t, "off")
	if _, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, off.LibraryID, "off", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("off job acquired ignore intent during replay", err)
	}
	before := ignoreSnapshot(t, f)
	for _, bad := range []domain.IgnoreIntent{{Mode: "unregistered", CaseMode: domain.IgnoreCaseSensitive}, {Mode: domain.IgnoreModeJeleeignore}, {CaseMode: domain.IgnoreCaseSensitive}} {
		if _, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "invalid", domain.JobPriorityManual, domain.ScanIntent{Ignore: bad}, f.policy, nil, nil); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("unsupported intent accepted", err)
		}
	}
	if before != ignoreSnapshot(t, f) {
		t.Fatal("rejected intent changed persistent state")
	}
}

func TestIgnoreRequestEveryRetryWrapperPreservesIntent(t *testing.T) {
	for _, wrapper := range []string{"job", "probe", "stages"} {
		t.Run(wrapper, func(t *testing.T) {
			f := newJobFixture(t)
			parent := ignoreSubmit(t, f, "parent")
			original := ignoreRequest(t, f, parent.ID)
			if _, err := f.s.CancelJob(f.ctx, f.a, parent.ID); err != nil {
				t.Fatal(err)
			}
			retry := func() (domain.Job, bool, error) {
				switch wrapper {
				case "job":
					return f.s.RetryJob(f.ctx, f.a, parent.ID, "retry", f.policy)
				case "probe":
					return f.s.RetryScanJob(f.ctx, f.a, parent.ID, "retry", f.policy, nil)
				default:
					return f.s.RetryScanWithStages(f.ctx, f.a, parent.ID, "retry", f.policy, nil, nil)
				}
			}
			next, replay, err := retry()
			if err != nil || replay || next.ID == parent.ID {
				t.Fatal("retry did not create a new run", err)
			}
			request := ignoreRequest(t, f, next.ID)
			if request.Intent != original.Intent || request.Identity != original.Identity || request.LibraryID != original.LibraryID {
				t.Fatal("retry dropped or repinned ignore contract")
			}
			got, replay, err := retry()
			if err != nil || !replay || got.ID != next.ID {
				t.Fatal("retry replay did not retain original run", err)
			}
			if _, err = f.s.ClaimJobWithCapabilities(f.ctx, "cannot-run", false, time.Minute, domain.ScanCapabilities{Probe: true, NFO: true}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("retry became executable without ignore capability", err)
			}
		})
	}
}

func TestIgnoreRequestMissingRowCannotDowngradeMarker(t *testing.T) {
	f := newJobFixture(t)
	j := ignoreSubmit(t, f, "missing-request")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_requests WHERE job_id=$1::uuid`, j.ID); err != nil {
		t.Fatal(err)
	}
	for _, intent := range []domain.IgnoreIntent{ignoreTestIntent(), {}} {
		got, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "missing-request", domain.JobPriorityManual, domain.ScanIntent{Ignore: intent}, f.policy, nil, nil)
		if !errors.Is(err, domain.ErrConflict) || got != (domain.Job{}) || replay {
			t.Fatal("missing row became an off replay", err)
		}
	}
	if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "cannot-run", false, time.Minute, domain.ScanCapabilities{Ignore: true}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing row disabled the claim guard", err)
	}
	l := ignoreManufacturedLease(t, f, j.ID)
	if _, err := f.s.NextScanDirectory(f.ctx, l); !errors.Is(err, domain.ErrIgnoreUnavailable) {
		t.Fatal("missing row bypassed direct execution guard", err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if got, replay, err := f.s.RetryJob(f.ctx, f.a, j.ID, "retry-missing", f.policy); !errors.Is(err, domain.ErrConflict) || got != (domain.Job{}) || replay {
		t.Fatal("missing parent request retried as off", err)
	}
}

func TestIgnoreRequestInsertFailureRollsBackAllSubmissionEffects(t *testing.T) {
	f := newNFOFixture(t)
	f.jobFixture.complete(t, "baseline", []string{"existing.mkv"}, 0)
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION deny_ignore_request_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private test insertion rejected' USING ERRCODE='42501'; END $$; CREATE TRIGGER deny_ignore_request_test BEFORE INSERT ON job_ignore_requests FOR EACH ROW EXECUTE FUNCTION deny_ignore_request_test()`); err != nil {
		t.Fatal(err)
	}
	before := ignoreSnapshot(t, f.jobFixture)
	probe := probeTestIdentity()
	intent := domain.ScanIntent{Probe: domain.ProbeIntent{Scope: domain.ProbeScopeIncremental}, NFO: true, Ignore: ignoreTestIntent()}
	j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "rollback", domain.JobPriorityManual, intent, f.policy, &probe, &f.identity)
	if err != domain.ErrDatabase || j != (domain.Job{}) || replay || before != ignoreSnapshot(t, f.jobFixture) {
		t.Fatal("failed ignore insert leaked job/frontier/phase/identity/audit or raw error", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER deny_ignore_request_test ON job_ignore_requests`); err != nil {
		t.Fatal(err)
	}
	if _, replay, err = f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "rollback", domain.JobPriorityManual, intent, f.policy, &probe, &f.identity); err != nil || replay {
		t.Fatal("rolled-back key remained consumed", err)
	}
}

func TestIgnoreRequestAllClaimWrappersLeaveEnabledQueued(t *testing.T) {
	f := newJobFixture(t)
	enabled := ignoreSubmit(t, f, "enabled")
	other, err := f.s.RegisterLibrary(f.ctx, "claimable", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct {
		name  string
		claim func() (domain.JobLease, error)
	}{
		{"job", func() (domain.JobLease, error) { return f.s.ClaimJob(f.ctx, "job", false, time.Minute) }},
		{"probe-false", func() (domain.JobLease, error) {
			return f.s.ClaimJobWithProbe(f.ctx, "probe-false", true, time.Minute, false)
		}},
		{"probe-true", func() (domain.JobLease, error) {
			return f.s.ClaimJobWithProbe(f.ctx, "probe-true", false, time.Minute, true)
		}},
		{"all-false", func() (domain.JobLease, error) {
			return f.s.ClaimJobWithCapabilities(f.ctx, "all-false", true, time.Minute, domain.ScanCapabilities{})
		}},
		{"without-ignore", func() (domain.JobLease, error) {
			return f.s.ClaimJobWithCapabilities(f.ctx, "without-ignore", false, time.Minute, domain.ScanCapabilities{Probe: true, NFO: true})
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			off, _, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "off-"+tc.name, domain.JobPriorityManual, f.policy)
			if err != nil {
				t.Fatal(err)
			}
			l, err := tc.claim()
			if err != nil || l.Job.ID != off.ID {
				t.Fatal("claim selected enabled or starved executable off job", err)
			}
			if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
				t.Fatal(err)
			}
			if got, err := tc.claim(); !errors.Is(err, domain.ErrNotFound) || !reflect.DeepEqual(got, domain.JobLease{}) {
				t.Fatal("C1 capability admitted enabled work", err)
			}
		})
	}
	if got := f.get(t, enabled.ID); got.State != domain.JobQueued || got.Attempts != 0 {
		t.Fatal("skipped enabled job mutated during claims")
	}
}

func TestIgnoreRequestDirectExecutionIsClosedEvenWithLiveLease(t *testing.T) {
	f := newNFOFixture(t)
	f.jobFixture.complete(t, "baseline", []string{"previous.mkv"}, 0)
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	probe := probeTestIdentity()
	ref, err := f.s.RegisterProbeIdentity(f.ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	intent := domain.ScanIntent{Probe: domain.ProbeIntent{Scope: domain.ProbeScopeIncremental}, NFO: true, Ignore: ignoreTestIntent()}
	j, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "direct", domain.JobPriorityManual, intent, f.policy, &probe, &f.identity)
	if err != nil {
		t.Fatal(err)
	}
	l := ignoreManufacturedLease(t, f.jobFixture, j.ID)
	var d domain.ScanDirectory
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text,path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&d.RootID, &d.RootPath); err != nil {
		t.Fatal(err)
	}
	d.Path = "."
	start := domain.ProbePhaseStart{Identity: ref, Scope: domain.ProbeScopeIncremental}
	calls := []struct {
		name string
		call func() (any, error)
		zero any
	}{
		{"next-directory", func() (any, error) { return f.s.NextScanDirectory(f.ctx, l) }, domain.ScanDirectory{}},
		{"save-batch", func() (any, error) {
			return nil, f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: []domain.InventoryEntry{scanEntry(d, "must-not-persist.mkv", 7)}, Done: true})
		}, nil},
		{"prepare-nfo", func() (any, error) { return f.s.PrepareNFOPhase(f.ctx, l, f.identity) }, domain.NFOPhase{}},
		{"load-nfo-work", func() (any, error) { return f.s.LoadNFOWork(f.ctx, l) }, domain.NFOWork{}},
		{"begin-nfo", func() (any, error) { return f.s.BeginNFOPhase(f.ctx, l) }, domain.NFOPhase{}},
		{"begin-requested-nfo", func() (any, error) { return f.s.BeginRequestedNFOPhase(f.ctx, l) }, domain.NFOPhase{}},
		{"next-nfo-page", func() (any, error) { return f.s.NextNFOPage(f.ctx, l, 1) }, domain.NFOPage{}},
		{"finish-nfo", func() (any, error) { return f.s.FinishNFOPhase(f.ctx, l) }, domain.NFOPhase{}},
		{"load-probe-work", func() (any, error) { return f.s.LoadProbeWork(f.ctx, l) }, domain.ProbeWork{}},
		{"begin-probe", func() (any, error) { return f.s.BeginProbePhase(f.ctx, l, start) }, domain.ProbePhase{}},
		{"begin-requested-probe", func() (any, error) { return f.s.BeginRequestedProbePhase(f.ctx, l) }, domain.ProbePhase{}},
		{"next-probe-page", func() (any, error) { return f.s.NextProbePage(f.ctx, l, 1) }, domain.ProbePage{}},
		{"finish-probe", func() (any, error) { return f.s.FinishProbePhase(f.ctx, l) }, domain.ProbePhase{}},
		{"finish-success", func() (any, error) { return nil, f.s.FinishJob(f.ctx, l, domain.JobSucceeded, "") }, nil},
	}
	before := ignoreSnapshot(t, f.jobFixture)
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err != domain.ErrIgnoreUnavailable || !reflect.DeepEqual(got, tc.zero) {
				t.Fatal("direct execution did not return the fixed error and zero result", err)
			}
			if before != ignoreSnapshot(t, f.jobFixture) {
				t.Fatal("rejected execution changed inventory/baseline or stage state")
			}
		})
	}
}

func TestIgnoreRequestFailureCancellationAndOffRecoveryPreserveBaseline(t *testing.T) {
	for _, terminal := range []string{"failure", "finish-cancel", "release-cancel", "queued-cancel"} {
		t.Run(terminal, func(t *testing.T) {
			f := newJobFixture(t)
			f.complete(t, "baseline", []string{"one.mkv", "two.mkv"}, 0)
			baseline := imageBaseline(t, f)
			j := ignoreSubmit(t, f, "enabled")
			if terminal == "queued-cancel" {
				if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				l := ignoreManufacturedLease(t, f, j.ID)
				var err error
				switch terminal {
				case "failure":
					err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable")
				case "finish-cancel":
					_, err = f.s.CancelJob(f.ctx, f.a, j.ID)
					if err == nil {
						err = f.s.FinishJob(f.ctx, l, domain.JobCancelled, "")
					}
				case "release-cancel":
					_, err = f.s.CancelJob(f.ctx, f.a, j.ID)
					if err == nil {
						err = f.s.ReleaseJob(f.ctx, l)
					}
				}
				if err != nil {
					t.Fatal("enabled task could not terminate safely", err)
				}
			}
			got := f.get(t, j.ID)
			want := domain.JobCancelled
			if terminal == "failure" {
				want = domain.JobFailed
			}
			if got.State != want || got.Missing != 0 || got.Files != 0 || imageBaseline(t, f) != baseline {
				t.Fatal("enabled termination inferred missing or replaced baseline")
			}
			image, err := f.s.GetImageJobSummary(f.ctx, f.a, j.ID)
			if err != nil || image.Missing != 0 || image.ComparisonComplete {
				t.Fatal("unexecuted enabled job claimed image comparison", err)
			}
			// A terminated enabled request creates no permanent library gate.
			var revision int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT inventory_baseline_revision FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			completed := f.complete(t, "off-after", []string{"one.mkv", "two.mkv"}, 0)
			var sameMetadata, advanced bool
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT
 (SELECT COALESCE(jsonb_agg(to_jsonb(b)-'observed_revision' ORDER BY root_id,path),'[]'::jsonb) FROM library_inventory_baseline b WHERE library_id=$1::uuid)=
 (SELECT COALESCE(jsonb_agg(v-'observed_revision' ORDER BY v->>'root_id',v->>'path'),'[]'::jsonb) FROM jsonb_array_elements($2::jsonb) v),
 inventory_baseline_revision=$3+1 AND NOT EXISTS(SELECT 1 FROM library_inventory_baseline b WHERE b.library_id=l.id AND b.observed_revision<>l.inventory_baseline_revision)
 FROM libraries l WHERE id=$1::uuid`, f.registration.Library.ID, baseline, revision).Scan(&sameMetadata, &advanced); err != nil {
				t.Fatal(err)
			}
			if completed.State != domain.JobSucceeded || completed.Missing != 0 || !sameMetadata || !advanced {
				t.Fatal("ordinary off inventory did not remain functional")
			}
		})
	}
}

func TestIgnoreRequestOrphanRowCannotAuthorizeOffExecution(t *testing.T) {
	f := newJobFixture(t)
	j := ignoreSubmit(t, f, "orphan")
	// Only this isolated test schema bypasses the immutable marker trigger to
	// model inconsistent retained data. Restore the trigger before API calls.
	if _, err := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs DISABLE TRIGGER job_ignore_requested_immutable`); err != nil {
		t.Fatal(err)
	}
	_, mutateErr := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET ignore_requested=false WHERE id=$1::uuid`, j.ID)
	_, restoreErr := f.s.Pool.Exec(f.ctx, `ALTER TABLE jobs ENABLE TRIGGER job_ignore_requested_immutable`)
	if mutateErr != nil || restoreErr != nil {
		t.Fatal("manufacture isolated inconsistent marker", mutateErr, restoreErr)
	}
	for _, intent := range []domain.IgnoreIntent{{}, ignoreTestIntent()} {
		if got, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "orphan", domain.JobPriorityManual, domain.ScanIntent{Ignore: intent}, f.policy, nil, nil); !errors.Is(err, domain.ErrConflict) || got != (domain.Job{}) || replay {
			t.Fatal("orphan request replayed as off or enabled", err)
		}
	}
	if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "orphan-worker", false, time.Minute, domain.ScanCapabilities{Ignore: true}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("request without marker was claimed", err)
	}
	l := ignoreManufacturedLease(t, f, j.ID)
	before := ignoreSnapshot(t, f)
	if _, err := f.s.NextScanDirectory(f.ctx, l); err != domain.ErrIgnoreUnavailable {
		t.Fatal("orphan row bypassed direct guard", err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrIgnoreUnavailable || before != ignoreSnapshot(t, f) {
		t.Fatal("orphan row published successful unfiltered inventory", err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.RetryJob(f.ctx, f.a, j.ID, "orphan-retry", f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("orphan parent retried as off", err)
	}
}

func TestIgnoreRequestCancelledAndExpiredCallsHaveNoEffects(t *testing.T) {
	f := newJobFixture(t)
	j := ignoreSubmit(t, f, "parent")
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	before := ignoreSnapshot(t, f)
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	expired, stop := context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		got, replay, err := f.s.SubmitScanWithStages(ctx, f.a, j.LibraryID, "new", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
		if err == nil || got != (domain.Job{}) || replay {
			t.Fatal("cancelled admission returned a job", err)
		}
		got, replay, err = f.s.SubmitScanWithStages(ctx, f.a, j.LibraryID, "parent", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
		if err == nil || got != (domain.Job{}) || replay {
			t.Fatal("cancelled replay returned a job", err)
		}
		got, replay, err = f.s.RetryScanWithStages(ctx, f.a, j.ID, "retry", f.policy, nil, nil)
		if err == nil || got != (domain.Job{}) || replay {
			t.Fatal("cancelled retry returned a job", err)
		}
		if before != ignoreSnapshot(t, f) {
			t.Fatal("cancelled/expired admission changed retained state")
		}
	}
}

func TestIgnoreRequestExpiredRecoveryNeverRunsOrPublishesInventory(t *testing.T) {
	for _, mode := range []string{"requeue", "exhausted", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newJobFixture(t)
			f.complete(t, "baseline", []string{"kept.mkv"}, 0)
			baseline := imageBaseline(t, f)
			f.policy.MaxAttempts = 2
			if mode == "exhausted" {
				f.policy.MaxAttempts = 1
			}
			j := ignoreSubmit(t, f, "expiry")
			old := ignoreManufacturedLease(t, f, j.ID)
			if mode == "cancelled" {
				if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, j.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ClaimJobWithCapabilities(f.ctx, "recovery", false, time.Minute, domain.ScanCapabilities{Probe: true, NFO: true}); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("expiry recovery admitted an incapable worker", err)
			}
			want := domain.JobQueued
			if mode == "exhausted" {
				want = domain.JobFailed
			} else if mode == "cancelled" {
				want = domain.JobCancelled
			}
			got := f.get(t, j.ID)
			if got.State != want || got.Attempts != 1 || got.Missing != 0 || imageBaseline(t, f) != baseline {
				t.Fatal("expiry recovery changed baseline, attempts or terminal semantics")
			}
			if mode == "exhausted" && got.ErrorCode != "job_attempts_exhausted" {
				t.Fatal("attempt exhaustion lost its fixed code")
			}
			before := ignoreSnapshot(t, f)
			if _, err := f.s.NextScanDirectory(f.ctx, old); !errors.Is(err, domain.ErrJobLeaseLost) {
				t.Fatal("expired owner retained directory authority", err)
			}
			if err := f.s.FinishJob(f.ctx, old, domain.JobFailed, "scan_unavailable"); !errors.Is(err, domain.ErrJobLeaseLost) || before != ignoreSnapshot(t, f) {
				t.Fatal("expired owner altered recovered job", err)
			}
		})
	}
}

func TestIgnoreRequestDeadlineDuringInsertRollsBack(t *testing.T) {
	f := newJobFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE ignore_insert_entered_test; CREATE FUNCTION slow_ignore_insert_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('ignore_insert_entered_test'); PERFORM pg_sleep(3); RETURN NEW; END $$; CREATE TRIGGER slow_ignore_insert_test BEFORE INSERT ON job_ignore_requests FOR EACH ROW EXECUTE FUNCTION slow_ignore_insert_test()`); err != nil {
		t.Fatal(err)
	}
	before := ignoreSnapshot(t, f)
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	started := time.Now()
	j, replay, err := f.s.SubmitScanWithStages(ctx, f.a, f.registration.Library.ID, "deadline-insert", domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil)
	if err == nil || j != (domain.Job{}) || replay || !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(started) > 3*time.Second {
		t.Fatal("bounded request timeout did not fail with a zero result", err)
	}
	// nextval is not rolled back: prove the deadline interrupted the insert
	// trigger itself, rather than an earlier admission or lock operation.
	var entered bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM ignore_insert_entered_test`).Scan(&entered); err != nil || !entered {
		t.Fatal("deadline test did not reach the insertion trigger", err)
	}
	if before != ignoreSnapshot(t, f) {
		t.Fatal("timed-out request retained provisional submission effects")
	}
}
