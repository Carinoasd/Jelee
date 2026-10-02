package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.ProbeRepository = (*Store)(nil)
var _ app.ProbeAdminRepository = (*Store)(nil)

func probeTestIdentity() domain.ProbeIdentity {
	return domain.ProbeIdentity{Platform: "linux-amd64", VendorVersion: "n9.0.2-test", UpstreamVersion: "9.0.2", SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("a", 64), RuntimeSHA256: strings.Repeat("b", 64), ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion, ArgumentsSHA256: strings.Repeat("c", 64), SandboxVersion: "sandbox-v1", FingerprintVersion: domain.ProbeFingerprintVersion}
}
func probeTestMetadata() *domain.MediaMetadata {
	return &domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "video", Video: &domain.MediaVideo{}}}}
}

type probeFixture struct {
	jobFixture
	identity domain.ProbeIdentityRef
}

func newProbeFixture(t *testing.T, policies ...domain.ProbeCachePolicy) probeFixture {
	t.Helper()
	f := probeFixture{jobFixture: newJobFixture(t)}
	policy := domain.DefaultProbeCachePolicy()
	if len(policies) > 0 {
		policy = policies[0]
	}
	if err := f.s.EnsureProbePolicy(f.ctx, policy); err != nil {
		t.Fatal("initialize probe policy", err)
	}
	var err error
	f.identity, err = f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	if err != nil {
		t.Fatal("register probe identity", err)
	}
	return f
}

