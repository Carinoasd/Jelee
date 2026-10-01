package postgres

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (f probeFixture) scanned(t *testing.T, key string, names ...string) domain.JobLease {
	t.Helper()
	f.submit(t, key)
	l := f.claim(t, "boundary-owner")
	d := f.directory(t, l)
	batch := domain.ScanBatch{Done: true}
	for _, name := range names {
		batch.Entries = append(batch.Entries, scanEntry(d, name, 7))
	}
	if err := f.s.SaveScanBatch(f.ctx, l, d, batch); err != nil {
		t.Fatal(err)
	}
	return l
}
func (f probeFixture) saveHead(t *testing.T, l domain.JobLease) {
	t.Helper()
	p, c := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, p.Token, c[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, p.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}}); err != nil {
		t.Fatal(err)
	}
}
func (f probeFixture) importItem(t *testing.T, path string) string {
	t.Helper()
	var root, name string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT r.path,l.name FROM library_roots r JOIN libraries l ON l.id=r.library_id WHERE r.id=$1::uuid`, f.registration.RootID).Scan(&root, &name); err != nil {
		t.Fatal(err)
	}
	source, err := f.s.ImportVideo(f.ctx, name, root, path, "Probe fixture", "video/x-matroska")
	if err != nil {
		t.Fatal(err)
	}
	var item string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestProbeRebuildScopesAndReplayPreserveGenerations(t *testing.T) {
	f := newProbeFixture(t)
	item := f.importItem(t, "a.mkv")
	f.importItem(t, "b.mkv")
	l, _ := f.begin(t, "seed", "a.mkv", "b.mkv")
	f.saveHead(t, l)
	f.saveHead(t, l)
	f.finish(t, l)
	l = f.scanned(t, "library-rebuild", "a.mkv", "b.mkv")
	start := domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeLibraryRebuild}
	p, err := f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil || p != again {
		t.Fatal("rebuild replay changed generation", err)
	}
	page, c := f.page(t, l)
	results, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Kind != domain.ProbeLookupMiss {
			t.Fatal("forced library rebuild reused previous generation")
		}
	}
	f.saveHead(t, l)
	f.saveHead(t, l)
	f.finish(t, l)
	l = f.scanned(t, "item-rebuild", "a.mkv", "b.mkv")
	start = domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeItemRebuild, TargetItemID: item}
	p, err = f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil {
		t.Fatal(err)
	}
	again, err = f.s.BeginProbePhase(f.ctx, l, start)
	if err != nil || p != again {
		t.Fatal("item rebuild replay changed generation")
	}
	page, c = f.page(t, l)
	if len(page.Entries) != 1 || page.Entries[0].Inventory.Path != "a.mkv" {
		t.Fatal("item scope leaked unrelated inventory")
	}
	results, err = f.s.LookupProbeBatch(f.ctx, l, page.Token, c)
	if err != nil || results[0].Kind != domain.ProbeLookupMiss {
		t.Fatal("item rebuild hit old generation", err)
	}
	f.saveHead(t, l)
	done, err := f.s.FinishProbePhase(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if repeat, err := f.s.FinishProbePhase(f.ctx, l); err != nil || repeat != done {
		t.Fatal("finish replay changed checkpoint")
	}
	if _, err = f.s.NextProbePage(f.ctx, l, 1); err != domain.ErrConflict {
		t.Fatal("completed phase readable")
	}
	if err = f.s.AbortProbePhase(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != domain.ErrConflict {
		t.Fatal("completed phase aborted")
	}
	if err = f.s.FinishJob(f.ctx, l, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 2, 0)
}

func TestProbeScopeMutationFencesInflightResults(t *testing.T) {
	for _, mutation := range []string{"root", "item", "mapping"} {
		t.Run(mutation, func(t *testing.T) {
			f := newProbeFixture(t)
			item := f.importItem(t, "a.mkv")
			l := f.scanned(t, "mutation", "a.mkv")
			start := domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeIncremental}
			if mutation == "item" {
				start.Scope = domain.ProbeScopeItemRebuild
				start.TargetItemID = item
			}
			if _, err := f.s.BeginProbePhase(f.ctx, l, start); err != nil {
				t.Fatal(err)
			}
			page, c := f.page(t, l)
			lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "root":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=$2 WHERE id=$1::uuid`, f.registration.RootID, t.TempDir())
			case "item":
				err = f.s.InvalidateProbeItem(f.ctx, f.a, item)
			case "mapping":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE media_sources SET relative_path='other.mkv' WHERE item_id=$1::uuid`, item)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}})
			if mutation == "mapping" {
				if err != domain.ErrProbeLeaseLost {
					t.Fatal("mapping drift committed", err)
				}
			} else if err != domain.ErrProbeInvalidated {
				t.Fatal("scope drift committed", err)
			}
			f.quota(t, 1, 1)
			var n int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT processed FROM probe_job_state WHERE job_id=$1::uuid`, l.Job.ID).Scan(&n); err != nil || n != 0 {
				t.Fatal("rejected result advanced checkpoint")
			}
			if err = f.s.ReleaseProbeLease(f.ctx, l, lease); err != nil {
				t.Fatal("cannot clean invalidated lease", err)
			}
			if err = f.s.AbortProbePhase(f.ctx, l, domain.ProbePhaseInvalidated); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.FinishProbePhase(f.ctx, l); err == nil {
				t.Fatal("aborted phase finished")
			}
			f.quota(t, 0, 0)
		})
	}
}

