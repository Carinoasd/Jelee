package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoPolicyRead(t *testing.T, f jobFixture) domain.NFOLibraryPolicy {
	t.Helper()
	p, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
	if err != nil {
		t.Fatal("read NFO policy")
	}
	return p
}

// Snapshots stay private: no failure prints paths, stored metadata, or SQL text.
func nfoPolicySnapshot(t *testing.T, f jobFixture) string {
	t.Helper()
	var state string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
	 'libraries',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,nfo_mode,nfo_generation) ORDER BY id),'[]') FROM libraries),
	 'roots',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,nfo_generation) ORDER BY id),'[]') FROM library_roots),
	 'requests',(SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY actor_id,idempotency_key),'[]') FROM nfo_policy_requests r),
	 'audit',(SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM audit_logs a)
	)::text`).Scan(&state)
	if err != nil {
		t.Fatal("read private policy snapshot")
	}
	return state
}

func nfoPolicyUnchanged(t *testing.T, f jobFixture, before string) {
	t.Helper()
	if nfoPolicySnapshot(t, f) != before {
		t.Fatal("rejected or replayed policy request changed mode, generation, ledger, or audit")
	}
}

func nfoPolicyFailure(t *testing.T, got domain.NFOLibraryPolicy, replay bool, err, want error) {
	t.Helper()
	// Exact fixed errors exclude PostgreSQL DETAIL/CONTEXT and private inputs.
	if err != want || got != (domain.NFOLibraryPolicy{}) || replay {
		t.Fatalf("policy rejection returned partial data or wrong safe error: error type %T", err)
	}
}

func nfoVideoState(t *testing.T, f jobFixture) string {
	t.Helper()
	var state string
	err := f.s.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
	 'cache',(SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY root_id,relative_path),'[]') FROM probe_cache c),
	 'quota',(SELECT COALESCE(jsonb_agg(to_jsonb(q)),'[]') FROM probe_cache_quota q),
	 'library_quota',(SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY library_id),'[]') FROM probe_library_quota q),
	 'libraries',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,probe_generation) ORDER BY id),'[]') FROM libraries),
	 'roots',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,probe_generation) ORDER BY id),'[]') FROM library_roots),
	 'items',(SELECT COALESCE(jsonb_agg(jsonb_build_array(id,probe_generation) ORDER BY id),'[]') FROM items)
	)::text`).Scan(&state)
	if err != nil {
		t.Fatal("read private video probe snapshot")
	}
	return state
}

