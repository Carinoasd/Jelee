package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoReplaceFixtureSummary(t *testing.T, f nfoFixture, body string) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET summary=$1::jsonb,charge_bytes=2048+octet_length($1::jsonb::text)`, body); err != nil {
		t.Fatal("private summary fixture", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET bytes_used=(SELECT sum(charge_bytes) FROM nfo_cache); UPDATE nfo_library_quota q SET bytes_used=(SELECT sum(charge_bytes) FROM nfo_cache c WHERE c.library_id=q.library_id)`); err != nil {
		t.Fatal(err)
	}
}
func TestNFOCacheSummaryVersionUpgradeAndCorruption(t *testing.T) {
	for _, caseName := range []string{"old-digest", "expired", "matching-corrupt"} {
		t.Run(caseName, func(t *testing.T) {
			f := newNFOFixture(t)
			l, _ := f.start(t, "seed", "a.nfo")
			f.parseHead(t, l, nfoValidSummary())
			f.finish(t, l)
			// An old schema row remains within its SQL storage/charge constraints.
			nfoReplaceFixtureSummary(t, f, `{"status":"valid","schemaVersion":999,"obsolete":true}`)
			if caseName == "old-digest" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET identity_digest=decode(repeat('c',64),'hex')`); err != nil {
					t.Fatal(err)
				}
			}
			if caseName == "expired" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			}
			l, page := f.start(t, "new-parser", "a.nfo")
			candidate := nfoCandidate(page.Entries[0])
			rows, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{candidate})
			if caseName == "matching-corrupt" {
				if !errors.Is(err, domain.ErrDatabase) || rows != nil {
					t.Fatal("corrupt current summary reported hit", err)
				}
				return
			}
			if err != nil || rows[0].Kind != domain.NFOLookupMiss {
				t.Fatal("old/expired summary cannot be replaced", err)
			}
			f.parseHead(t, l, nfoValidSummary())
			assertNFOPlanQuota(t, f.jobFixture, 1)
			f.finish(t, l)
			l, page = f.start(t, "read-new", "a.nfo")
			rows, err = f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[0])})
			if err != nil || rows[0].Kind != domain.NFOLookupHit {
				t.Fatal("replacement is unusable", err)
			}
		})
	}
}
func TestNFOSummaryJSONBChargeAndSQLBounds(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "bounded", "a.nfo")
	summary := nfoValidSummary()
	summary.WarningCount = 64
	summary.IssueCount = 64
	for i := 0; i < 64; i++ {
		summary.Issues = append(summary.Issues, domain.NFOIssue{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0})
	}
	raw, err := domain.MarshalNFOSummary(summary)
	if err != nil {
		t.Fatal(err)
	}
	p := f.parseHead(t, l, summary)
	if p.Progress.WarningFiles != 1 || p.Progress.Valid != 1 {
		t.Fatal("warnings changed validity")
	}
	var actual, charge int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT octet_length(summary::text),charge_bytes FROM nfo_cache`).Scan(&actual, &charge); err != nil {
		t.Fatal(err)
	}
	if actual <= int64(len(raw)) || charge != int64(domain.NFORowAllowanceBytes)+actual {
		t.Fatal("JSONB expansion was not charged exactly")
	}
	before := nfoSnapshot(t, f)
	for _, body := range []string{`{}`, `{"status":"invalid"}`, `{"status":"valid","oversize":"` + strings.Repeat("a", domain.NFOSummaryMaxBytes) + `"}`} {
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET summary=$1::jsonb,charge_bytes=2048+octet_length($1::jsonb::text)`, body); err == nil {
			t.Fatal("SQL accepted missing/mismatched status or oversize summary")
		}
		if nfoSnapshot(t, f) != before {
			t.Fatal("failed SQL constraint changed cache")
		}
	}
	assertNFOPlanQuota(t, f.jobFixture, 1)
}
func TestNFOFinalFenceAfterJobUpdateWait(t *testing.T) {
	for _, operation := range []string{"prepare", "load", "begin", "next", "lookup", "commit", "finish", "abort"} {
		t.Run(operation, func(t *testing.T) {
			f := newNFOFixture(t)
			var l domain.JobLease
			var page domain.NFOPage
			if operation == "prepare" || operation == "begin" {
				f.submit(t, "wait")
				l = f.claim(t, "wait-owner")
				if operation == "begin" {
					if _, err := f.s.PrepareNFOPhase(f.ctx, l, f.identity); err != nil {
						t.Fatal(err)
					}
					d := f.directory(t, l)
					if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
						t.Fatal(err)
					}
				}
			} else if operation == "finish" {
				l, page = f.start(t, "wait")
			} else {
				l, page = f.start(t, "wait", "a.nfo")
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '350 milliseconds' WHERE id=$1::uuid`, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_wait_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.5); RETURN NEW; END $$; CREATE TRIGGER nfo_wait_job BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION nfo_wait_job()`); err != nil {
				t.Fatal(err)
			}
			before := nfoSnapshot(t, f)
			var err error
			zero := true
			started := time.Now()
			switch operation {
			case "prepare":
				var p domain.NFOPhase
				p, err = f.s.PrepareNFOPhase(f.ctx, l, f.identity)
				zero = p == (domain.NFOPhase{})
			case "load":
				var p domain.NFOPhase
				p, err = f.s.LoadNFOPhase(f.ctx, l)
				zero = p == (domain.NFOPhase{})
			case "begin":
				var p domain.NFOPhase
				p, err = f.s.BeginNFOPhase(f.ctx, l)
				zero = p == (domain.NFOPhase{})
			case "next":
				var p domain.NFOPage
				p, err = f.s.NextNFOPage(f.ctx, l, 1)
				zero = p.Entries == nil && p.Token == (domain.NFOPageToken{})
			case "lookup":
				var rows []domain.NFOLookup
				rows, err = f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[0])})
				zero = rows == nil
			case "commit":
				summary := nfoValidSummary()
				var p domain.NFOPhase
				p, err = f.s.CommitNFOBatch(f.ctx, l, page.Token, []domain.NFOCompletion{{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}})
				zero = p == (domain.NFOPhase{})
			case "finish":
				var p domain.NFOPhase
				p, err = f.s.FinishNFOPhase(f.ctx, l)
				zero = p == (domain.NFOPhase{})
			case "abort":
				err = f.s.AbortNFOPhase(f.ctx, l, domain.NFOPhaseUnavailable)
			}
			if !errors.Is(err, domain.ErrJobLeaseLost) || !zero || before != nfoSnapshot(t, f) || time.Since(started) < 500*time.Millisecond {
				t.Fatal("late parent fence did not reject/rollback after actually waiting", err)
			}
		})
	}
}
func TestNFOQuotaCapacityEvictsBoundedRowsAndCharges(t *testing.T) {
	for _, axis := range []string{"global-rows", "library-rows", "global-bytes", "library-bytes"} {
		t.Run(axis, func(t *testing.T) {
			f := newNFOFixture(t)
			other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			l, _ := f.start(t, "capacity", "fresh.nfo")
			_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM nfo_cache_quota`, other.Library.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET library_scopes=2`); err != nil {
				t.Fatal(err)
			}
			body, err := domain.MarshalNFOSummary(nfoValidSummary())
			if err != nil {
				t.Fatal(err)
			}
			rows := 1000
			if strings.Contains(axis, "bytes") {
				rows = 8000
			}
			for i, r := range []domain.LibraryRegistration{f.registration, other} {
				count := rows
				if strings.HasPrefix(axis, "global") {
					count = rows / 2
				} else if i == 1 {
					continue
				}
				_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_cache(root_id,relative_path,library_id,size,modified_unix_nano,source_sha256,fingerprint_version,identity_digest,library_generation,root_generation,status,summary,expires_at,charge_bytes) SELECT r.id,'seed-'||n||'.nfo',r.library_id,7,123456789,decode(repeat('a',64),'hex'),'sha256-full-v1',decode(repeat('b',64),'hex'),lib.nfo_generation,r.nfo_generation,'valid',$3::jsonb,clock_timestamp()+interval '1 day',2048+octet_length($3::jsonb::text) FROM library_roots r JOIN libraries lib ON lib.id=r.library_id CROSS JOIN generate_series(1,$2::int) n WHERE r.id=$1::uuid`, r.RootID, count, string(body))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET rows_used=(SELECT count(*) FROM nfo_cache),bytes_used=(SELECT sum(charge_bytes) FROM nfo_cache); UPDATE nfo_library_quota q SET rows_used=(SELECT count(*) FROM nfo_cache c WHERE c.library_id=q.library_id),bytes_used=COALESCE((SELECT sum(charge_bytes) FROM nfo_cache c WHERE c.library_id=q.library_id),0)`); err != nil {
				t.Fatal(err)
			}
			switch axis {
			case "global-rows":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET global_row_limit=1000,library_row_limit=1000`)
			case "library-rows":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_library_quota SET row_limit=1000 WHERE library_id=$1::uuid`, f.registration.Library.ID)
			case "global-bytes":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET global_byte_limit=bytes_used,library_byte_limit=16777216`)
			case "library-bytes":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_library_quota SET byte_limit=bytes_used WHERE library_id=$1::uuid`, f.registration.Library.ID)
			}
			if err != nil {
				t.Fatal("configure actual quota boundary", err)
			}
			f.parseHead(t, l, nfoValidSummary())
			assertNFOPlanQuota(t, f.jobFixture, int64(rows-domain.NFOSweepMax+1))
			f.finish(t, l)
		})
	}
}
func TestNFOScopesRetainedRecoveryAndBoundedCleanup(t *testing.T) {
	f := newNFOFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET library_limit=1`); err != nil {
		t.Fatal(err)
	}
	l, _ := f.start(t, "retain")
	if err := f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SweepNFOCache(f.ctx, 128); err != nil {
		t.Fatal(err)
	}
	var scopes int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes FROM nfo_cache_quota`).Scan(&scopes); err != nil || scopes != 1 {
		t.Fatal("queued checkpoint lost scope")
	}
	other, err := f.s.RegisterLibrary(f.ctx, "blocked", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, other.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, other.Library.ID, "other-enable", current.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	next := f.claim(t, "resume")
	f.finish(t, next)
	if _, err = f.s.SweepNFOCache(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes FROM nfo_cache_quota`).Scan(&scopes); err != nil || scopes != 0 {
		t.Fatal("terminal history pinned empty scope")
	}
	if _, _, err = f.s.SubmitScanWithStages(f.ctx, f.a, other.Library.ID, "other", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &f.identity); err != nil {
		t.Fatal(err)
	}
	next = f.claim(t, "other")
	if _, err = f.s.PrepareNFOPhase(f.ctx, next, f.identity); err != nil {
		t.Fatal("reclaimed scope cannot be reused", err)
	}
}
func TestNFOCacheClosedAndCancelledReturnZero(t *testing.T) {
	f := newNFOFixture(t)
	l, page := f.start(t, "closed", "a.nfo")
	calls := map[string]func(context.Context) error{
		"prepare": func(ctx context.Context) error {
			p, e := f.s.PrepareNFOPhase(ctx, l, f.identity)
			if p != (domain.NFOPhase{}) {
				t.Fatal("partial prepare")
			}
			return e
		},
		"load": func(ctx context.Context) error {
			p, e := f.s.LoadNFOPhase(ctx, l)
			if p != (domain.NFOPhase{}) {
				t.Fatal("partial load")
			}
			return e
		},
		"begin": func(ctx context.Context) error {
			p, e := f.s.BeginNFOPhase(ctx, l)
			if p != (domain.NFOPhase{}) {
				t.Fatal("partial begin")
			}
			return e
		},
		"next": func(ctx context.Context) error {
			p, e := f.s.NextNFOPage(ctx, l, 1)
			if p.Entries != nil {
				t.Fatal("partial page")
			}
			return e
		},
		"lookup": func(ctx context.Context) error {
			p, e := f.s.LookupNFOBatch(ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[0])})
			if p != nil {
				t.Fatal("partial lookup")
			}
			return e
		},
		"commit": func(ctx context.Context) error {
			summary := nfoValidSummary()
			p, e := f.s.CommitNFOBatch(ctx, l, page.Token, []domain.NFOCompletion{{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}})
			if p != (domain.NFOPhase{}) {
				t.Fatal("partial commit")
			}
			return e
		},
		"finish": func(ctx context.Context) error {
			p, e := f.s.FinishNFOPhase(ctx, l)
			if p != (domain.NFOPhase{}) {
				t.Fatal("partial finish")
			}
			return e
		},
		"abort": func(ctx context.Context) error { return f.s.AbortNFOPhase(ctx, l, domain.NFOPhaseUnavailable) },
		"sweep": func(ctx context.Context) error {
			p, e := f.s.SweepNFOCache(ctx, 1)
			if p != (domain.NFOSweepResult{}) {
				t.Fatal("partial sweep")
			}
			return e
		},
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	for name, call := range calls {
		t.Run("cancelled-"+name, func(t *testing.T) {
			if !errors.Is(call(cancelled), context.Canceled) {
				t.Fatal("cancel contract")
			}
		})
	}
	f.s.Pool.Close()
	for name, call := range calls {
		t.Run("closed-"+name, func(t *testing.T) {
			if !errors.Is(call(f.ctx), domain.ErrDatabase) {
				t.Fatal("closed pool contract")
			}
		})
	}
}

