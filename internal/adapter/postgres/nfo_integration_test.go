package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoFixture struct {
	jobFixture
	identity domain.NFOIdentity
}

func (f nfoFixture) submit(t *testing.T, key string) domain.Job {
	t.Helper()
	j, replay, err := f.s.SubmitScanWithStages(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, domain.ScanIntent{NFO: true}, f.policy, nil, &f.identity)
	if err != nil || replay {
		t.Fatal("NFO opt-in enqueue", err)
	}
	return j
}
func (f nfoFixture) claim(t *testing.T, owner string) domain.JobLease {
	t.Helper()
	l, err := f.s.ClaimJobWithCapabilities(f.ctx, owner, false, time.Minute, domain.ScanCapabilities{NFO: true})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func newNFOFixture(t *testing.T) nfoFixture {
	t.Helper()
	f := nfoFixture{newJobFixture(t), domain.DefaultNFOIdentity()}
	if err := f.s.EnsureNFOCachePolicy(f.ctx, domain.DefaultNFOCachePolicy()); err != nil {
		t.Fatal(err)
	}
	policy, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, policy.LibraryID, "enable-nfo", policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f nfoFixture) start(t *testing.T, key string, names ...string) (domain.JobLease, domain.NFOPage) {
	t.Helper()
	f.submit(t, key)
	l := f.claim(t, "nfo-worker")
	if p, err := f.s.PrepareNFOPhase(f.ctx, l, f.identity); err != nil || p.State != domain.NFOPhaseWaiting {
		t.Fatal("prepare", err)
	}
	d := f.directory(t, l)
	entries := make([]domain.InventoryEntry, 0, len(names))
	for _, name := range names {
		e := scanEntry(d, name, 7)
		e.Kind = "nfo"
		entries = append(entries, e)
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: entries, Done: true}); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.BeginNFOPhase(f.ctx, l); err != nil || p.State != domain.NFOPhaseRunning {
		t.Fatal("begin", err)
	}
	page, err := f.s.NextNFOPage(f.ctx, l, domain.NFOPageMax)
	if len(names) == 0 {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	return l, page
}
func nfoCandidate(e domain.NFOEntry) domain.NFOCandidate {
	return domain.NFOCandidate{InventoryID: e.Inventory.ID, Stamp: domain.NFOStamp{Size: e.Inventory.Size, ModifiedUnixNano: e.Inventory.ModifiedUnixNano, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}}
}
func nfoValidSummary() domain.NFOValidationSummary {
	return domain.NFOValidationSummary{SchemaVersion: 1, Status: domain.NFOStatusValid, Encoding: "UTF-8", Root: "movie", Entries: 1, Issues: []domain.NFOIssue{}}
}
func nfoInvalidSummary() domain.NFOValidationSummary {
	return domain.NFOValidationSummary{SchemaVersion: 1, Status: domain.NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: domain.NFOFailureInvalidXML, Issues: []domain.NFOIssue{}}
}
func (f nfoFixture) parseHead(t *testing.T, l domain.JobLease, summary domain.NFOValidationSummary) domain.NFOPhase {
	t.Helper()
	page, err := f.s.NextNFOPage(f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, []domain.NFOCompletion{{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}})
	if err != nil {
		t.Fatal("parse commit", err)
	}
	return p
}
func (f nfoFixture) finish(t *testing.T, l domain.JobLease) {
	t.Helper()
	if _, err := f.s.FinishNFOPhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
}
func nfoSnapshot(t *testing.T, f nfoFixture) string {
	t.Helper()
	var value string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object('cache',(SELECT jsonb_agg(to_jsonb(c) ORDER BY root_id,relative_path) FROM nfo_cache c),'quota',(SELECT to_jsonb(q) FROM nfo_cache_quota q),'libraries',(SELECT jsonb_agg(to_jsonb(q) ORDER BY library_id) FROM nfo_library_quota q),'phase',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM nfo_job_state p),'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),'requests',(SELECT jsonb_agg(to_jsonb(r) ORDER BY job_id) FROM nfo_job_requests r),'images',(SELECT jsonb_agg(to_jsonb(i) ORDER BY job_id) FROM image_job_state i),'baseline',(SELECT jsonb_agg(to_jsonb(b) ORDER BY library_id,root_id,path) FROM library_inventory_baseline b),'directories',(SELECT jsonb_agg(to_jsonb(d) ORDER BY job_id,root_id,path) FROM job_directories d),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_logs a))::text`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestNFOCacheRoundTripHitsNegativeAndHashIdentity(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "first", "good.nfo", "bad.nfo")
	for _, summary := range []domain.NFOValidationSummary{nfoValidSummary(), nfoInvalidSummary()} {
		f.parseHead(t, l, summary)
	}
	f.finish(t, l)
	assertNFOPlanQuota(t, f.jobFixture, 2)
	l, page := f.start(t, "again", "good.nfo", "bad.nfo")
	candidates := make([]domain.NFOCandidate, len(page.Entries))
	for i, e := range page.Entries {
		candidates[i] = nfoCandidate(e)
	}
	hits, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, candidates)
	if err != nil {
		t.Fatal(err)
	}
	batch := make([]domain.NFOCompletion, len(hits))
	count := map[string]int{}
	for i, hit := range hits {
		count[hit.Kind]++
		batch[i] = domain.NFOCompletion{Candidate: candidates[i], Kind: hit.Kind}
	}
	if count[domain.NFOLookupHit] != 1 || count[domain.NFOLookupNegativeHit] != 1 {
		t.Fatal("ready positive/negative cache was not reused")
	}
	phase, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, batch)
	if err != nil || phase.Progress.Hits != 1 || phase.Progress.NegativeHits != 1 || phase.Progress.Valid != 1 || phase.Progress.Invalid != 1 {
		t.Fatal("hit counters", err)
	}
	before := nfoSnapshot(t, f)
	zero, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, batch)
	if !errors.Is(err, domain.ErrConflict) || zero != (domain.NFOPhase{}) || before != nfoSnapshot(t, f) {
		t.Fatal("stale cursor repeated effects", err)
	}
	f.finish(t, l)
	l, page = f.start(t, "hash-changed", "good.nfo", "bad.nfo")
	changed := nfoCandidate(page.Entries[0])
	changed.Stamp.SHA256 = strings.Repeat("b", 64)
	look, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{changed})
	if err != nil || look[0].Kind != domain.NFOLookupMiss {
		t.Fatal("full hash omitted from key", err)
	}
	if err = f.s.AbortNFOPhase(f.ctx, l, domain.NFOPhaseUnavailable); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
}
func TestNFOPhaseFreezeInventoryAndFinishBarriers(t *testing.T) {
	t.Run("off cannot silently opt in", func(t *testing.T) {
		f := newJobFixture(t)
		f.submit(t, "off")
		l := f.claim(t, "off-owner")
		p, err := f.s.PrepareNFOPhase(f.ctx, l, domain.DefaultNFOIdentity())
		if err != nil || !frozenNFOOff(p) {
			t.Fatal(err)
		}
		current, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, current.LibraryID, "later-enable", current.Generation, domain.NFOModeReadOnly); err != nil {
			t.Fatal(err)
		}
		again, err := f.s.PrepareNFOPhase(f.ctx, l, domain.DefaultNFOIdentity())
		if err != nil || again != p {
			t.Fatal("off phase repinned", err)
		}
		d := f.directory(t, l)
		if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
			t.Fatal("frozen off blocks scan", err)
		}
		if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
			t.Fatal("frozen off blocks ordinary success", err)
		}
	})
	t.Run("waiting running aborted done", func(t *testing.T) {
		f := newNFOFixture(t)
		f.submit(t, "barrier")
		l := f.claim(t, "barrier-owner")
		p, err := f.s.PrepareNFOPhase(f.ctx, l, f.identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.BeginNFOPhase(f.ctx, l); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("begin skipped inventory")
		}
		if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("parent skipped waiting phase")
		}
		changed := f.identity
		changed.MaxSourceBytes++
		if _, err = f.s.PrepareNFOPhase(f.ctx, l, changed); !errors.Is(err, domain.ErrNFOIdentityMismatch) {
			t.Fatal("identity repinned")
		}
		d := f.directory(t, l)
		if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.BeginNFOPhase(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("running phase accepts inventory")
		}
		if err = f.s.AbortNFOPhase(f.ctx, l, domain.NFOPhaseDisabled); err != nil {
			t.Fatal(err)
		}
		if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("read-only disabled abort gained off exception")
		}
		if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("aborted read-only parent succeeded")
		}
		loaded, err := f.s.LoadNFOPhase(f.ctx, l)
		if err != nil || loaded.Mode != p.Mode || loaded.Identity != p.Identity || loaded.LibraryGeneration != p.LibraryGeneration {
			t.Fatal("abort changed frozen identity", err)
		}
		if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
			t.Fatal(err)
		}
		l, _ = f.start(t, "done-barrier")
		if _, err = f.s.FinishNFOPhase(f.ctx, l); err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.NextScanDirectory(f.ctx, l); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("done phase accepts inventory")
		}
		f.finish(t, l)
	})
	t.Run("prepare too late", func(t *testing.T) {
		f := newNFOFixture(t)
		f.submit(t, "late")
		l := f.claim(t, "late-owner")
		d := f.directory(t, l)
		if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
			t.Fatal(err)
		}
		if p, err := f.s.PrepareNFOPhase(f.ctx, l, f.identity); err != nil || p.State != domain.NFOPhaseWaiting {
			t.Fatal("C Prepare must replay enqueue snapshot after inventory", err)
		}
	})
}
func TestNFOPhaseRootInvalidationRecoveryAndOldIdentity(t *testing.T) {
	for _, change := range []string{"root", "policy", "old-identity"} {
		t.Run(change, func(t *testing.T) {
			f := newNFOFixture(t)
			l, page := f.start(t, "invalidate", "one.nfo")
			original, err := f.s.LoadNFOPhase(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			want := domain.ErrNFOInvalidated
			code := domain.NFOPhaseInvalidated
			switch change {
			case "root":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'/other' WHERE id=$1::uuid`, f.registration.RootID)
			case "policy":
				current, e := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
				if e != nil {
					t.Fatal(e)
				}
				_, _, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, current.LibraryID, "disable", current.Generation, domain.NFOModeOff)
			case "old-identity":
				// Simulate a phase retained across a parser upgrade, never a public API.
				_, err = f.s.Pool.Exec(f.ctx, `ALTER TABLE nfo_job_state DISABLE TRIGGER nfo_phase_identity_immutable; UPDATE nfo_job_state SET parser_version='nfo-validation-previous'; ALTER TABLE nfo_job_state ENABLE TRIGGER nfo_phase_identity_immutable`)
				want, code = domain.ErrNFOIdentityMismatch, domain.NFOPhaseIdentityMismatch
			}
			if err != nil {
				t.Fatal(err)
			}
			before := nfoSnapshot(t, f)
			if result, err := f.s.BeginNFOPhase(f.ctx, l); !errors.Is(err, want) || result != (domain.NFOPhase{}) {
				t.Fatal("invalidated Begin", err)
			}
			if result, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[0])}); !errors.Is(err, want) || result != nil {
				t.Fatal("invalidated lookup", err)
			}
			if before != nfoSnapshot(t, f) {
				t.Fatal("invalidated operation wrote state")
			}
			loaded, err := f.s.LoadNFOPhase(f.ctx, l)
			if err != nil || loaded.LibraryGeneration != original.LibraryGeneration {
				t.Fatal("load cannot recover old phase", err)
			}
			if err = f.s.AbortNFOPhase(f.ctx, l, code); err != nil {
				t.Fatal("fenced cleanup could not abort old identity/scope", err)
			}
			if err = f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestNFOCacheChangedUnavailableRejectedDoNotPoison(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "no-negative", "a.nfo", "b.nfo", "c.nfo")
	for _, kind := range []string{domain.NFOCompletionChanged, domain.NFOCompletionUnavailable, domain.NFOCompletionRejected} {
		page, err := f.s.NextNFOPage(f.ctx, l, 1)
		if err != nil {
			t.Fatal(err)
		}
		c := domain.NFOCompletion{Candidate: domain.NFOCandidate{InventoryID: page.Entries[0].Inventory.ID}, Kind: kind}
		if kind == domain.NFOCompletionRejected {
			c.FailureCode = domain.NFOFailureTooLarge
		}
		if _, err = f.s.CommitNFOBatch(f.ctx, l, page.Token, []domain.NFOCompletion{c}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.s.LoadNFOPhase(f.ctx, l)
	if err != nil || p.Progress.Processed != 3 || p.Progress.Changed != 1 || p.Progress.Unavailable != 1 || p.Progress.Rejected != 1 || p.Progress.Invalid != 0 {
		t.Fatal("noncache outcomes", err)
	}
	assertNFOPlanQuota(t, f.jobFixture, 0)
	f.finish(t, l)
}
func TestNFOCachePrefixAndCancelledFencing(t *testing.T) {
	f := newNFOFixture(t)
	l, page := f.start(t, "prefix", "a.nfo", "b.nfo")
	before := nfoSnapshot(t, f)
	if rows, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{nfoCandidate(page.Entries[1])}); !errors.Is(err, domain.ErrConflict) || rows != nil {
		t.Fatal("skipped prefix", err)
	}
	wrong := nfoCandidate(page.Entries[0])
	wrong.Stamp.Size++
	if rows, err := f.s.LookupNFOBatch(f.ctx, l, page.Token, []domain.NFOCandidate{wrong}); !errors.Is(err, domain.ErrNFOInvalidated) || rows != nil {
		t.Fatal("inventory stamp mismatch", err)
	}
	stale := l
	stale.Generation++
	if p, err := f.s.BeginNFOPhase(f.ctx, stale); !errors.Is(err, domain.ErrJobLeaseLost) || p != (domain.NFOPhase{}) {
		t.Fatal("stale parent")
	}
	if before != nfoSnapshot(t, f) {
		t.Fatal("rejected request changed state")
	}
	if err := f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	next := f.claim(t, "new-owner")
	if p, err := f.s.LoadNFOPhase(f.ctx, l); !errors.Is(err, domain.ErrJobLeaseLost) || p != (domain.NFOPhase{}) {
		t.Fatal("released owner revived")
	}
	if p, err := f.s.LoadNFOPhase(f.ctx, next); err != nil || p.Token != page.Token {
		t.Fatal("resume lost checkpoint", err)
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, next.Job.ID); err != nil {
		t.Fatal(err)
	}
	if p, err := f.s.BeginNFOPhase(f.ctx, next); !errors.Is(err, context.Canceled) || p != (domain.NFOPhase{}) {
		t.Fatal("persistent cancel", err)
	}
	if err := f.s.FinishJob(f.ctx, next, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
}
func TestNFOInvalidInputsWithoutDatabase(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	calls := []func() error{
		func() error { return s.EnsureNFOCachePolicy(ctx, domain.NFOCachePolicy{}) },
		func() error { _, e := s.PrepareNFOPhase(ctx, domain.JobLease{}, domain.NFOIdentity{}); return e },
		func() error { _, e := s.NextNFOPage(ctx, domain.JobLease{}, 0); return e },
		func() error { _, e := s.LookupNFOBatch(ctx, domain.JobLease{}, domain.NFOPageToken{}, nil); return e },
		func() error { _, e := s.CommitNFOBatch(ctx, domain.JobLease{}, domain.NFOPageToken{}, nil); return e },
		func() error { return s.AbortNFOPhase(ctx, domain.JobLease{}, "secret") },
		func() error { _, e := s.SweepNFOCache(ctx, 129); return e },
	}
	for i, call := range calls {
		if !errors.Is(call(), domain.ErrInvalid) {
			t.Fatalf("invalid call %d", i)
		}
	}
}

func TestNFOCacheLeaseExpiryAndHitTTLAtCheckpoint(t *testing.T) {
	for _, boundary := range []string{"lease", "hit-ttl"} {
		t.Run(boundary, func(t *testing.T) {
			f := newNFOFixture(t)
			l, _ := f.start(t, "seed", "a.nfo")
			f.parseHead(t, l, nfoValidSummary())
			f.finish(t, l)
			l, page := f.start(t, "blocked", "a.nfo")
			c := domain.NFOCompletion{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionHit}
			target := "UPDATE jobs SET lease_until=clock_timestamp()+interval '350 milliseconds' WHERE id=$1::uuid"
			want := domain.ErrJobLeaseLost
			if boundary == "hit-ttl" {
				target = "UPDATE nfo_cache SET expires_at=clock_timestamp()+interval '350 milliseconds' WHERE $1::uuid IS NOT NULL"
				want = domain.ErrConflict
			}
			if _, err := f.s.Pool.Exec(f.ctx, target, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_test_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.5); RETURN NEW; END $$; CREATE TRIGGER nfo_test_pause BEFORE UPDATE ON nfo_job_state FOR EACH ROW EXECUTE FUNCTION nfo_test_pause()`); err != nil {
				t.Fatal(err)
			}
			before := nfoSnapshot(t, f)
			started := time.Now()
			p, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, []domain.NFOCompletion{c})
			if !errors.Is(err, want) || p != (domain.NFOPhase{}) || before != nfoSnapshot(t, f) || time.Since(started) < 500*time.Millisecond {
				t.Fatal("late fence failed to roll back checkpoint/cache", err)
			}
		})
	}
}