func TestProbeCurrentKeyAndTTLRecheckedAtCommit(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "cache-seed", "a.mkv")
	f.saveHead(t, l)
	f.finish(t, l)
	l, _ = f.begin(t, "cache-read", "a.mkv")
	page, c := f.page(t, l)
	if _, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0]); err != domain.ErrConflict {
		t.Fatal("fresh hit launched probe")
	}
	wrong := c[0]
	wrong.Stamp.Size++
	if _, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, []domain.ProbeCandidate{wrong}); err != domain.ErrProbeInvalidated {
		t.Fatal("inventory stamp mismatch read cache")
	}
	wrong = c[0]
	wrong.Stamp.Fingerprint = strings.Repeat("e", 64)
	if got, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, []domain.ProbeCandidate{wrong}); err != nil || got[0].Kind != domain.ProbeLookupMiss {
		t.Fatal("different edge bytes hit", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionHit}}); err != domain.ErrConflict {
		t.Fatal("expired hit checkpointed", err)
	}
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, c); err != nil || got[0].Kind != domain.ProbeLookupBusy {
		t.Fatal("leased path not busy", err)
	}
	if _, err = f.s.HeartbeatJob(f.ctx, l, time.Minute); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = time.Unix(1, 0) // The database timestamp, not a caller's stale observation, is authoritative.
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionChanged, Lease: &lease}}); err != nil {
		t.Fatal("valid renewed lease rejected", err)
	}
	f.quota(t, 0, 0)
	f.finish(t, l)
	// A retained done phase does not prevent sweeping an empty scope. A new
	// job must recreate its scope reservation before starting a cache miss.
	if _, err = f.s.SweepProbeCache(f.ctx, 128); err != nil {
		t.Fatal(err)
	}
	l, _ = f.begin(t, "after-empty-sweep", "a.mkv")
	page, c = f.page(t, l)
	lease, err = f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
	if err != nil {
		t.Fatal("scope not restored", err)
	}
	if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionUnavailable, Lease: &lease}}); err != nil {
		t.Fatal(err)
	}
	f.quota(t, 0, 0)
	f.finish(t, l)
}

