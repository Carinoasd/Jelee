package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func requestSnapshot(t *testing.T, f probeFixture) string {
	t.Helper()
	var requests string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY job_id),'[]')::text FROM probe_requests r`).Scan(&requests); err != nil {
		t.Fatal("request snapshot failed")
	}
	return probeFaultSnapshot(t, f) + requests
}
func (f probeFixture) request(t *testing.T, key string, intent domain.ProbeIntent) domain.Job {
	t.Helper()
	identity := probeTestIdentity()
	library := f.registration.Library.ID
	if intent.Scope == domain.ProbeScopeItemRebuild {
		library = ""
	}
	j, replay, err := f.s.SubmitScanJob(f.ctx, f.a, library, key, domain.JobPriorityManual, intent, f.policy, &identity)
	if err != nil || replay {
		t.Fatal("submit requested scan failed", err)
	}
	return j
}
func (f probeFixture) claimProbe(t *testing.T) domain.JobLease {
	t.Helper()
	l, err := f.s.ClaimJobWithProbe(f.ctx, "capable-worker", false, time.Minute, true)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func (f probeFixture) scanRequested(t *testing.T, l domain.JobLease, names ...string) {
	t.Helper()
	d := f.directory(t, l)
	batch := domain.ScanBatch{Done: true}
	for _, name := range names {
		batch.Entries = append(batch.Entries, scanEntry(d, name, 7))
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, batch); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRequestConcurrentReplayAndCapabilityClaim(t *testing.T) {
	f := newProbeFixture(t)
	identity := probeTestIdentity()
	intent := domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}
	var before int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT probe_generation FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	type result struct {
		j      domain.Job
		replay bool
		err    error
	}
	out := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, r, e := f.s.SubmitScanJob(f.ctx, f.a, f.registration.Library.ID, "same-intent", domain.JobPriorityManual, intent, f.policy, &identity)
			out <- result{j, r, e}
		}()
	}
	wg.Wait()
	close(out)
	var job domain.Job
	created := 0
	for result := range out {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if job.ID != "" && job.ID != result.j.ID {
			t.Fatal("duplicate job")
		}
		job = result.j
		if !result.replay {
			created++
		}
	}
	if created != 1 {
		t.Fatal("request was not atomic")
	}
	var gen, requests, audits int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT probe_generation,(SELECT count(*) FROM probe_requests),(SELECT count(*) FROM audit_logs WHERE event='job.submitted') FROM libraries WHERE id=$1::uuid`, job.LibraryID).Scan(&gen, &requests, &audits); err != nil || gen != before+1 || requests != 1 || audits != 1 {
		t.Fatal("replay repeated side effects")
	}
	for _, runtime := range []*domain.ProbeIdentity{nil, func() *domain.ProbeIdentity { v := identity; v.VendorVersion = "upgraded-runtime"; return &v }()} {
		got, replay, err := f.s.SubmitScanJob(f.ctx, f.a, job.LibraryID, "same-intent", job.Priority, intent, f.policy, runtime)
		if err != nil || !replay || got.ID != job.ID {
			t.Fatal("retained replay repinned or required runtime", err)
		}
	}
	for _, changed := range []domain.ProbeIntent{{}, {Scope: domain.ProbeScopeIncremental}} {
		if _, _, err := f.s.SubmitScanJob(f.ctx, f.a, job.LibraryID, "same-intent", job.Priority, changed, f.policy, &identity); err != domain.ErrConflict {
			t.Fatal("different probe intent replayed", err)
		}
	}
	if _, _, err := f.s.SubmitJob(f.ctx, f.a, job.LibraryID, "same-intent", job.Priority, f.policy); err != domain.ErrConflict {
		t.Fatal("legacy submit ignored probe intent")
	}
	if _, _, err := f.s.SubmitScanJob(f.ctx, f.a, job.LibraryID, "same-intent", domain.JobPriorityBackground, intent, f.policy, &identity); err != domain.ErrConflict {
		t.Fatal("different priority replayed")
	}
	if _, err := f.s.SweepProbeCache(f.ctx, 128); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimJob(f.ctx, "disabled-worker", false, time.Minute); err != domain.ErrNotFound {
		t.Fatal("legacy claimant took probe request")
	}
	if _, err := f.s.ClaimJobWithProbe(f.ctx, "disabled-worker", false, time.Minute, false); err != domain.ErrNotFound {
		t.Fatal("disabled claimant took probe request")
	}
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, job.ID)
	if err != nil || !summary.Enabled || summary.Phase != domain.ProbeSummaryWaitingScan {
		t.Fatal("queued summary", err)
	}
	l := f.claimProbe(t)
	if l.Job.ID != job.ID || l.Job.Attempts != 1 {
		t.Fatal("excluded claim consumed attempt")
	}
	work, err := f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request == nil || work.Phase != nil || work.Request.LibraryGeneration != gen {
		t.Fatal("request lost before inventory", err)
	}
	if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("probe began before directory completion")
	}
	if _, err = f.s.BeginProbePhase(f.ctx, l, requestPhaseStart(work.Request)); err != domain.ErrConflict {
		t.Fatal("legacy begin bypassed request generations")
	}
	f.scanRequested(t, l)
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrConflict {
		t.Fatal("missing requested phase marked success")
	}
	p, err := f.s.BeginRequestedProbePhase(f.ctx, l)
	if err != nil || p.LibraryGeneration != gen {
		t.Fatal("begin bumped generation twice", err)
	}
	if again, e := f.s.BeginRequestedProbePhase(f.ctx, l); e != nil || again != p {
		t.Fatal("begin replay reset phase", e)
	}
	f.finish(t, l)
	summary, err = f.s.GetProbeJobSummary(f.ctx, f.a, job.ID)
	if err != nil || summary.Phase != domain.ProbeSummaryDone || summary.Processed != 0 || summary.ErrorCode != "" {
		t.Fatal("done summary", err)
	}
	raw, _ := json.Marshal(summary)
	for _, secret := range []string{"rootPath", "owner", "generation", "identity", "metadata", "fingerprint"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private worker field in public summary")
		}
	}
}