func TestNFOPolicySwitchReplayCASAndVideoIsolation(t *testing.T) {
	video := newProbeFixture(t)
	f := video.jobFixture
	// Build a real persisted video observation through repository operations.
	lease, _ := video.begin(t, "video-before-nfo", "one.mkv")
	page, candidates := video.page(t, lease)
	child, err := f.s.AcquireProbe(f.ctx, lease, page.Token, candidates[0])
	if err != nil {
		t.Fatal("reserve video observation")
	}
	_, err = f.s.CommitProbeBatch(f.ctx, lease, page.Token, []domain.ProbeCompletion{{Candidate: candidates[0], Kind: domain.ProbeCompletionSucceeded, Lease: &child, Metadata: probeTestMetadata()}})
	if err != nil {
		t.Fatal("persist video observation")
	}
	video.finish(t, lease)
	beforeVideo := nfoVideoState(t, f)
	initial := nfoPolicyRead(t, f)
	if initial.Mode != domain.NFOModeOff || initial.Generation < 1 || initial.LibraryID != f.registration.Library.ID {
		t.Fatal("new library did not default to NFO off")
	}
	enabled, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, initial.LibraryID, "enable", initial.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || enabled.Mode != domain.NFOModeReadOnly || enabled.Generation != initial.Generation+1 {
		t.Fatal("enable did not advance only the NFO generation")
	}
	unchanged, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, initial.LibraryID, "same-mode", enabled.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || unchanged != enabled {
		t.Fatal("same-mode request bumped generation")
	}
	disabled, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, initial.LibraryID, "disable", enabled.Generation, domain.NFOModeOff)
	if err != nil || replay || disabled.Mode != domain.NFOModeOff || disabled.Generation != enabled.Generation+1 {
		t.Fatal("disable did not advance NFO generation")
	}
	before := nfoPolicySnapshot(t, f)
	again, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, initial.LibraryID, "enable", initial.Generation, domain.NFOModeReadOnly)
	if err != nil || !replay || again != enabled || nfoPolicyRead(t, f) != disabled {
		t.Fatal("exact replay did not retain its original result independently of current policy")
	}
	nfoPolicyUnchanged(t, f, before)
	for _, tc := range []struct {
		key, mode  string
		generation int64
	}{
		{"enable", domain.NFOModeOff, initial.Generation},
		{"enable", domain.NFOModeReadOnly, disabled.Generation},
		{"stale-cas", domain.NFOModeReadOnly, initial.Generation},
	} {
		got, r, e := f.s.SetNFOLibraryPolicy(f.ctx, f.a, initial.LibraryID, tc.key, tc.generation, tc.mode)
		nfoPolicyFailure(t, got, r, e, domain.ErrConflict)
		nfoPolicyUnchanged(t, f, before)
	}
	if nfoVideoState(t, f) != beforeVideo {
		t.Fatal("NFO policy altered a video probe generation, observation, or quota")
	}
	var rows, audits int
	var boundedTTL bool
	err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),bool_and(expires_at-created_at BETWEEN interval '24 hours' AND interval '24 hours 1 second'),(SELECT count(*) FROM audit_logs WHERE event='nfo.policy_changed') FROM nfo_policy_requests`).Scan(&rows, &boundedTTL, &audits)
	if err != nil || rows != 3 || audits != 3 || !boundedTTL {
		t.Fatal("policy requests did not retain bounded one-day idempotency or single mutation audits")
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other-nfo", t.TempDir())
	if err != nil {
		t.Fatal("register second NFO library")
	}
	before = nfoPolicySnapshot(t, f)
	got, r, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, other.Library.ID, "enable", initial.Generation, domain.NFOModeReadOnly)
	nfoPolicyFailure(t, got, r, err, domain.ErrConflict)
	nfoPolicyUnchanged(t, f, before)
}

type nfoPolicyOutcome struct {
	policy domain.NFOLibraryPolicy
	replay bool
	err    error
}

func nfoPolicyRace(f jobFixture, expected int64, mode string, keys ...string) []nfoPolicyOutcome {
	start := make(chan struct{})
	results := make(chan nfoPolicyOutcome, len(keys))
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, r, e := f.s.SetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID, key, expected, mode)
			results <- nfoPolicyOutcome{p, r, e}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var out []nfoPolicyOutcome
	for r := range results {
		out = append(out, r)
	}
	return out
}

func TestNFOPolicyConcurrentCASAndExactReplay(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_key_%t", sameKey), func(t *testing.T) {
			f := newJobFixture(t)
			initial := nfoPolicyRead(t, f)
			second := "second"
			if sameKey {
				second = "first"
			}
			created, replays, conflicts := 0, 0, 0
			for _, r := range nfoPolicyRace(f, initial.Generation, domain.NFOModeReadOnly, "first", second) {
				switch {
				case r.err == nil:
					if r.policy.Generation != initial.Generation+1 || r.policy.Mode != domain.NFOModeReadOnly {
						t.Fatal("concurrent change returned the wrong policy")
					}
					if r.replay {
						replays++
					} else {
						created++
					}
				case r.err == domain.ErrConflict:
					nfoPolicyFailure(t, r.policy, r.replay, r.err, domain.ErrConflict)
					conflicts++
				default:
					t.Fatal("unexpected concurrent policy failure")
				}
			}
			if created != 1 || (sameKey && replays != 1) || (!sameKey && conflicts != 1) {
				t.Fatal("concurrent requests did not serialize their CAS and idempotency")
			}
			var requests, audits int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_policy_requests),(SELECT count(*) FROM audit_logs WHERE event='nfo.policy_changed')`).Scan(&requests, &audits); err != nil || requests != 1 || audits != 1 {
				t.Fatal("concurrent requests repeated ledger or audit insertion")
			}
		})
	}
}

func TestNFOPolicyRequiresLiveAdministrationIncludingReplay(t *testing.T) {
	for _, state := range []string{"revoked", "demoted", "disabled", "forged-session"} {
		t.Run(state, func(t *testing.T) {
			f := newJobFixture(t)
			p := nfoPolicyRead(t, f)
			if _, _, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "saved", p.Generation, p.Mode); err != nil {
				t.Fatal("prepare retained policy request")
			}
			want := domain.ErrUnauthenticated
			// Direct SQL changes are private auth-state fixtures, not claims about
			// the behavior of the account administration API.
			var err error
			switch state {
			case "revoked":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID)
			case "demoted":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID)
				want = domain.ErrForbidden
			case "disabled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET disabled=true WHERE id=$1::uuid`, f.a.UserID)
			case "forged-session":
				f.a.SessionID = f.registration.Library.ID
			}
			if err != nil {
				t.Fatal("prepare private authorization state")
			}
			before := nfoPolicySnapshot(t, f)
			got, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID)
			nfoPolicyFailure(t, got, false, err, want)
			for _, key := range []string{"saved", "new"} {
				got, r, e := f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, key, p.Generation, p.Mode)
				nfoPolicyFailure(t, got, r, e, want)
			}
			nfoPolicyUnchanged(t, f, before)
		})
	}
}