func TestProbeMetadataJSONBExpansionBecomesBoundedFailure(t *testing.T) {
	f := newProbeFixture(t)
	ptr := func(v int64) *int64 { return &v }
	str := func(v string) *string { return &v }
	yes := true
	r := &domain.MediaRational{Numerator: math.MaxInt64 - 1, Denominator: math.MaxInt64}
	m := domain.MediaMetadata{Format: domain.MediaFormat{DurationMicros: ptr(math.MaxInt64), SizeBytes: ptr(math.MaxInt64), BitRate: ptr(math.MaxInt64)}}
	for i := 0; i < 256; i++ {
		m.Chapters = append(m.Chapters, domain.MediaChapter{ID: math.MaxInt64 - int64(i), StartMicros: ptr(math.MaxInt64 - 1), EndMicros: ptr(math.MaxInt64)})
	}
	var compact, expanded int
	for i := 0; i < 64; i++ {
		m.Streams = append(m.Streams, domain.MediaStream{Index: math.MaxInt32 - i, Kind: "video", Codec: str("h264"), Profile: str("High 4:4:4 Predictive"), Language: str("eng-abcdefgh-abcdefgh-abcdefgh"), DurationMicros: ptr(math.MaxInt64), BitRate: ptr(math.MaxInt64), Default: &yes, Forced: &yes, Video: &domain.MediaVideo{Level: ptr(65535), Width: ptr(65535), Height: ptr(65535), FrameRate: r, AverageFrameRate: r, ColorRange: str("pc"), ColorSpace: str("chroma-derived-nc"), ColorTransfer: str("iec61966-2-1"), ColorPrimaries: str("smpte170m"), HDR10Plus: &yes, MasteringDisplay: &domain.MediaMasteringDisplay{RedX: r, RedY: r, GreenX: r, GreenY: r, BlueX: r, BlueY: r, WhiteX: r, WhiteY: r, MinLuminance: r, MaxLuminance: r}, ContentLight: &domain.MediaContentLight{MaxContent: ptr(65535), MaxAverage: ptr(65535)}, DolbyVision: &domain.MediaDolbyVision{Profile: ptr(255), Level: ptr(255), CompatibilityID: ptr(15), RPU: &yes, EnhancementLayer: &yes, BaseLayer: &yes}}})
		payload, err := domain.MarshalProbeMetadata(m)
		if err != nil {
			t.Fatal("normalized fixture exceeded compact limit before reaching jsonb boundary", err)
		}
		compact = len(payload)
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT octet_length($1::jsonb::text)`, string(payload)).Scan(&expanded); err != nil {
			t.Fatal(err)
		}
		if expanded > domain.ProbeMetadataMaxBytes {
			break
		}
	}
	if compact > domain.ProbeMetadataMaxBytes || expanded <= domain.ProbeMetadataMaxBytes {
		t.Fatalf("fixture did not exercise compact/JSONB difference: compact=%d expanded=%d", compact, expanded)
	}
	t.Logf("normalized compact bytes=%d; PostgreSQL JSONB text bytes=%d", compact, expanded)
	l, _ := f.begin(t, "large-metadata", "a.mkv")
	page, c := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: &m}})
	if err != nil || p.Progress.Failed != 1 || p.Progress.Succeeded != 0 {
		t.Fatal("metadata limit not terminal media failure", err)
	}
	var valid bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT state='failed' AND metadata IS NULL AND error_code='probe_metadata_limit' AND charge_bytes=2048 AND lease_owner IS NULL FROM probe_cache`).Scan(&valid); err != nil || !valid {
		t.Fatal("oversized JSONB persisted or reservation leaked")
	}
	f.quota(t, 1, 0)
	f.finish(t, l)
}