func TestProbeEvictionUsesBoundedIndexPagesAndExactDeltas(t *testing.T) {
	policy := domain.DefaultProbeCachePolicy()
	policy.MaxRows = 10000
	policy.LibraryMaxRows = 5000
	f := newProbeFixture(t, policy)
	l, _ := f.begin(t, "eviction", "incoming.mkv")
	page, candidates := f.page(t, l)
	// Bulk fixture setup belongs only to this isolated schema; all measured
	// admission and eviction work then uses the real repository transaction.
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes) SELECT r.id,'evict-'||lpad(n::text,6,'0')||'.mkv',r.library_id,7,123456789,decode(repeat('d',64),'hex'),'edge-sha256-v1',$2::uuid,lib.probe_generation,r.probe_generation,'ready','{"format":{},"streams":[{"index":0,"kind":"video","video":{}}],"chapters":[]}'::jsonb,CASE WHEN n<=2500 THEN clock_timestamp()-interval '1 day' ELSE clock_timestamp()+interval '1 day' END,2048+octet_length('{"format":{},"streams":[{"index":0,"kind":"video","video":{}}],"chapters":[]}'::jsonb::text) FROM library_roots r JOIN libraries lib ON lib.id=r.library_id CROSS JOIN generate_series(1,5000) n WHERE r.id=$1::uuid`, f.registration.RootID, f.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "index-noise", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO probe_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM probe_cache_quota`, other.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes,last_used_at) SELECT $2::uuid,c.relative_path,$1::uuid,c.size,c.modified_unix_nano,c.fingerprint,c.fingerprint_version,c.tool_version_id,lib.probe_generation,r.probe_generation,c.state,c.metadata,c.expires_at-interval '1 day',c.charge_bytes,c.last_used_at-interval '1 day' FROM probe_cache c CROSS JOIN libraries lib CROSS JOIN library_roots r WHERE c.library_id=$3::uuid AND lib.id=$1::uuid AND r.id=$2::uuid`, other.Library.ID, other.RootID, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_cache_quota SET library_scopes=2,rows_used=10000,bytes_used=(SELECT sum(charge_bytes) FROM probe_cache); UPDATE probe_library_quota q SET rows_used=5000,bytes_used=(SELECT sum(charge_bytes) FROM probe_cache c WHERE c.library_id=q.library_id); ANALYZE probe_cache`); err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{"global_expired": probeExpiredPageSQL, "global_lru": probeLRUPageSQL, "library_expired": probeLibraryExpiredPageSQL, "library_lru": probeLibraryLRUPageSQL, "sweep_pending": probeSweepPendingSQL, "sweep_expired": probeSweepExpiredSQL} {
		t.Run(name, func(t *testing.T) {
			args := []any{f.registration.RootID, "incoming.mkv", 128}
			if strings.HasPrefix(name, "library") {
				args = append(args, f.registration.Library.ID)
			}
			if strings.HasPrefix(name, "sweep") {
				args = []any{128}
			}
			var raw []byte
			if err := f.s.Pool.QueryRow(f.ctx, `EXPLAIN(ANALYZE,FORMAT JSON,COSTS false) `+query, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			type planNode struct {
				NodeType string            `json:"Node Type"`
				Index    string            `json:"Index Name"`
				Rows     int               `json:"Actual Rows"`
				Removed  int               `json:"Rows Removed by Filter"`
				Plans    []json.RawMessage `json:"Plans"`
			}
			var outer []struct {
				Plan json.RawMessage `json:"Plan"`
			}
			if err := json.Unmarshal(raw, &outer); err != nil || len(outer) != 1 {
				t.Fatal("invalid explain")
			}
			found := false
			var check func(json.RawMessage)
			check = func(value json.RawMessage) {
				var p planNode
				if err := json.Unmarshal(value, &p); err != nil {
					t.Fatal(err)
				}
				if p.NodeType == "Sort" || p.NodeType == "Seq Scan" {
					t.Fatal("cache page sorted/scanned full fixture")
				}
				if p.Index != "" {
					found = true
					if p.Rows+p.Removed > 129 {
						t.Fatal("cache index page visited beyond bounded prefix")
					}
					t.Logf("%s: %s %s visited %d rows", name, p.NodeType, p.Index, p.Rows+p.Removed)
				}
				for _, child := range p.Plans {
					check(child)
				}
			}
			check(outer[0].Plan)
			if !found {
				t.Fatal("page did not use index")
			}
		})
	}
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal("bounded eviction failed admission", err)
	}
	f.quota(t, 10000-128+1, 1)
	if err = f.s.ReleaseProbeLease(f.ctx, l, lease); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 10000-128, 0)
}
func (f probeFixture) begin(t *testing.T, key string, names ...string) (domain.JobLease, domain.ProbePhase) {
	t.Helper()
	f.submit(t, key)
	l := f.claim(t, "probe-owner")
	d := f.directory(t, l)
	entries := make([]domain.InventoryEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, scanEntry(d, name, 7))
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Entries: entries, Done: true}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.BeginProbePhase(f.ctx, l, domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeIncremental})
	if err != nil {
		t.Fatal("begin probe phase", err)
	}
	return l, p
}
func (f probeFixture) page(t *testing.T, l domain.JobLease) (domain.ProbePage, []domain.ProbeCandidate) {
	t.Helper()
	page, err := f.s.NextProbePage(f.ctx, l, domain.ProbePageMax)
	if err != nil {
		t.Fatal(err)
	}
	candidates := make([]domain.ProbeCandidate, 0, len(page.Entries))
	for _, e := range page.Entries {
		candidates = append(candidates, domain.ProbeCandidate{InventoryID: e.Inventory.ID, Stamp: domain.ProbeStamp{Size: e.Inventory.Size, ModifiedUnixNano: e.Inventory.ModifiedUnixNano, Fingerprint: strings.Repeat("d", 64), FingerprintVersion: domain.ProbeFingerprintVersion}})
	}
	return page, candidates
}
func (f probeFixture) quota(t *testing.T, rows, leases int64) {
	t.Helper()
	var gr, gb, gl, ar, ab, al int64
	err := f.s.Pool.QueryRow(f.ctx, `SELECT rows_used,bytes_used,active_leases,(SELECT count(*) FROM probe_cache),(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache),(SELECT count(*) FROM probe_cache WHERE lease_owner IS NOT NULL) FROM probe_cache_quota WHERE singleton`).Scan(&gr, &gb, &gl, &ar, &ab, &al)
	if err != nil || gr != ar || gb != ab || gl != al || gr != rows || gl != leases {
		t.Fatalf("quota drift: rows=%d actual=%d bytes=%d actual=%d leases=%d actual=%d error=%v", gr, ar, gb, ab, gl, al, err)
	}
	var mismatch bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM probe_library_quota q WHERE q.rows_used<>(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id) OR q.bytes_used<>(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache c WHERE c.library_id=q.library_id) OR q.active_leases<>(SELECT count(*) FROM probe_cache c WHERE c.library_id=q.library_id AND c.lease_owner IS NOT NULL))`).Scan(&mismatch); err != nil || mismatch {
		t.Fatal("library quota drift")
	}
}
func (f probeFixture) finish(t *testing.T, l domain.JobLease) {
	t.Helper()
	if _, err := f.s.FinishProbePhase(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
}

func TestProbePolicyIdentityAndPhaseContract(t *testing.T) {
	f := newProbeFixture(t)
	policy := domain.DefaultProbeCachePolicy()
	if err := f.s.EnsureProbePolicy(f.ctx, policy); err != nil {
		t.Fatal(err)
	}
	policy.MaxRows++
	if err := f.s.EnsureProbePolicy(f.ctx, policy); err != domain.ErrConflict {
		t.Fatal("policy silently changed")
	}
	ref, err := f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	if err != nil || ref != f.identity {
		t.Fatal("identity replay changed")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE tool_versions SET vendor_version='modified' WHERE id=$1::uuid`, ref.ID); err == nil {
		t.Fatal("identity mutable")
	}
	f.submit(t, "phase")
	l := f.claim(t, "phase-owner")
	start := domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeIncremental}
	if _, err = f.s.BeginProbePhase(f.ctx, l, start); err != domain.ErrConflict {
		t.Fatal("phase began before scan finished")
	}
	d := f.directory(t, l)
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil || p != again {
		t.Fatal("begin replay reset phase")
	}
	other := start
	other.Identity.Digest = strings.Repeat("f", 64)
	if _, err = f.s.BeginProbePhase(f.ctx, l, other); err != domain.ErrConflict {
		t.Fatal("phase identity changed")
	}
	if _, err = f.s.NextScanDirectory(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("probe phase reopened inventory")
	}
	if err = f.s.SaveScanBatch(f.ctx, l, d, domain.ScanBatch{Done: true}); err != domain.ErrConflict {
		t.Fatal("probe phase rewrote inventory")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != domain.ErrConflict {
		t.Fatal("job bypassed phase completion")
	}
	if _, err = f.s.NextProbePage(f.ctx, l, 32); err != domain.ErrNotFound {
		t.Fatal("empty phase page")
	}
	f.finish(t, l)
	f.quota(t, 0, 0)
}