func TestNFOPolicyLedgerQuotaSerializesLastSlot(t *testing.T) {
	for _, scope := range []string{"actor", "global"} {
		t.Run(scope, func(t *testing.T) {
			f := newJobFixture(t)
			p := nfoPolicyRead(t, f)
			var err error
			// SQL seeds only private generated ledger rows at the boundary.
			// The competing admissions and replay below are real public methods.
			if scope == "actor" {
				_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation) SELECT $1::uuid,'fixture-'||n,$2::uuid,'off',$3,$3 FROM generate_series(1,63) n`, f.a.UserID, p.LibraryID, p.Generation)
			} else {
				_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO users(name) SELECT 'nfo-ledger-'||n FROM generate_series(1,64) n`)
				if err == nil {
					_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation) SELECT u.id,'fixture-'||n,$1::uuid,'off',$2,$2 FROM users u CROSS JOIN generate_series(1,64) n WHERE u.name LIKE 'nfo-ledger-%' ORDER BY u.id,n LIMIT 4095`, p.LibraryID, p.Generation)
				}
			}
			if err != nil {
				t.Fatal("seed private ledger quota boundary")
			}
			created, denied := 0, 0
			for _, r := range nfoPolicyRace(f, p.Generation, p.Mode, "last-slot-a", "last-slot-b") {
				if r.err == nil {
					if r.replay || r.policy != p {
						t.Fatal("quota admission changed same-mode policy")
					}
					created++
				} else {
					nfoPolicyFailure(t, r.policy, r.replay, r.err, domain.ErrNFOCacheCapacity)
					denied++
				}
			}
			if created != 1 || denied != 1 {
				t.Fatal("quota admitted more or fewer than the last slot")
			}
			var total, actorRows, largestActor int
			var winner string
			err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE actor_id=$1::uuid),(SELECT max(n) FROM (SELECT count(*) n FROM nfo_policy_requests GROUP BY actor_id) q),(SELECT idempotency_key FROM nfo_policy_requests WHERE actor_id=$1::uuid AND idempotency_key LIKE 'last-slot-%') FROM nfo_policy_requests`, f.a.UserID).Scan(&total, &actorRows, &largestActor, &winner)
			if err != nil || largestActor > domain.NFORequestActorMax || (scope == "actor" && actorRows != domain.NFORequestActorMax) || (scope == "global" && (total != domain.NFORequestGlobalMax || actorRows != 1)) {
				t.Fatal("committed ledger exceeded its actor or global quota")
			}
			before := nfoPolicySnapshot(t, f)
			got, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, winner, p.Generation, p.Mode)
			if err != nil || !replay || got != p {
				t.Fatal("exact replay consumed quota at capacity")
			}
			nfoPolicyUnchanged(t, f, before)
		})
	}
}