func TestProbeAdminExpiryDuringWriteRollsBackGenerationAndAudit(t *testing.T) {
	f := newProbeFixture(t)
	var before int64
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT probe_generation FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION delay_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER delay_invalidation BEFORE UPDATE OF probe_generation ON libraries FOR EACH ROW EXECUTE FUNCTION delay_invalidation()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.InvalidateProbeLibrary(f.ctx, f.a, f.registration.Library.ID); err != domain.ErrUnauthenticated {
		t.Fatal("session expired during write remained authorized", err)
	}
	var after int64
	var audits int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT probe_generation,(SELECT count(*) FROM audit_logs WHERE event='probe.library_invalidated') FROM libraries WHERE id=$1::uuid`, f.registration.Library.ID).Scan(&after, &audits); err != nil || before != after || audits != 0 {
		t.Fatal("expired administrator committed generation or audit")
	}
}

func TestProbeNegativeTTLRetryAndNewToolIdentity(t *testing.T) {
	f := newProbeFixture(t)
	for attempt := 0; attempt < 3; attempt++ {
		l, _ := f.begin(t, "negative-retry-"+string(rune('a'+attempt)), "a.mkv")
		page, c := f.page(t, l)
		if attempt > 0 {
			if _, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0]); err != domain.ErrConflict {
				t.Fatal("unexpired negative cache retried", err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET expires_at=clock_timestamp()-interval '1 second',retry_after=clock_timestamp()-interval '1 second'`); err != nil {
				t.Fatal(err)
			}
		}
		if attempt == 2 {
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE probe_cache SET failure_count=10`); err != nil {
				t.Fatal(err)
			}
		}
		lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionFailed, Lease: &lease, FailureCode: domain.ProbeFailureTimeout}}); err != nil {
			t.Fatal(err)
		}
		var n int
		expected := attempt + 1
		if attempt == 2 {
			expected = 10
		}
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT failure_count FROM probe_cache`).Scan(&n); err != nil || n != expected {
			t.Fatal("negative retry history reset or grew unexpectedly")
		}
		f.finish(t, l)
		f.quota(t, 1, 0)
	}
	identity := probeTestIdentity()
	identity.VendorVersion = "different-verified-build"
	var err error
	f.identity, err = f.s.RegisterProbeIdentity(f.ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := f.begin(t, "new-identity", "a.mkv")
	page, c := f.page(t, l)
	if got, err := f.s.LookupProbeBatch(f.ctx, l, page.Token, c); err != nil || got[0].Kind != domain.ProbeLookupMiss {
		t.Fatal("new tool identity reused old negative result", err)
	}
	f.saveHead(t, l)
	f.finish(t, l)
	f.quota(t, 1, 0)
	var failures int
	var id string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT failure_count,tool_version_id::text FROM probe_cache`).Scan(&failures, &id); err != nil || failures != 0 || id != f.identity.ID {
		t.Fatal("replacement identity not committed atomically")
	}
}

func TestProbeBeginMissingIdentityAndItemHaveNoAdmissionEffects(t *testing.T) {
	f := newProbeFixture(t)
	l := f.scanned(t, "missing-phase", "a.mkv")
	start := domain.ProbePhaseStart{Identity: f.identity, Scope: domain.ProbeScopeIncremental}
	start.Identity.Digest = strings.Repeat("e", 64)
	if _, err := f.s.BeginProbePhase(f.ctx, l, start); err != domain.ErrProbeIdentityMismatch {
		t.Fatal("unknown identity admitted", err)
	}
	start.Identity = f.identity
	start.Scope = domain.ProbeScopeItemRebuild
	start.TargetItemID = "11111111-1111-4111-8111-111111111111"
	if _, err := f.s.BeginProbePhase(f.ctx, l, start); err != domain.ErrNotFound {
		t.Fatal("unknown item admitted", err)
	}
	var scopes, phases int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT library_scopes,(SELECT count(*) FROM probe_job_state) FROM probe_cache_quota`).Scan(&scopes, &phases); err != nil || scopes != 0 || phases != 0 {
		t.Fatal("failed phase consumed cardinality/checkpoint")
	}
	start.Scope = domain.ProbeScopeIncremental
	start.TargetItemID = ""
	if _, err := f.s.BeginProbePhase(f.ctx, l, start); err != nil {
		t.Fatal("valid retry failed", err)
	}
	if _, err := f.s.FinishProbePhase(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("unfinished prefix marked done")
	}
	if err := f.s.AbortProbePhase(f.ctx, l, domain.ProbePhaseRuntimeUnavailable); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.FinishProbePhase(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("aborted phase completed")
	}
}