func TestNFOCacheMigrationGuardAndRoundTrip(t *testing.T) {
	f := newNFOFixture(t)
	l, _ := f.start(t, "migration", "a.nfo")
	body, err := migrationFiles.ReadFile("migrations/000006_nfo_cache.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, f)
	conn, err := f.s.Pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, denied := conn.Exec(f.ctx, string(body))
	_, rolled := conn.Exec(f.ctx, "ROLLBACK")
	conn.Release()
	if denied == nil || rolled != nil || before != nfoSnapshot(t, f) {
		t.Fatal("active NFO downgrade was not atomic")
	}
	f.parseHead(t, l, nfoValidSummary())
	f.finish(t, l)
	const query = `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j)-'inventory_generation'-'ignore_requested' ORDER BY id) FROM jobs j),'inventory',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM job_inventory i),'baseline',(SELECT jsonb_agg(to_jsonb(b)-'observed_revision'-'attributes_known'-'kind'-'size'-'modified_unix_nano'-'inventory_generation' ORDER BY library_id,root_id,path) FROM library_inventory_baseline b))::text`
	var preserved, after string
	if err = f.s.Pool.QueryRow(f.ctx, query).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	dsn := f.s.Pool.Config().ConnString()
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 35 {
		t.Fatalf("down35 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 34 {
		t.Fatalf("down34 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 33 {
		t.Fatalf("down33 version=%d dirty=%v error=%v", version, dirty, e)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 32 {
		t.Fatalf("down32 version=%d dirty=%v error=%v", version, dirty, e)
	}
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
		t.Fatal("manifest downgrade failed", v, dirty, err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 7 {
		t.Fatal("rollback ignore intent schema", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || v != 6 {
		t.Fatal("rollback NFO worker schema", err)
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "down"); e != nil || dirty || version != 5 {
		t.Fatal("down006", e)
	}
	if err = f.s.Pool.QueryRow(f.ctx, query).Scan(&after); err != nil || preserved != after {
		t.Fatal("down006 changed parent inventory/baseline")
	}
	if f.s.Ready(f.ctx) == nil {
		t.Fatal("schema5 accepted by current binary")
	}
	if version, dirty, e := Migrate(f.ctx, dsn, "up"); e != nil || dirty || version != SchemaVersion {
		t.Fatal("up006", e)
	}
}

func TestNFOCacheFreshFailureRollsBackAndRetries(t *testing.T) {
	f := newNFOFixture(t)
	l, page := f.start(t, "rollback", "a.nfo")
	summary := nfoValidSummary()
	batch := []domain.NFOCompletion{{Candidate: nfoCandidate(page.Entries[0]), Kind: domain.NFOCompletionParsed, Summary: &summary}}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_test_deny() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test checkpoint unavailable' USING ERRCODE='42501'; END $$; CREATE TRIGGER nfo_test_deny BEFORE UPDATE ON nfo_job_state FOR EACH ROW EXECUTE FUNCTION nfo_test_deny()`); err != nil {
		t.Fatal(err)
	}
	before := nfoSnapshot(t, f)
	p, err := f.s.CommitNFOBatch(f.ctx, l, page.Token, batch)
	if !errors.Is(err, domain.ErrDatabase) || p != (domain.NFOPhase{}) || before != nfoSnapshot(t, f) {
		t.Fatal("failed checkpoint retained cache/quota", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_test_deny ON nfo_job_state`); err != nil {
		t.Fatal(err)
	}
	p, err = f.s.CommitNFOBatch(f.ctx, l, page.Token, batch)
	if err != nil || p.Progress.Parsed != 1 {
		t.Fatal("retry after rollback", err)
	}
	assertNFOPlanQuota(t, f.jobFixture, 1)
}