func TestProbePersistAndHitPrefixCheckpoint(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "first", "a.mkv", "b.mkv")
	for n := 0; n < 2; n++ {
		page, candidates := f.page(t, l)
		candidate := candidates[0]
		lookup, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, candidates)
		if err != nil || lookup[0].Kind != domain.ProbeLookupMiss {
			t.Fatal("initial cache was not miss", err)
		}
		lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidate)
		if err != nil {
			t.Fatal(err)
		}
		f.quota(t, int64(n+1), 1)
		if _, err = f.s.AcquireProbe(f.ctx, l, page.Token, candidate); err != domain.ErrProbeBusy {
			t.Fatal("same-owner repeat renewed lease")
		}
		completion := domain.ProbeCompletion{Candidate: candidate, Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}
		p, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion})
		if err != nil || p.Progress.Succeeded != int64(n+1) {
			t.Fatal("save metadata", err)
		}
		if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{completion}); err != domain.ErrConflict {
			t.Fatal("replay advanced counters")
		}
		f.quota(t, int64(n+1), 0)
	}
	f.finish(t, l)
	l, _ = f.begin(t, "second", "a.mkv", "b.mkv")
	page, candidates := f.page(t, l)
	lookup, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, candidates)
	if err != nil || len(lookup) != 2 || lookup[0].Kind != domain.ProbeLookupHit || lookup[1].Kind != domain.ProbeLookupHit {
		t.Fatal("stable key did not hit", err)
	}
	if _, err = f.s.AcquireProbe(f.ctx, l, page.Token, candidates[1]); err != domain.ErrConflict {
		t.Fatal("acquired beyond cursor")
	}
	completions := []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionHit}, {Candidate: candidates[1], Kind: domain.ProbeCompletionHit}}
	p, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, completions)
	if err != nil || p.Progress.Processed != 2 || p.Progress.Hits != 2 {
		t.Fatal("hit prefix", err)
	}
	f.finish(t, l)
	f.quota(t, 2, 0)
	var raw string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT metadata::text FROM probe_cache LIMIT 1`).Scan(&raw); err != nil || strings.Contains(raw, "root") || strings.Contains(raw, "stderr") {
		t.Fatal("metadata not normalized")
	}
}

func TestProbeNegativeChangedAndUnavailableOutcomes(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "negative", "a.mkv", "b.mkv", "c.mkv")
	page, candidates := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionFailed, Lease: &lease, FailureCode: domain.ProbeFailureMedia}})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{domain.ProbeCompletionChanged, domain.ProbeCompletionUnavailable} {
		page, candidates = f.page(t, l)
		_, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: domain.ProbeCandidate{InventoryID: candidates[0].InventoryID}, Kind: kind}})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.finish(t, l)
	f.quota(t, 1, 0)
	l, _ = f.begin(t, "negative-hit", "a.mkv", "b.mkv", "c.mkv")
	page, candidates = f.page(t, l)
	lookup, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, candidates)
	if err != nil {
		t.Fatal(err)
	}
	for i, result := range lookup {
		if result.Kind == domain.ProbeLookupNegativeHit {
			if i != 0 { // UUID order changes between jobs; commit preceding noncached entries first.
				for j := 0; j < i; j++ {
					p, c := f.page(t, l)
					if _, err = f.s.CommitProbeBatch(f.ctx, l, p.Token, []domain.ProbeCompletion{{Candidate: domain.ProbeCandidate{InventoryID: c[0].InventoryID}, Kind: domain.ProbeCompletionUnavailable}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			p, c := f.page(t, l)
			if _, err = f.s.CommitProbeBatch(f.ctx, l, p.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionNegativeHit}}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("failed stable key did not negative-hit")
}

func TestProbeLeaseABAConcurrencyCancellationAndExpiry(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "lease", "a.mkv")
	page, candidates := f.page(t, l)
	type outcome struct {
		lease domain.ProbeLease
		err   error
	}
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
			out <- outcome{lease, err}
		}()
	}
	wg.Wait()
	close(out)
	var old domain.ProbeLease
	wins := 0
	for value := range out {
		if value.err == nil {
			old = value.lease
			wins++
		} else if value.err != domain.ErrProbeBusy {
			t.Fatal(value.err)
		}
	}
	if wins != 1 {
		t.Fatal("multiple path owners")
	}
	f.quota(t, 1, 1)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil || current.Generation <= old.Generation {
		t.Fatal("ABA fence reused", err)
	}
	if err = f.s.ReleaseProbeLease(f.ctx, l, old); err != domain.ErrProbeLeaseLost {
		t.Fatal("stale release")
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &old, Metadata: probeTestMetadata()}}); err != domain.ErrProbeLeaseLost {
		t.Fatal("stale commit")
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &current, Metadata: probeTestMetadata()}}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel did not fence commit", err)
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
}

func TestProbeTransactionExpiryRollsBackMetadataQuotaAndCursor(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "expiry", "a.mkv")
	page, candidates := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION delay_probe_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ready' THEN PERFORM pg_sleep(1.1); END IF; RETURN NEW; END $$; CREATE TRIGGER delay_probe_save BEFORE UPDATE ON probe_cache FOR EACH ROW EXECUTE FUNCTION delay_probe_save()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET lease_until=clock_timestamp()+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}})
	if err != domain.ErrProbeLeaseLost || time.Since(started) < time.Second {
		t.Fatal("transaction expiry was not rejected after work", err)
	}
	f.quota(t, 1, 1)
	var state string
	var processed int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT c.state,p.processed FROM probe_cache c CROSS JOIN probe_job_state p`).Scan(&state, &processed); err != nil || state != "pending" || processed != 0 {
		t.Fatal("expired transaction partially committed")
	}
}