func TestProbeMalformedTokensAndMetadataCannotTouchLease(t *testing.T) {
	f := newProbeFixture(t)
	l, _ := f.begin(t, "bad-values", "a.mkv")
	page, c := f.page(t, l)
	lease, err := f.s.AcquireProbe(f.ctx, l, page.Token, c[0])
	if err != nil {
		t.Fatal(err)
	}
	before := probeFaultSnapshot(t, f)
	badToken := domain.ProbePageToken{Revision: 1, AfterID: "not-an-id"}
	badCandidate := c[0]
	badCandidate.Stamp.Fingerprint = strings.Repeat("F", 64)
	badMetadata := domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "video", Video: &domain.MediaVideo{}, Language: func() *string { s := "private/path"; return &s }()}}}
	for name, call := range map[string]func() error{
		"lookup token":  func() error { _, e := f.s.LookupProbeBatch(f.ctx, l, badToken, c); return e },
		"acquire token": func() error { _, e := f.s.AcquireProbe(f.ctx, l, badToken, c[0]); return e },
		"commit token": func() error {
			_, e := f.s.CommitProbeBatch(f.ctx, l, badToken, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}})
			return e
		},
		"stamp": func() error { _, e := f.s.AcquireProbe(f.ctx, l, page.Token, badCandidate); return e },
		"metadata": func() error {
			_, e := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c[0], Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: &badMetadata}})
			return e
		},
		"missing context":       func() error { return f.s.EnsureProbePolicy(nil, domain.DefaultProbeCachePolicy()) },
		"missing admin context": func() error { return f.s.InvalidateProbeLibrary(nil, f.a, l.Job.LibraryID) },
	} {
		t.Run(name, func(t *testing.T) {
			if e := call(); e != domain.ErrInvalid {
				t.Fatal("malformed value accepted", e)
			}
		})
	}
	if before != probeFaultSnapshot(t, f) {
		t.Fatal("malformed value mutated private state")
	}
	f.saveAfterLease(t, l, page, c[0], lease)
}

func (f probeFixture) saveAfterLease(t *testing.T, l domain.JobLease, page domain.ProbePage, c domain.ProbeCandidate, lease domain.ProbeLease) {
	t.Helper()
	if _, err := f.s.CommitProbeBatch(f.ctx, l, page.Token, []domain.ProbeCompletion{{Candidate: c, Kind: domain.ProbeCompletionSucceeded, Lease: &lease, Metadata: probeTestMetadata()}}); err != nil {
		t.Fatal("lease unusable after rejected input", err)
	}
}

func TestProbePolicyRequiredBeforeAdmission(t *testing.T) {
	f := probeFixture{jobFixture: newJobFixture(t)}
	l := f.scanned(t, "no-policy", "a.mkv")
	start := domain.ProbePhaseStart{Identity: domain.ProbeIdentityRef{ID: "11111111-1111-4111-8111-111111111111", Digest: strings.Repeat("a", 64)}, Scope: domain.ProbeScopeIncremental}
	for name, call := range map[string]func() error{
		"identity": func() error { _, e := f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity()); return e },
		"begin":    func() error { _, e := f.s.BeginProbePhase(f.ctx, l, start); return e },
		"page":     func() error { _, e := f.s.NextProbePage(f.ctx, l, 1); return e },
		"finish":   func() error { _, e := f.s.FinishProbePhase(f.ctx, l); return e },
		"abort":    func() error { return f.s.AbortProbePhase(f.ctx, l, domain.ProbePhaseRuntimeUnavailable) },
		"sweep":    func() error { _, e := f.s.SweepProbeCache(f.ctx, 128); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if e := call(); e != domain.ErrNotFound {
				t.Fatal("uninitialized policy admitted work", e)
			}
		})
	}
	var n int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM tool_versions)+(SELECT count(*) FROM probe_job_state)+(SELECT count(*) FROM probe_cache)+(SELECT count(*) FROM probe_library_quota)`).Scan(&n); err != nil || n != 0 {
		t.Fatal("uninitialized policy created partial state")
	}
}