func TestNFOPolicyExpiredExactKeyAndBoundedAtomicCleanup(t *testing.T) {
	f := newJobFixture(t)
	p := nfoPolicyRead(t, f)
	// Four fixture actors each have 64 expired rows; none violates actor quota.
	// The requested exact key is newer than all 256, outside the oldest prefix.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO users(name) SELECT 'nfo-expired-'||n FROM generate_series(1,4) n`); err != nil {
		t.Fatal("seed expiry fixture actors")
	}
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation,created_at,expires_at) SELECT u.id,'old-'||n,$1::uuid,'off',$2,$2,clock_timestamp()-interval '26 hours',clock_timestamp()-interval '2 hours' FROM users u CROSS JOIN generate_series(1,64) n WHERE u.name LIKE 'nfo-expired-%'`, p.LibraryID, p.Generation)
	if err != nil {
		t.Fatal("seed expired request prefix")
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_policy_requests(actor_id,idempotency_key,library_id,requested_mode,expected_generation,result_generation,created_at,expires_at) VALUES($1::uuid,'expired-exact',$2::uuid,'off',$3,$3,clock_timestamp()-interval '25 hours',clock_timestamp()-interval '1 hour')`, f.a.UserID, p.LibraryID, p.Generation)
	if err != nil {
		t.Fatal("seed expired exact key")
	}
	before := nfoPolicySnapshot(t, f)
	got, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "expired-exact", p.Generation+1, domain.NFOModeReadOnly)
	nfoPolicyFailure(t, got, replay, err, domain.ErrConflict)
	nfoPolicyUnchanged(t, f, before)
	got, replay, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "expired-exact", p.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || got.Generation != p.Generation+1 || got.Mode != domain.NFOModeReadOnly {
		t.Fatal("expired exact key did not permit a new request body")
	}
	var total, expired, audits int
	var fresh bool
	err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE expires_at<=clock_timestamp()),(SELECT expires_at>clock_timestamp() AND expires_at-created_at BETWEEN interval '24 hours' AND interval '24 hours 1 second' FROM nfo_policy_requests WHERE actor_id=$1::uuid AND idempotency_key='expired-exact'),(SELECT count(*) FROM audit_logs WHERE event='nfo.policy_changed') FROM nfo_policy_requests`, f.a.UserID).Scan(&total, &expired, &fresh, &audits)
	if err != nil || total != 257-domain.NFOSweepMax+1 || expired != 257-domain.NFOSweepMax || !fresh || audits != 1 {
		t.Fatal("admission cleanup did not count exact-key deletion inside its 128-row bound")
	}
}

func TestNFOPolicyLateSessionExpiryRollsBackMutation(t *testing.T) {
	f := newJobFixture(t)
	p := nfoPolicyRead(t, f)
	_, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_policy_expiry_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$; CREATE TRIGGER nfo_policy_expiry_delay AFTER INSERT ON nfo_policy_requests FOR EACH ROW EXECUTE FUNCTION nfo_policy_expiry_delay()`)
	if err != nil {
		t.Fatal("install private late-expiry trigger")
	}
	before := nfoPolicySnapshot(t, f)
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal("prepare short-lived administrator session")
	}
	started := time.Now()
	got, replay, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "late-expiry", p.Generation, domain.NFOModeReadOnly)
	if time.Since(started) < time.Second {
		t.Fatal("session was rejected before the late transaction trigger ran")
	}
	nfoPolicyFailure(t, got, replay, err, domain.ErrUnauthenticated)
	nfoPolicyUnchanged(t, f, before)
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_policy_expiry_delay ON nfo_policy_requests; UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE revoked_at IS NULL`); err != nil {
		t.Fatal("restore private session fixture")
	}
	got, replay, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "late-expiry", p.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || got.Generation != p.Generation+1 {
		t.Fatal("expired transaction stranded its key or generation")
	}
}

func TestNFOPolicyCancellationAfterWriteRollsBackAndJoins(t *testing.T) {
	f := newJobFixture(t)
	p := nfoPolicyRead(t, f)
	before := nfoPolicySnapshot(t, f)
	cancelled, stop := context.WithCancel(f.ctx)
	stop()
	got, err := f.s.GetNFOLibraryPolicy(cancelled, f.a, p.LibraryID)
	nfoPolicyFailure(t, got, false, err, context.Canceled)
	got, replay, err := f.s.SetNFOLibraryPolicy(cancelled, f.a, p.LibraryID, "cancel-before", p.Generation, domain.NFOModeReadOnly)
	nfoPolicyFailure(t, got, replay, err, context.Canceled)
	nfoPolicyUnchanged(t, f, before)
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION nfo_policy_hold_after_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema()),17481206); RETURN NEW; END $$; CREATE TRIGGER nfo_policy_hold_after_write AFTER INSERT ON nfo_policy_requests FOR EACH ROW EXECUTE FUNCTION nfo_policy_hold_after_write()`)
	if err != nil {
		t.Fatal("install private policy latch")
	}
	holder, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal("open private policy latch transaction")
	}
	defer holder.Rollback(f.ctx)
	if _, err = holder.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),17481206)`); err != nil {
		t.Fatal("hold private policy latch")
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	results := make(chan nfoPolicyOutcome, 1)
	go func() {
		p, r, e := f.s.SetNFOLibraryPolicy(ctx, f.a, p.LibraryID, "cancel-after", p.Generation, domain.NFOModeReadOnly)
		results <- nfoPolicyOutcome{p, r, e}
	}()
	joined := false
	defer func() {
		cancel()
		_ = holder.Rollback(f.ctx)
		if !joined {
			select {
			case <-results:
			case <-time.After(3 * time.Second):
				t.Error("cancelled policy writer failed to join after latch release")
			}
		}
	}()
	// The trigger asks for this lock after both policy and ledger writes. Server
	// lock observation fixes the cancellation point without timing a client sleep.
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext(current_schema())::oid AND objid=17481206)`).Scan(&waiting)
		if err != nil {
			t.Fatal("observe private policy latch")
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("policy write did not reach its transaction latch")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case r := <-results:
		joined = true
		nfoPolicyFailure(t, r.policy, r.replay, r.err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled policy write did not return")
	}
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal("release private policy latch")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER nfo_policy_hold_after_write ON nfo_policy_requests`); err != nil {
		t.Fatal("remove private policy latch")
	}
	nfoPolicyUnchanged(t, f, before)
	got, replay, err = f.s.SetNFOLibraryPolicy(f.ctx, f.a, p.LibraryID, "cancel-after", p.Generation, domain.NFOModeReadOnly)
	if err != nil || replay || got.Generation != p.Generation+1 {
		t.Fatal("cancelled policy transaction retained its key or database lock")
	}
}