func TestProbeRequestItemResolutionAndQueuedInvalidation(t *testing.T) {
	for _, mutation := range []string{"stable", "root", "item"} {
		t.Run(mutation, func(t *testing.T) {
			f := newProbeFixture(t)
			item := f.importItem(t, "a.mkv")
			f.importItem(t, "b.mkv")
			identity := probeTestIdentity()
			intent := domain.ProbeIntent{Scope: domain.ProbeScopeItemRebuild, TargetItemID: item}
			other, err := f.s.RegisterLibrary(f.ctx, "unrelated", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			before := requestSnapshot(t, f)
			if _, _, err = f.s.SubmitScanJob(f.ctx, f.a, other.Library.ID, "wrong-library", domain.JobPriorityManual, intent, f.policy, &identity); err != domain.ErrNotFound {
				t.Fatal("item/library mismatch accepted", err)
			}
			if requestSnapshot(t, f) != before {
				t.Fatal("rejected item changed state")
			}
			j := f.request(t, "item-request", intent)
			l := f.claimProbe(t)
			work, err := f.s.LoadProbeWork(f.ctx, l)
			if err != nil || work.Request.LibraryID != f.registration.Library.ID || work.Request.Intent != intent {
				t.Fatal("item library not resolved", err)
			}
			f.scanRequested(t, l, "a.mkv", "b.mkv")
			if mutation == "stable" {
				phase, e := f.s.BeginRequestedProbePhase(f.ctx, l)
				if e != nil || phase.TargetItemGeneration != work.Request.TargetItemGeneration {
					t.Fatal("item begin changed the admitted generation", e)
				}
				page, _ := f.page(t, l)
				if len(page.Entries) != 1 || page.Entries[0].Source.RelativePath != "a.mkv" {
					t.Fatal("item rebuild included unrelated inventory")
				}
				f.saveHead(t, l)
				f.finish(t, l)
				summary, e := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
				if e != nil || summary.Processed != 1 || summary.TargetItemID != item || summary.Phase != domain.ProbeSummaryDone {
					t.Fatal("item summary disagrees with scoped work", e)
				}
				return
			}
			if mutation == "root" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=$2 WHERE id=$1::uuid`, f.registration.RootID, t.TempDir())
			} else {
				err = f.s.InvalidateProbeItem(f.ctx, f.a, item)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); err != domain.ErrProbeInvalidated {
				t.Fatal("queued generation changed silently", err)
			}
			if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseInvalidated); err != nil {
				t.Fatal(err)
			}
			if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
				t.Fatal(err)
			}
			summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
			if err != nil || summary.Phase != domain.ProbeSummaryAborted || summary.ErrorCode != string(domain.ProbePhaseInvalidated) {
				t.Fatal("invalidated summary", err)
			}
		})
	}
}

func TestProbeRequestRetryPreservesIntentAndPinsNewIdentity(t *testing.T) {
	f := newProbeFixture(t)
	intent := domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}
	old := f.request(t, "retry-original", intent)
	if _, err := f.s.CancelJob(f.ctx, f.a, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.RetryJob(f.ctx, f.a, old.ID, "legacy-retry", f.policy); err != domain.ErrProbeDisabled {
		t.Fatal("legacy retry discarded opt-in")
	}
	before := requestSnapshot(t, f)
	if _, _, err := f.s.RetryScanJob(f.ctx, f.a, old.ID, "disabled-retry", f.policy, nil); err != domain.ErrProbeDisabled {
		t.Fatal("disabled runtime admitted fresh retry")
	}
	if requestSnapshot(t, f) != before {
		t.Fatal("disabled retry changed state")
	}
	identity := probeTestIdentity()
	identity.VendorVersion = "new-verified-build"
	j, replay, err := f.s.RetryScanJob(f.ctx, f.a, old.ID, "retry", f.policy, &identity)
	if err != nil || replay {
		t.Fatal(err)
	}
	if again, r, e := f.s.RetryScanJob(f.ctx, f.a, old.ID, "retry", f.policy, nil); e != nil || !r || again.ID != j.ID {
		t.Fatal("disabled retry replay failed", e)
	}
	l := f.claimProbe(t)
	work, err := f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request.Intent != intent {
		t.Fatal("retry lost scope", err)
	}
	digest, _ := domain.ProbeIdentityDigest(identity)
	if work.Request.Identity.Digest != digest {
		t.Fatal("retry reused old identity")
	}
	var oldGen int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT library_generation FROM probe_requests WHERE job_id=$1::uuid`, old.ID).Scan(&oldGen); err != nil || work.Request.LibraryGeneration != oldGen+1 {
		t.Fatal("retry did not bump exactly once")
	}
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.NextScanDirectory(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("aborted request kept scanning")
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	l = f.claimProbe(t)
	work, err = f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request.ErrorCode != domain.ProbePhaseRuntimeUnavailable || work.Phase != nil {
		t.Fatal("pre-phase failure did not survive recovery", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrConflict {
		t.Fatal("aborted request succeeded")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
	if err != nil || summary.Phase != domain.ProbeSummaryAborted || summary.ErrorCode != string(domain.ProbePhaseRuntimeUnavailable) {
		t.Fatal("runtime error hidden", err)
	}
}

func TestProbeRequestCheckpointAndDoneRecovery(t *testing.T) {
	f := newProbeFixture(t)
	j := f.request(t, "resume", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	l := f.claimProbe(t)
	f.scanRequested(t, l, "a.mkv", "b.mkv")
	if _, err := f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.saveHead(t, l)
	page, c := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 1, 0)
	next := f.claimProbe(t)
	work, err := f.s.LoadProbeWork(f.ctx, next)
	if err != nil || work.Phase == nil || work.Phase.Progress.Processed != 1 || work.Phase.State != domain.ProbePhaseRunning {
		t.Fatal("checkpoint lost", err)
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}}); err != domain.ErrJobLeaseLost {
		t.Fatal("old owner wrote result", err)
	}
	f.saveHead(t, next)
	if _, err = f.s.FinishProbePhase(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseJob(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	last := f.claimProbe(t)
	work, err = f.s.LoadProbeWork(f.ctx, last)
	if err != nil || work.Phase.State != domain.ProbePhaseDone {
		t.Fatal("done checkpoint lost", err)
	}
	if err = f.s.AbortProbeRequest(f.ctx, last, domain.ProbePhaseRuntimeUnavailable); err != domain.ErrConflict {
		t.Fatal("done phase overwritten")
	}
	if err = f.s.FinishJob(f.ctx, last, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
	if err != nil || summary.Processed != 2 || summary.Succeeded != 2 || summary.Phase != domain.ProbeSummaryDone {
		t.Fatal("recovery double-counted or lost progress", err)
	}
	f.quota(t, 2, 0)
}

func TestProbeRequestEnqueueFaultsAreAtomic(t *testing.T) {
	for _, point := range []string{"insert", "commit", "admin_expiry"} {
		t.Run(point, func(t *testing.T) {
			f := newProbeFixture(t)
			identity := probeTestIdentity()
			identity.VendorVersion = "admission-only-new-build"
			trigger := `CREATE FUNCTION fail_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='42501',MESSAGE='private test denial'; END $$; CREATE TRIGGER fail_request BEFORE INSERT ON probe_requests FOR EACH ROW EXECUTE FUNCTION fail_request()`
			if point == "commit" {
				trigger = `CREATE FUNCTION fail_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='42501',MESSAGE='private deferred denial'; END $$; CREATE CONSTRAINT TRIGGER fail_request AFTER INSERT ON probe_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_request()`
			}
			if point == "admin_expiry" {
				trigger = `CREATE FUNCTION fail_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER fail_request BEFORE INSERT ON probe_requests FOR EACH ROW EXECUTE FUNCTION fail_request()`
			}
			if _, err := f.s.Pool.Exec(f.ctx, trigger); err != nil {
				t.Fatal("cannot install owned fault fixture")
			}
			if point == "admin_expiry" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, f.a.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			before := requestSnapshot(t, f)
			j, replay, err := f.s.SubmitScanJob(f.ctx, f.a, f.registration.Library.ID, "atomic-request", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}, f.policy, &identity)
			want := domain.ErrDatabase
			if point == "admin_expiry" {
				want = domain.ErrUnauthenticated
			}
			if err != want || j != (domain.Job{}) || replay {
				t.Fatal("enqueue fault leaked result or diagnostic")
			}
			if before != requestSnapshot(t, f) {
				t.Fatal("failed enqueue changed generation, identity, job, quota or audit")
			}
			if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER fail_request ON probe_requests`); err != nil {
				t.Fatal(err)
			}
			if point != "admin_expiry" {
				if _, _, err = f.s.SubmitScanJob(f.ctx, f.a, f.registration.Library.ID, "atomic-request", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}, f.policy, &identity); err != nil {
					t.Fatal("failed key could not retry", err)
				}
			}
		})
	}
}

func TestProbeRequestSweepReferencesAndDisabledFairness(t *testing.T) {
	f := newProbeFixture(t)
	probeJob := f.request(t, "probe-first", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	other, err := f.s.RegisterLibrary(f.ctx, "plain", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := f.s.SubmitJob(f.ctx, f.a, other.Library.ID, "plain", domain.JobPriorityBackground, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SweepProbeCache(f.ctx, 128); err != nil {
		t.Fatal(err)
	}
	var tools, scopes int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM tool_versions),(SELECT count(*) FROM probe_library_quota)`).Scan(&tools, &scopes); err != nil || tools != 1 || scopes != 1 {
		t.Fatal("sweep deleted queued request references")
	}
	l, err := f.s.ClaimJobWithProbe(f.ctx, "disabled", false, time.Minute, false)
	if err != nil || l.Job.ID != plain.ID {
		t.Fatal("disabled worker blocked unrelated scan", err)
	}
	work, err := f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request != nil || work.Phase != nil {
		t.Fatal("plain scan acquired opt-in state", err)
	}
	l, err = f.s.ClaimJobWithProbe(f.ctx, "capable", true, time.Minute, true)
	if err != nil || l.Job.ID != probeJob.ID {
		t.Fatal("capable fallback failed", err)
	}
	if _, err = f.s.SweepProbeCache(f.ctx, 128); err != nil {
		t.Fatal(err)
	}
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, probeJob.ID)
	if err != nil || summary.Phase != domain.ProbeSummaryCancelled || summary.ErrorCode != "" {
		t.Fatal("cancelled summary retained error", err)
	}
}

func TestProbeRequestMigrationGuardAndRollbackPreserveCache(t *testing.T) {
	f := newProbeFixture(t)
	old, _ := f.begin(t, "legacy-cache", "a.mkv")
	f.saveHead(t, old)
	f.finish(t, old)
	j := f.request(t, "queued-downgrade", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	before := requestSnapshot(t, f)
	body, err := migrationFiles.ReadFile("migrations/000005_probe_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rejected := conn.Exec(f.ctx, string(body))
	_, rollback := conn.Exec(f.ctx, "ROLLBACK")
	conn.Release()
	if rejected == nil || rollback != nil || before != requestSnapshot(t, f) {
		t.Fatal("active request downgrade was not refused atomically")
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	preserved := probeFaultSnapshot(t, f)
	dsn := f.s.Pool.Config().ConnString()
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
		t.Fatal("manifest downgrade failed", v, dirty, err)
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
	if v, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || v != 4 {
		t.Fatal("down005 failed")
	}
	if f.s.Ready(f.ctx) == nil {
		t.Fatal("schema4 passed current readiness")
	}
	if preserved != probeFaultSnapshot(t, f) {
		t.Fatal("down005 changed cache/phase/quota/catalog/jobs")
	}
	if v, dirty, e := Migrate(f.ctx, dsn, "up"); e != nil || dirty || v != SchemaVersion {
		t.Fatal("up005 failed")
	}
	f.quota(t, 1, 0)
}

func TestProbeRequestInvalidInputsAndCancelledCalls(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	policy := jobTestPolicy()
	identity := probeTestIdentity()
	for _, call := range []func() error{
		func() error {
			_, _, e := s.SubmitScanJob(ctx, domain.Actor{}, "", "key", domain.JobPriorityManual, domain.ProbeIntent{}, policy, nil)
			return e
		},
		func() error {
			_, _, e := s.SubmitScanJob(ctx, domain.Actor{}, "", "key", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeItemRebuild, TargetItemID: "bad"}, policy, &identity)
			return e
		},
		func() error {
			_, _, e := s.RetryScanJob(ctx, domain.Actor{}, "bad", "key", policy, &identity)
			return e
		},
		func() error { _, e := s.GetProbeJobSummary(ctx, domain.Actor{}, "bad"); return e },
		func() error { return s.AbortProbeRequest(ctx, domain.JobLease{}, "raw private error") },
	} {
		if e := call(); e == nil {
			t.Fatal("invalid input reached database")
		}
	}
	f := newProbeFixture(t)
	j := f.request(t, "cancel-api", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	l := f.claimProbe(t)
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	before := requestSnapshot(t, f)
	for _, call := range []func() error{
		func() error {
			_, _, e := f.s.SubmitScanJob(cancelled, f.a, j.LibraryID, "other", j.Priority, domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}, f.policy, &identity)
			return e
		},
		func() error { _, e := f.s.GetProbeJobSummary(cancelled, f.a, j.ID); return e },
		func() error { _, e := f.s.LoadProbeWork(cancelled, l); return e },
		func() error { _, e := f.s.BeginRequestedProbePhase(cancelled, l); return e },
		func() error { return f.s.AbortProbeRequest(cancelled, l, domain.ProbePhaseRuntimeUnavailable) },
	} {
		if e := call(); !errors.Is(e, context.Canceled) {
			t.Fatal("cancelled call proceeded", e)
		}
	}
	if before != requestSnapshot(t, f) {
		t.Fatal("cancelled request API mutated state")
	}
}

func TestProbeRequestAbortReleasesChildAndPreservesProgress(t *testing.T) {
	f := newProbeFixture(t)
	j := f.request(t, "abort-active", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	l := f.claimProbe(t)
	f.scanRequested(t, l, "a.mkv", "b.mkv")
	if _, err := f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.saveHead(t, l)
	page, candidates := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	f.quota(t, 2, 1)
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 1, 0)
	work, err := f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request.ErrorCode != domain.ProbePhaseRuntimeUnavailable || work.Phase.State != domain.ProbePhaseAborted || work.Phase.Progress.Processed != 1 {
		t.Fatal("abort lost progress or split request/phase status", err)
	}
	before := requestSnapshot(t, f)
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != nil {
		t.Fatal("same abort was not idempotent", err)
	}
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseInvalidated); err != domain.ErrConflict {
		t.Fatal("later error replaced durable first cause")
	}
	if before != requestSnapshot(t, f) {
		t.Fatal("abort replay changed durable state")
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}}); err == nil {
		t.Fatal("released child committed after abort")
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	l = f.claimProbe(t)
	work, err = f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Phase.State != domain.ProbePhaseAborted || work.Phase.Progress.Processed != 1 {
		t.Fatal("aborted work was lost after recovery", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
	if err != nil || summary.Phase != domain.ProbeSummaryAborted || summary.Processed != 1 || summary.Succeeded != 1 || summary.ErrorCode != string(domain.ProbePhaseRuntimeUnavailable) {
		t.Fatal("aborted summary lost committed prefix", err)
	}
}

func TestProbeRequestEnqueueSerializesWithSweep(t *testing.T) {
	f := newProbeFixture(t)
	_, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION hold_request() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema()),17481206); RETURN NEW; END $$; CREATE TRIGGER hold_request BEFORE INSERT ON probe_requests FOR EACH ROW EXECUTE FUNCTION hold_request()`)
	if err != nil {
		t.Fatal("install private admission latch")
	}
	holder, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(f.ctx)
	if _, err = holder.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481206)`); err != nil {
		t.Fatal("hold private admission latch")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	results := make(chan error, 2)
	started, joined := 1, 0
	defer func() {
		cancel()
		_ = holder.Rollback(f.ctx)
		for joined < started {
			select {
			case <-results:
				joined++
			case <-time.After(3 * time.Second):
				t.Error("request/sweep operation did not join")
				return
			}
		}
	}()
	identity := probeTestIdentity()
	identity.VendorVersion = "concurrent-admission"
	go func() {
		_, _, e := f.s.SubmitScanJob(ctx, f.a, f.registration.Library.ID, "sweep-race", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}, f.policy, &identity)
		results <- e
	}()
	waitLock := func(key int64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			var waiting bool
			if e := f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=$1::oid)`, key).Scan(&waiting); e != nil {
				t.Fatal("observe private transaction latch")
			}
			if waiting {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("operation did not reach transaction latch")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// The request trigger waits after identity registration and scope generation
	// writes. Sweep must wait for that same transaction's jobs lock.
	waitLock(17481206)
	started++
	go func() { _, e := f.s.SweepProbeCache(ctx, 128); results <- e }()
	waitLock(17481204)
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal("release private admission latch")
	}
	for joined < started {
		select {
		case e := <-results:
			joined++
			if e != nil {
				t.Fatal("serialized admission/sweep failed", e)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("serialized operations did not return")
		}
	}
	l := f.claimProbe(t)
	work, err := f.s.LoadProbeWork(f.ctx, l)
	if err != nil || work.Request == nil {
		t.Fatal("sweep removed admitted work", err)
	}
	var registered, scopes int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM tool_versions WHERE id=$1::uuid),(SELECT count(*) FROM probe_library_quota WHERE library_id=$2::uuid)`, work.Request.Identity.ID, l.Job.LibraryID).Scan(&registered, &scopes); err != nil || registered != 1 || scopes != 1 {
		t.Fatal("sweep removed newly admitted identity or scope")
	}
}

func TestProbeRequestParentFenceAndPersistentCancellation(t *testing.T) {
	for _, mode := range []string{"stale", "expired", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeFixture(t)
			f.request(t, "parent-fence", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
			l := f.claimProbe(t)
			f.scanRequested(t, l)
			want := domain.ErrJobLeaseLost
			switch mode {
			case "stale":
				l.Generation++
			case "expired":
				if _, e := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID); e != nil {
					t.Fatal(e)
				}
			case "cancelled":
				if _, e := f.s.CancelJob(f.ctx, f.a, l.Job.ID); e != nil {
					t.Fatal(e)
				}
				want = context.Canceled
			}
			before := requestSnapshot(t, f)
			work, err := f.s.LoadProbeWork(f.ctx, l)
			requireProbeFault(t, err, want, work)
			phase, err := f.s.BeginRequestedProbePhase(f.ctx, l)
			requireProbeFault(t, err, want, phase)
			requireProbeFault(t, f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable), want, nil)
			if requestSnapshot(t, f) != before {
				t.Fatal("rejected parent changed request state")
			}
		})
	}
}

func TestProbeRequestRejectsMissingOrTerminalWork(t *testing.T) {
	f := newProbeFixture(t)
	f.submit(t, "plain-parent")
	l := f.claim(t, "plain")
	f.scanRequested(t, l)
	before := requestSnapshot(t, f)
	phase, err := f.s.BeginRequestedProbePhase(f.ctx, l)
	requireProbeFault(t, err, domain.ErrNotFound, phase)
	requireProbeFault(t, f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable), domain.ErrNotFound, nil)
	if before != requestSnapshot(t, f) {
		t.Fatal("plain scan was implicitly opted in")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"request_error", "aborted_phase"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeFixture(t)
			f.request(t, mode, domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
			l := f.claimProbe(t)
			f.scanRequested(t, l)
			if mode == "request_error" {
				err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable)
			} else {
				if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				err = f.s.AbortProbePhase(f.ctx, l, domain.ProbePhaseRuntimeUnavailable)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := requestSnapshot(t, f)
			phase, e := f.s.BeginRequestedProbePhase(f.ctx, l)
			requireProbeFault(t, e, domain.ErrConflict, phase)
			if before != requestSnapshot(t, f) {
				t.Fatal("failed work silently restarted")
			}
		})
	}
}

func TestProbeRequestRejectsPhaseIdentityDrift(t *testing.T) {
	f := newProbeFixture(t)
	j := f.request(t, "identity-drift", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
	l := f.claimProbe(t)
	f.scanRequested(t, l)
	if _, err := f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	other := probeTestIdentity()
	other.VendorVersion = "different-verified-runtime"
	ref, err := f.s.RegisterProbeIdentity(f.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	// Inject a stored state inconsistency in the private schema. A worker must
	// reject it rather than treating the current runtime as the admitted one.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_job_state SET tool_version_id=$2::uuid WHERE job_id=$1::uuid`, j.ID, ref.ID); err != nil {
		t.Fatal(err)
	}
	before := requestSnapshot(t, f)
	work, err := f.s.LoadProbeWork(f.ctx, l)
	requireProbeFault(t, err, domain.ErrProbeIdentityMismatch, work)
	phase, err := f.s.BeginRequestedProbePhase(f.ctx, l)
	requireProbeFault(t, err, domain.ErrProbeIdentityMismatch, phase)
	summary, err := f.s.GetProbeJobSummary(f.ctx, f.a, j.ID)
	requireProbeFault(t, err, domain.ErrProbeIdentityMismatch, summary)
	requireProbeFault(t, f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""), domain.ErrProbeIdentityMismatch, nil)
	if before != requestSnapshot(t, f) {
		t.Fatal("mismatched identity changed durable state")
	}
	if err = f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseIdentityMismatch); err != nil {
		t.Fatal("inconsistent phase could not be safely aborted", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRequestFinalFenceRollsBackBeginAndAbort(t *testing.T) {
	for _, operation := range []string{"begin", "abort"} {
		t.Run(operation, func(t *testing.T) {
			f := newProbeFixture(t)
			f.request(t, "final-fence", domain.ProbeIntent{Scope: domain.ProbeScopeIncremental})
			l := f.claimProbe(t)
			f.scanRequested(t, l, "one.mkv")
			trigger := `CREATE FUNCTION slow_request_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER slow_request_change AFTER INSERT ON probe_job_state FOR EACH ROW EXECUTE FUNCTION slow_request_change()`
			if operation == "abort" {
				if _, err := f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				page, candidates := f.page(t, l)
				if _, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0]); err != nil {
					t.Fatal(err)
				}
				trigger = `CREATE FUNCTION slow_request_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER slow_request_change AFTER UPDATE ON probe_requests FOR EACH ROW EXECUTE FUNCTION slow_request_change()`
			}
			if _, err := f.s.Pool.Exec(f.ctx, trigger); err != nil {
				t.Fatal("install private late-fence fixture")
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			before := requestSnapshot(t, f)
			if operation == "begin" {
				phase, err := f.s.BeginRequestedProbePhase(f.ctx, l)
				requireProbeFault(t, err, domain.ErrJobLeaseLost, phase)
			} else {
				requireProbeFault(t, f.s.AbortProbeRequest(f.ctx, l, domain.ProbePhaseRuntimeUnavailable), domain.ErrJobLeaseLost, nil)
			}
			if before != requestSnapshot(t, f) {
				t.Fatal("late lease expiry committed phase, request or released quota")
			}
		})
	}
}

func TestProbeRequestRequiresExistingPolicyAndTarget(t *testing.T) {
	f := probeFixture{jobFixture: newJobFixture(t)}
	identity := probeTestIdentity()
	before := requestSnapshot(t, f)
	j, replay, err := f.s.SubmitScanJob(f.ctx, f.a, f.registration.Library.ID, "missing-policy", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeIncremental}, f.policy, &identity)
	requireProbeFault(t, err, domain.ErrNotFound, j)
	if replay || before != requestSnapshot(t, f) {
		t.Fatal("enqueue implicitly created probe policy")
	}
	if err = f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	before = requestSnapshot(t, f)
	j, replay, err = f.s.SubmitScanJob(f.ctx, f.a, "", "missing-target", domain.JobPriorityManual, domain.ProbeIntent{Scope: domain.ProbeScopeItemRebuild, TargetItemID: "00000000-0000-4000-8000-000000000001"}, f.policy, &identity)
	requireProbeFault(t, err, domain.ErrNotFound, j)
	if replay || before != requestSnapshot(t, f) {
		t.Fatal("unknown target admitted partial work")
	}
}