func TestProbeReleaseRecoveryInvalidationAndMigration(t *testing.T) {
	f := newProbeFixture(t)
	f.importItem(t, "a.mkv")
	f.complete(t, "baseline", []string{"a.mkv"}, 0)
	l, _ := f.begin(t, "recovery", "a.mkv")
	page, candidates := f.page(t, l)
	// Exercise the down body directly so its intentional refusal does not mark
	// this fixture's golang-migrate version dirty. The actual down/up runner is
	// tested below after the parent has terminated.
	func() {
		body, err := migrationFiles.ReadFile("migrations/000004_probe_cache.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := f.s.Pool.Acquire(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		_, rejected := conn.Exec(f.ctx, string(body))
		_, rollback := conn.Exec(f.ctx, "ROLLBACK")
		if rejected == nil || rollback != nil {
			t.Fatal("down4 failed to reject live parent safely")
		}
	}()
	if _, err := f.s.AcquireProbe(f.ctx, l, page.Token, candidates[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	next := f.claim(t, "replacement")
	if next.Generation <= l.Generation {
		t.Fatal("parent fence unchanged")
	}
	if _, err := f.s.NextProbePage(f.ctx, l, 1); err != domain.ErrJobLeaseLost {
		t.Fatal("stale parent reads phase")
	}
	if err := f.s.InvalidateProbeLibrary(f.ctx, f.a, l.Job.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.NextProbePage(f.ctx, next, 1); err != domain.ErrProbeInvalidated {
		t.Fatal("invalidation did not stop phase", err)
	}
	if err := f.s.AbortProbePhase(f.ctx, next, domain.ProbePhaseInvalidated); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, next, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	const preservedQuery = `SELECT jsonb_build_object('inventory',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM job_inventory i),'jobs',(SELECT jsonb_agg(to_jsonb(j)-'inventory_generation'-'ignore_requested' ORDER BY id) FROM jobs j),'baseline',(SELECT jsonb_agg(to_jsonb(b)-'observed_revision'-'attributes_known'-'kind'-'size'-'modified_unix_nano'-'inventory_generation' ORDER BY library_id,root_id,path) FROM library_inventory_baseline b),'items',(SELECT jsonb_agg(to_jsonb(i)-'probe_generation' ORDER BY id) FROM items i),'sources',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM media_sources m))::text`
	var before string
	if err := f.s.Pool.QueryRow(f.ctx, preservedQuery).Scan(&before); err != nil {
		t.Fatal(err)
	}
	dsn := f.s.Pool.Config().ConnString()
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
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != 4 {
		t.Fatal("down 5", err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != 3 {
		t.Fatal("down 4", err)
	}
	var after string
	if err := f.s.Pool.QueryRow(f.ctx, preservedQuery).Scan(&after); err != nil || before != after {
		t.Fatal("down4 changed existing catalog, inventory, jobs or baseline")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade to current schema", err)
	}
}

func TestProbeInvalidInputsDoNotNeedDatabase(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	tests := []func() error{
		func() error { return s.EnsureProbePolicy(ctx, domain.ProbeCachePolicy{}) },
		func() error { _, e := s.RegisterProbeIdentity(ctx, domain.ProbeIdentity{}); return e },
		func() error { _, e := s.BeginProbePhase(ctx, domain.JobLease{}, domain.ProbePhaseStart{}); return e },
		func() error { _, e := s.NextProbePage(ctx, domain.JobLease{}, 0); return e },
		func() error {
			_, e := s.LookupProbeBatch(ctx, domain.JobLease{}, domain.ProbePageToken{}, nil)
			return e
		},
		func() error {
			_, e := s.AcquireProbe(ctx, domain.JobLease{}, domain.ProbePageToken{}, domain.ProbeCandidate{})
			return e
		},
		func() error {
			_, e := s.CommitProbeBatch(ctx, domain.JobLease{}, domain.ProbePageToken{}, nil)
			return e
		},
		func() error { return s.ReleaseProbeLease(ctx, domain.JobLease{}, domain.ProbeLease{}) },
		func() error { return s.AbortProbePhase(ctx, domain.JobLease{}, "private error") },
		func() error { _, e := s.SweepProbeCache(ctx, 129); return e },
		func() error { return s.InvalidateProbeItem(ctx, domain.Actor{}, "bad") },
	}
	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if err := test(); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}
