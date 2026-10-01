package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFORequestedIntentIsAtomicFrozenAndReplayable(t *testing.T) {
	f := newNFOFixture(t)
	// Existing callers remain off even when the library policy is read-only.
	off := f.jobFixture.submit(t, "old-wrapper")
	l := f.jobFixture.claim(t, "legacy-capability")
	work, err := f.s.LoadNFOWork(f.ctx, l)
	if err != nil || work.Request == nil || work.Request.Requested || work.Phase == nil || !frozenNFOOff(*work.Phase) {
		t.Fatal("old wrapper silently opted in", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	summary, err := f.s.GetNFOJobSummary(f.ctx, f.a, off.ID)
	if err != nil || summary.Phase != domain.NFOSummaryDisabled {
		t.Fatal("off history", err)
	}
	j := f.submit(t, "explicit")
	current, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, j.LibraryID, "disable", current.Generation, domain.NFOModeOff); err != nil {
		t.Fatal(err)
	}
	replay, wasReplay, err := f.s.SubmitScanWithStages(f.ctx, f.a, j.LibraryID, "explicit", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, nil)
	if err != nil || !wasReplay || replay.ID != j.ID {
		t.Fatal("retained identical request needs today's capability/policy", err)
	}
	if _, _, err = f.s.SubmitJob(f.ctx, f.a, j.LibraryID, "explicit", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed false body replayed true job", err)
	}
	l = f.claim(t, "explicit-worker")
	work, err = f.s.LoadNFOWork(f.ctx, l)
	if err != nil || !work.Request.Requested || work.Phase.Mode != domain.NFOModeReadOnly {
		t.Fatal("queued mode repinned", err)
	}
	if _, err = f.s.BeginRequestedNFOPhase(f.ctx, l); !errors.Is(err, domain.ErrNFOInvalidated) {
		t.Fatal("disabled scope ran", err)
	}
	if err = f.s.AbortNFORequest(f.ctx, l, domain.NFOPhaseInvalidated); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.RetryScanWithStages(f.ctx, f.a, j.ID, "retry", f.policy, nil, &f.identity); !errors.Is(err, domain.ErrNFODisabled) {
		t.Fatal("new retry ignored policy", err)
	}
	current, err = f.s.GetNFOLibraryPolicy(f.ctx, f.a, j.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, j.LibraryID, "reenable", current.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	next, replayed, err := f.s.RetryScanWithStages(f.ctx, f.a, j.ID, "retry", f.policy, nil, &f.identity)
	if err != nil || replayed || next.ID == j.ID {
		t.Fatal("explicit retry failed", err)
	}
	if got, replay, err := f.s.RetryScanWithStages(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil); err != nil || !replay || got.ID != next.ID {
		t.Fatal("retry replay repins", err)
	}
}
func TestNFOClaimFiltersBothCapabilities(t *testing.T) {
	f := newNFOFixture(t)
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	probe := probeTestIdentity()
	cases := []struct {
		probe, nfo bool
		id         string
	}{{false, false, ""}, {true, false, ""}, {false, true, ""}, {true, true, ""}}
	for i := range cases {
		r := f.registration
		if i > 0 {
			var err error
			r, err = f.s.RegisterLibrary(f.ctx, fmt.Sprintf("cap-%d", i), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			p, e := f.s.GetNFOLibraryPolicy(f.ctx, f.a, r.Library.ID)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = f.s.SetNFOLibraryPolicy(f.ctx, f.a, r.Library.ID, fmt.Sprintf("mode-%d", i), p.Generation, domain.NFOModeReadOnly); e != nil {
				t.Fatal(e)
			}
		}
		intent := domain.ScanIntent{NFO: cases[i].nfo}
		if cases[i].probe {
			intent.Probe.Scope = domain.ProbeScopeIncremental
		}
		j, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, r.Library.ID, fmt.Sprintf("cap-%d", i), domain.JobPriorityManual, intent, f.policy, &probe, &f.identity)
		if err != nil {
			t.Fatal(err)
		}
		cases[i].id = j.ID
	}
	for _, tc := range cases {
		cap := domain.ScanCapabilities{Probe: tc.probe, NFO: tc.nfo}
		l, err := f.s.ClaimJobWithCapabilities(f.ctx, "cap-worker", false, time.Minute, cap)
		if err != nil || l.Job.ID != tc.id {
			t.Fatal("claim capability mismatch", err)
		}
		if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNFOEnqueueRollbackAndUnavailableReader(t *testing.T) {
	f := newNFOFixture(t)
	if j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "no-reader", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, nil); !errors.Is(err, domain.ErrNFOReaderUnavailable) || j != (domain.Job{}) || replay {
		t.Fatal("missing reader admitted job", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_request_test_deny() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test request rejected' USING ERRCODE='42501'; END $$; CREATE TRIGGER nfo_request_test_deny BEFORE INSERT ON nfo_job_requests FOR EACH ROW EXECUTE FUNCTION nfo_request_test_deny()`); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, f)
	if j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "reject", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &f.identity); !errors.Is(err, domain.ErrDatabase) || j != (domain.Job{}) || replay || before != nfoSnapshot(t, f) {
		t.Fatal("request rejection retained job/scope/phase", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_request_test_deny ON nfo_job_requests`); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "reject")
}
func TestNFOCurrentObservationIdentityAndIssuePagination(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "observe", "a.nfo")
	summary := nfoValidSummary()
	summary.WarningCount = 70
	summary.IssueCount = 70
	summary.IssuesTruncated = true
	for range 64 {
		summary.Issues = append(summary.Issues, domain.NFOIssue{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0})
	}
	f.parseHead(t, l, summary)
	f.finish(t, l)
	page, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("current observations", err)
	}
	id := page.Items[0].ID
	first, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, id, 0, 32, f.identity)
	if err != nil || len(first.Issues) != 32 || first.NextOffset == nil || *first.NextOffset != 32 || first.IssueCount != 70 || !first.IssuesTruncated {
		t.Fatal("first issue prefix", err)
	}
	last, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, id, 32, 32, f.identity)
	if err != nil || len(last.Issues) != 32 || last.NextOffset != nil {
		t.Fatal("last retained issue prefix", err)
	}
	empty, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, id, 64, 32, f.identity)
	if err != nil || empty.Issues == nil || len(empty.Issues) != 0 {
		t.Fatal("end offset", err)
	}
	mismatch := f.identity
	mismatch.MaxSourceBytes++
	if list, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 20, mismatch); err != nil || len(list.Items) != 0 {
		t.Fatal("different identity exposed old observations", err)
	}
	l, head := f.start(t, "hit", "a.nfo")
	if _, err = f.s.CommitNFOBatch(f.ctx, l, head.Token, []domain.NFOCompletion{{Candidate: nfoCandidate(head.Entries[0]), Kind: domain.NFOCompletionHit}}); err != nil {
		t.Fatal(err)
	}
	f.finish(t, l)
	page, err = f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity)
	if err != nil || page.Items[0].ID != id {
		t.Fatal("hit replaced observation identity", err)
	}
	l, _ = f.start(t, "fresh", "a.nfo")
	f.parseHead(t, l, nfoInvalidSummary())
	f.finish(t, l)
	if result, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, id, 0, 32, f.identity); !errors.Is(err, domain.ErrNotFound) || result.Issues != nil {
		t.Fatal("old observation follows replacement", err)
	}
	page, err = f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity)
	if err != nil || page.Items[0].ID == id || page.Items[0].FailureCode != domain.NFOFailureInvalidXML {
		t.Fatal("new observation projection", err)
	}
	current, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, l.Job.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, l.Job.LibraryID, "off", current.Generation, domain.NFOModeOff); err != nil {
		t.Fatal(err)
	}
	if page, err = f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 20, f.identity); err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatal("off policy reveals observations", err)
	}
	historic, err := f.s.GetNFOJobSummary(f.ctx, f.a, l.Job.ID)
	if err != nil || historic.Parsed != 1 || historic.Invalid != 1 {
		t.Fatal("history was erased by policy", err)
	}
}
func TestNFOCurrentQueriesRejectRevokedActorAndReturnZero(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "auth", "a.nfo")
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	page, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.GetNFOJobSummary(f.ctx, f.a, l.Job.ID); !errors.Is(err, domain.ErrUnauthenticated) || got != (domain.NFOJobSummary{}) {
		t.Fatal("revoked history")
	}
	if got, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity); !errors.Is(err, domain.ErrUnauthenticated) || got.Items != nil {
		t.Fatal("revoked observations")
	}
	if got, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, page.Items[0].ID, 0, 32, f.identity); !errors.Is(err, domain.ErrUnauthenticated) || got.Issues != nil {
		t.Fatal("revoked issues")
	}
	if got, err := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID); !errors.Is(err, domain.ErrUnauthenticated) || got != (domain.ImageJobSummary{}) {
		t.Fatal("revoked image summary")
	}
}
func TestNFOStageInvalidInputsWithoutDB(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	id := "11111111-1111-1111-1111-111111111111"
	if _, _, err := s.SubmitScanWithStages(ctx, domain.Actor{}, id, "key", domain.JobPriorityManual, domain.ScanIntent{Probe: domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}, NFO: true}, jobTestPolicy(), nil, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("NFO rebuild combination accepted")
	}
	if _, err := s.ListNFOObservations(ctx, domain.Actor{}, id, "", 51, domain.DefaultNFOIdentity()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded page")
	}
	if _, err := s.GetNFOObservationIssues(ctx, domain.Actor{}, id, id, 65, 32, domain.DefaultNFOIdentity()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unbounded issues")
	}
}

func TestNFORequestedPhaseMustFinishBeforeProbeAndPublication(t *testing.T) {
	f := newNFOFixture(t)
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	probe := probeTestIdentity()
	j, _, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "stages", domain.JobPriorityManual, domain.ScanIntent{Probe: domain.ProbeIntent{Scope: domain.ProbeScopeIncremental}, NFO: true}, f.policy, &probe, &f.identity)
	if err != nil {
		t.Fatal(err)
	}
	l, err := f.s.ClaimJobWithCapabilities(f.ctx, "both", false, time.Minute, domain.ScanCapabilities{Probe: true, NFO: true})
	if err != nil || l.Job.ID != j.ID {
		t.Fatal(err)
	}
	d := f.directory(t, l)
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("probe bypassed waiting NFO", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("parent bypassed waiting NFO", err)
	}
	if _, err = f.s.BeginRequestedNFOPhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("probe bypassed running NFO", err)
	}
	if _, err = f.s.FinishNFOPhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.BeginRequestedProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.FinishProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
}

func TestNFOCurrentObservationsScopeExpiryAndCorruption(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "three", "a.nfo", "b.nfo", "c.nfo")
	for range 3 {
		f.parseHead(t, l, nfoValidSummary())
	}
	f.finish(t, l)
	first, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 2, f.identity)
	if err != nil || len(first.Items) != 2 || first.NextCursor != first.Items[1].ID {
		t.Fatal("bounded keyset first page", err)
	}
	last, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, first.NextCursor, 2, f.identity)
	if err != nil || len(last.Items) != 1 || last.NextCursor != "" || last.Items[0].ID <= first.NextCursor {
		t.Fatal("bounded keyset last page", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other-observations", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.GetNFOObservationIssues(f.ctx, f.a, other.Library.ID, first.Items[0].ID, 0, 32, f.identity); !errors.Is(err, domain.ErrNotFound) || result.Issues != nil {
		t.Fatal("cross-library observation exposed", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET expires_at=clock_timestamp()-interval '1 second' WHERE observation_id=$1::uuid`, first.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, first.Items[0].ID, 0, 32, f.identity); !errors.Is(err, domain.ErrNotFound) || result.Issues != nil {
		t.Fatal("expired issues exposed", err)
	}
	page, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 50, f.identity)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("expired observation listed", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET summary=summary-'encoding',charge_bytes=2048+octet_length((summary-'encoding')::text) WHERE observation_id=$1::uuid`, page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 50, f.identity); !errors.Is(err, domain.ErrDatabase) || result.Items != nil || result.NextCursor != "" {
		t.Fatal("corrupt current row leaked partial list", err)
	}
	if result, err := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, page.Items[0].ID, 0, 32, f.identity); !errors.Is(err, domain.ErrDatabase) || result.Issues != nil {
		t.Fatal("corrupt issue row returned", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-new' WHERE library_id=$1::uuid`, l.Job.LibraryID); err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 50, f.identity); err != nil || result.Items == nil || len(result.Items) != 0 {
		t.Fatal("old root scope exposed observations", err)
	}
}

func TestNFOQueriesRecheckLiveActorAfterLockWait(t *testing.T) {
	for _, name := range []string{"history", "observations", "issues", "images"} {
		t.Run(name, func(t *testing.T) {
			f := newNFOFixture(t)
			l, _ := f.start(t, "auth-expiry", "a.nfo")
			f.parseHead(t, l, nfoValidSummary())
			f.finish(t, l)
			page, err := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 1, f.identity)
			if err != nil {
				t.Fatal(err)
			}
			holder, err := f.s.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(f.ctx)
			if err = lockJobs(f.ctx, holder); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '600 milliseconds' WHERE id=$1::uuid`, f.a.SessionID); err != nil {
				t.Fatal(err)
			}
			type answer struct {
				err  error
				zero bool
			}
			done := make(chan answer, 1)
			go func() {
				switch name {
				case "history":
					got, e := f.s.GetNFOJobSummary(f.ctx, f.a, l.Job.ID)
					done <- answer{e, got == (domain.NFOJobSummary{})}
				case "images":
					got, e := f.s.GetImageJobSummary(f.ctx, f.a, l.Job.ID)
					done <- answer{e, got == (domain.ImageJobSummary{})}
				case "observations":
					got, e := f.s.ListNFOObservations(f.ctx, f.a, l.Job.LibraryID, "", 50, f.identity)
					done <- answer{e, got.Items == nil && got.NextCursor == ""}
				case "issues":
					got, e := f.s.GetNFOObservationIssues(f.ctx, f.a, l.Job.LibraryID, page.Items[0].ID, 0, 32, f.identity)
					done <- answer{e, got.Issues == nil && got.ObservationID == ""}
				}
			}()
			waiting := false
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=17481204)`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				holder.Rollback(f.ctx)
				<-done
				t.Fatal("query did not reach lock after initial authorization")
			}
			if _, err = holder.Exec(f.ctx, `SELECT pg_sleep(0.7)`); err != nil {
				t.Fatal(err)
			}
			if err = holder.Rollback(f.ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case a := <-done:
				if !errors.Is(a.err, domain.ErrUnauthenticated) || !a.zero {
					t.Fatal("expired administrator received query output", a.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("query did not join")
			}
		})
	}
}