// json encoding here verifies that no read boundary ever leaks stored private
// text while still using the actual PostgreSQL jsonb representation.
func TestNFOCacheMatchingSummaryHasNoArbitraryData(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "seed", "a.nfo")
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	body, _ := json.Marshal(map[string]any{"status": "valid", "private": "secret-title"})
	nfoReplaceFixtureSummary(t, f, string(body))
	l, page := f.start(t, "read", "a.nfo")
	rows, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[0])})
	if !errors.Is(err, domain.ErrDatabase) || rows != nil || strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatal("private malformed payload escaped")
	}
}

func TestNFOAdmissionRequiresEstablishedPolicyAndAvailableScope(t *testing.T) {
	f := newJobFixture(t)
	current, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, current.LibraryID, "enable", current.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	identity := domain.DefaultNFOIdentity()
	if j, replay, e := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "missing-policy", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &identity); !errors.Is(e, domain.ErrNotFound) || j != (domain.Job{}) || replay {
		t.Fatal("enqueue silently established quota policy", e)
	}
	policy := domain.DefaultNFOCachePolicy()
	policy.MaxLibraries = 1
	if err = f.s.EnsureNFOCachePolicy(f.ctx, policy); err != nil {
		t.Fatal(err)
	}
	if err = f.s.EnsureNFOCachePolicy(f.ctx, policy); err != nil {
		t.Fatal("matching policy replay", err)
	}
	changed := policy
	changed.MaxLibraries++
	if err = f.s.EnsureNFOCachePolicy(f.ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("policy silently enlarged capacity")
	}
	if _, _, err = f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, "with-policy", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &identity); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.ClaimJobWithCapabilities(f.ctx, "nfo-policy", false, time.Minute, domain.ScanCapabilities{NFO: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, err = f.s.GetNFOLibraryPolicy(f.ctx, f.a, other.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, current.LibraryID, "other-enable", current.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, nfoFixture{f, domain.DefaultNFOIdentity()})
	if j, replay, e := f.s.SubmitScanWithStages(f.ctx, f.a, other.Library.ID, "other-job", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &identity); !errors.Is(e, domain.ErrNFOCacheCapacity) || j != (domain.Job{}) || replay {
		t.Fatal("scope capacity admitted another library", e)
	}
	if nfoSnapshot(t, nfoFixture{f, domain.DefaultNFOIdentity()}) != before {
		t.Fatal("failed scope admission retained phase/counter")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SweepNFOCache(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SubmitScanWithStages(f.ctx, f.a, other.Library.ID, "other-job", domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &identity); err != nil {
		t.Fatal("reclaimed terminal scope unavailable", err)
	}
}

func TestNFODeferredCommitFailureReturnsZeroAndRollsBack(t *testing.T) {
	f := newNFOFixture(t)
	l, page := f.start(t, "deferred", "a.nfo")
	summary := nfoValidSummary()
	batch := []domain.NFOCompletion{{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_deferred_deny() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test commit rejected' USING ERRCODE='42501'; END $$; CREATE CONSTRAINT TRIGGER nfo_deferred_deny AFTER UPDATE ON nfo_job_state DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nfo_deferred_deny()`); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, f)
	if p, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, batch); !errors.Is(err, domain.ErrDatabase) || p != (domain.NFOPhase{}) || before != nfoSnapshot(t, f) {
		t.Fatal("commit rejection leaked provisional result", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_deferred_deny ON nfo_job_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, batch); err != nil {
		t.Fatal("retry after deferred rejection", err)
	}
}

func TestNFOSweepFailureRollsBackAndRetries(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "cleanup", "a.nfo")
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET expires_at=clock_timestamp()-interval '1 second'; CREATE FUNCTION nfo_delete_deny() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test cleanup rejected' USING ERRCODE='42501'; END $$; CREATE TRIGGER nfo_delete_deny BEFORE DELETE ON nfo_cache FOR EACH ROW EXECUTE FUNCTION nfo_delete_deny()`); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, f)
	if result, err := f.s.SweepNFOCache(f.ctx, 128); !errors.Is(err, domain.ErrDatabase) || result != (domain.NFOSweepResult{}) || before != nfoSnapshot(t, f) {
		t.Fatal("failed deletion changed quotas/cache", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_delete_deny ON nfo_cache`); err != nil {
		t.Fatal(err)
	}
	if result, err := f.s.SweepNFOCache(f.ctx, 128); err != nil || result.Deleted != 1 || result.FreedBytes <= 0 {
		t.Fatal("cleanup retry", err)
	}
	assertNFOPlanQuota(t, f.jobFixture, 0)
}
