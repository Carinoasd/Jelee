package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pure SQL capacity recipes clone metadata and canonical receipts. They are not
// fresh native observations and are never passed to a filesystem writer.
func makeAttemptQuotaBatch(t *testing.T, f jobFixture, base domain.JobLease, prepared domain.NFOWritePreparation, batch, count, start int, sizes [][2]int) (domain.JobLease, []domain.NFOWriteCommitFilePlan, []string) {
	t.Helper()
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal("owned attempt quota transaction unavailable")
	}
	defer tx.Rollback(f.ctx)
	var job string
	err = tx.QueryRow(f.ctx, `INSERT INTO jobs SELECT (jsonb_populate_record(NULL::jobs,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key',$2::text,'state','running','owner','nfo-write-fixture','cancel_requested',false,'finished_at',NULL,'error_code','','lease_until',clock_timestamp()+interval '1 hour','started_at',clock_timestamp()))).* FROM jobs j WHERE id=$1::uuid RETURNING id::text`, base.Job.ID, fmt.Sprintf("attempt-quota-batch-%d", batch)).Scan(&job)
	if err != nil {
		t.Fatal("owned attempt quota job clone failed")
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO nfo_write_requests SELECT $1::uuid,library_id,generation,$3,intent_digest FROM nfo_write_requests WHERE job_id=$2::uuid`, job, base.Job.ID, count); err != nil {
		t.Fatal("owned quota request clone failed")
	}
	for sequence := 1; sequence <= count; sequence++ {
		ordinal := start + sequence
		originalSize, replacementSize := sizes[ordinal-1][0], sizes[ordinal-1][1]
		var item, source string
		if err = tx.QueryRow(f.ctx, `INSERT INTO items SELECT (jsonb_populate_record(NULL::items,to_jsonb(i)||jsonb_build_object('id',gen_random_uuid()))).* FROM items i WHERE id=$1::uuid RETURNING id::text`, prepared.Scope.ItemID).Scan(&item); err != nil {
			t.Fatal("owned quota item clone failed")
		}
		media, nfo := fmt.Sprintf("attempt-quota-%d.mkv", ordinal), fmt.Sprintf("attempt-quota-%d.nfo", ordinal)
		if err = tx.QueryRow(f.ctx, `INSERT INTO media_sources SELECT (jsonb_populate_record(NULL::media_sources,to_jsonb(m)||jsonb_build_object('id',gen_random_uuid(),'item_id',$2::uuid,'relative_path',$3::text))).* FROM media_sources m WHERE id=$1::uuid RETURNING id::text`, prepared.Scope.SourceID, item, media).Scan(&source); err != nil {
			t.Fatal("owned quota media clone failed")
		}
		request := domain.CloneNFOWritePrepareRequest(prepared.Request)
		request.ItemID = item
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal("owned canonical quota request encode failed")
		}
		var preparation string
		err = tx.QueryRow(f.ctx, `WITH recipe AS MATERIALIZED(SELECT p.*,$9::bytea AS encoded,
   CASE WHEN $6::integer=0 THEN original_bytes ELSE convert_to(rpad('<movie/>',$6::integer,' '),'UTF8') END AS original,
   CASE WHEN $7::integer=0 THEN replacement_bytes ELSE convert_to(rpad('<movie/>',$7::integer,' '),'UTF8') END AS replacement,
   overlay(overlay(native_receipt placing decode(lpad(to_hex($8::bigint*2+10000),16,'0'),'hex') from 73 for 8) placing decode(lpad(to_hex($8::bigint*2+10001),16,'0'),'hex') from 121 for 8) AS receipt
   FROM nfo_write_preparations p WHERE id=$1::uuid)
   INSERT INTO nfo_write_preparations SELECT(jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'idempotency_key','attempt-quota-preparation-'||$8::text,'item_id',$2::uuid,'source_id',$3::uuid,'relative_path',$4::text,'media_path',$5::text,'request_bytes',encoded,'request_digest',encode(sha256(encoded),'hex'),'original_bytes',original,'original_sha256',encode(sha256(original),'hex'),'replacement_bytes',replacement,'replacement_sha256',encode(sha256(replacement),'hex'),'native_receipt',receipt))).* FROM recipe p RETURNING id::text`, prepared.ID, item, source, nfo, media, originalSize, replacementSize, ordinal, encoded).Scan(&preparation)
		if err != nil {
			t.Fatal("owned quota matching preparation clone failed")
		}
		tag, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt)
 SELECT $1::uuid,$2,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,native_receipt FROM nfo_write_preparations WHERE id=$3::uuid AND actor_id=$4::uuid AND library_id=$5::uuid AND generation=$6 AND root_generation>0 AND native_receipt IS NOT NULL AND expires_at>clock_timestamp()`, job, sequence, preparation, f.a.UserID, prepared.Scope.LibraryID, prepared.Scope.Generation)
		if err != nil || tag.RowsAffected() != 1 {
			var denied *pgconn.PgError
			if errors.As(err, &denied) {
				t.Logf("owned entry sqlstate=%s constraint=%s", denied.Code, denied.ConstraintName)
			}
			t.Fatal("owned bounded quota entry clone failed")
		}
		if _, err = tx.Exec(f.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, preparation); err != nil {
			t.Fatal("owned quota preparation cleanup failed")
		}
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal("owned complete quota batch failed")
	}
	var lease domain.JobLease
	lease, err = scanLease(f.s.Pool.QueryRow(f.ctx, `SELECT `+leaseColumns+` FROM jobs WHERE id=$1::uuid`, job))
	if err != nil {
		t.Fatal("owned quota lease read failed")
	}
	plans := make([]domain.NFOWriteCommitFilePlan, count)
	tokens := make([]string, count)
	for sequence := 1; sequence <= count; sequence++ {
		task, err := f.s.GetNFOWriteTask(f.ctx, lease, sequence)
		if err != nil {
			t.Fatal("owned quota task read failed")
		}
		r, err := f.s.BeginNFOWriteCommit(f.ctx, lease, sequence)
		if err != nil {
			t.Fatal("owned quota journal failed")
		}
		plans[sequence-1], tokens[sequence-1] = commitPlanFixture(task.Preparation), r.Token
	}
	return lease, plans, tokens
}
func stopAttemptQuotaJob(t *testing.T, f jobFixture, lease domain.JobLease) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET state='cancelled',owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),cancel_requested=true WHERE id=$1::uuid`, lease.Job.ID); err != nil {
		t.Fatal("owned quota job stop failed")
	}
}
func insertAttemptQuotaPlan(t *testing.T, f jobFixture, tx pgx.Tx, token string, plan domain.NFOWriteCommitFilePlan) error {
	t.Helper()
	_, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,$2,$3,$4,$5)`, token, plan.Version, plan.TargetName, plan.ParentIdentity[:], plan.TargetIdentity[:])
	return err
}

// attemptQuotaOtherLibrary builds a second library, item, preparation and queued
// job through the ordinary fixtures. Its candidate shares no job, library, root,
// item or catalog row with the seeded library, so only the global fence can
// serialize the two final-slot plans.
func attemptQuotaOtherLibrary(t *testing.T, f jobFixture) (jobFixture, domain.Job, domain.NFOWritePreparation) {
	t.Helper()
	r, err := f.s.RegisterLibrary(f.ctx, "attempt-quota-other", t.TempDir())
	if err != nil {
		t.Fatal("owned quota second library unavailable")
	}
	g := f
	g.registration = r
	service, _, request := nfoWritePreparationLibrary(t, g)
	prepared, _, err := service.Prepare(g.ctx, g.a, "attempt-quota-other", request)
	if err != nil || prepared.Scope.LibraryID != r.Library.ID || prepared.Scope.LibraryID == f.registration.Library.ID {
		t.Fatal("owned quota second preparation unavailable")
	}
	j := nfoWriteJobFixture(t, g, prepared, "attempt-quota-other-job", domain.JobPriorityManual)
	if _, err = g.s.Pool.Exec(g.ctx, `DELETE FROM nfo_write_preparations WHERE id=$1::uuid`, prepared.ID); err != nil {
		t.Fatal("owned quota second preparation cleanup failed")
	}
	return g, j, prepared
}
func attemptQuotaOtherCandidate(t *testing.T, g jobFixture, j domain.Job) (domain.JobLease, domain.NFOWriteCommitFilePlan, string) {
	t.Helper()
	l := nfoWriteLeaseFixture(t, g, j.ID)
	task, err := g.s.GetNFOWriteTask(g.ctx, l, 1)
	if err != nil {
		t.Fatal("owned quota second task read failed")
	}
	r, err := g.s.BeginNFOWriteCommit(g.ctx, l, 1)
	if err != nil {
		t.Fatal("owned quota second journal failed")
	}
	return l, commitPlanFixture(task.Preparation), r.Token
}
func attemptQuotaFiller(t *testing.T, retained int64) [2]int {
	t.Helper()
	for original := int64(8); original <= 8<<20; original++ {
		rest := retained/4 - 3*original
		if retained%4 == 0 && rest%2 == 0 && rest/2 >= 8 && rest/2 <= 8<<20 && domain.NFOWriteCommitAttemptRetainedBytes(original, rest/2) == retained {
			return [2]int{int(original), int(rest / 2)}
		}
	}
	t.Fatal("owned exact filler recipe invalid")
	return [2]int{}
}
func disableAttemptQuotaFence(t *testing.T, f jobFixture) {
	t.Helper()
	var schema string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT current_schema()`).Scan(&schema); err != nil || schema == "public" {
		t.Fatal("owned quota schema unavailable")
	}
	name := pgx.Identifier{schema, "fence_nfo_commit_attempt_quota"}.Sanitize()
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE OR REPLACE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$ BEGIN RETURN NEW; END $$`); err != nil {
		t.Fatal("owned quota fence overlay failed")
	}
}

func TestNFOCommitAttemptGlobalQuotaSnapshots(t *testing.T) {
	for _, kind := range []string{"rows", "bytes"} {
		for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
			t.Run(kind+"/"+string(isolation), func(t *testing.T) { attemptQuotaRace(t, kind, isolation, false) })
		}
		// Causality control: the same independent candidates, with only the owned
		// schema's fence made a no-op, must let a stale snapshot exceed the bound.
		t.Run(kind+"/fence-disabled/"+string(pgx.RepeatableRead), func(t *testing.T) { attemptQuotaRace(t, kind, pgx.RepeatableRead, true) })
	}
}

func attemptQuotaRace(t *testing.T, kind string, isolation pgx.TxIsoLevel, fenceDisabled bool) {
	f, base, prepared, _, initial := commitAttemptFixture(t)
	stopAttemptQuotaJob(t, f, base)
	g, otherJob, other := attemptQuotaOtherLibrary(t, f)
	otherBytes := domain.NFOWriteCommitAttemptRetainedBytes(int64(len(other.Original)), int64(len(other.Replacement)))
	capBytes := int64(1073741824)
	// The first candidate is the last entry of the seeded library; the second is
	// the other library. Both fit alone and exactly one fits together.
	seedCount := 254
	sizes := make([][2]int, seedCount+1)
	if kind == "bytes" {
		per := domain.NFOWriteCommitAttemptRetainedBytes(2<<20, 2<<20)
		room := capBytes - initial.RetainedBytes - otherBytes
		full := room / per
		filler := room - full*per
		if filler != 0 && filler < 160 {
			full--
			filler += per
		}
		seedCount = int(full)
		sizes = make([][2]int, 0, seedCount+2)
		for i := 0; i < seedCount; i++ {
			sizes = append(sizes, [2]int{2 << 20, 2 << 20})
		}
		if filler != 0 {
			sizes = append(sizes, attemptQuotaFiller(t, filler))
			seedCount++
		}
		sizes = append(sizes, [2]int{len(other.Original), len(other.Replacement)})
	}
	var firstPlan domain.NFOWriteCommitFilePlan
	var firstToken string
	remaining, total, batch := seedCount+1, 0, 0
	for remaining > 0 {
		count := min(remaining, 100)
		batch++
		lease, plans, tokens := makeAttemptQuotaBatch(t, f, base, prepared, batch, count, total, sizes)
		for i := 0; i < count; i++ {
			if total+i < seedCount {
				if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, i+1, tokens[i], plans[i]); err != nil {
					t.Fatal("owned below-bound plan refused")
				}
			} else {
				firstPlan, firstToken = plans[i], tokens[i]
			}
		}
		remaining -= count
		total += count
		if remaining > 0 {
			stopAttemptQuotaJob(t, f, lease)
		}
	}
	if firstToken == "" {
		t.Fatal("owned final-slot candidates missing")
	}
	_, secondPlan, secondToken := attemptQuotaOtherCandidate(t, g, otherJob)
	var shared int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal a JOIN nfo_write_commit_journal b ON b.token=$2::uuid JOIN jobs ja ON ja.id=a.job_id JOIN jobs jb ON jb.id=b.job_id
 JOIN nfo_write_entries ea ON ea.job_id=a.job_id AND ea.sequence=a.sequence JOIN nfo_write_entries eb ON eb.job_id=b.job_id AND eb.sequence=b.sequence
 WHERE a.token=$1::uuid AND (ja.id=jb.id OR ja.library_id=jb.library_id OR ea.root_id=eb.root_id OR ea.item_id=eb.item_id OR ea.source_id=eb.source_id OR ja.state<>'running' OR jb.state<>'running')`, firstToken, secondToken).Scan(&shared); err != nil || shared != 0 {
		t.Fatal("owned quota candidates are not independent live scopes")
	}
	if fenceDisabled {
		disableAttemptQuotaFence(t, f)
	}
	first, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		t.Fatal("owned quota first snapshot unavailable")
	}
	defer first.Rollback(f.ctx)
	second, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		t.Fatal("owned quota second snapshot unavailable")
	}
	defer second.Rollback(f.ctx)
	for _, tx := range []pgx.Tx{first, second} {
		var rows int
		var used int64
		if err = tx.QueryRow(f.ctx, `SELECT count(*),COALESCE(sum(retained_bytes),0) FROM nfo_write_commit_attempt_reservations`).Scan(&rows, &used); err != nil || rows != seedCount+1 || (kind == "rows" && rows != 255) || (kind == "bytes" && used != capBytes-otherBytes) {
			t.Fatal("owned quota boundary masked by another limit")
		}
	}
	if err = insertAttemptQuotaPlan(t, f, first, firstToken, firstPlan); err != nil {
		t.Fatal("first exact-slot plan refused")
	}
	if err = first.Commit(f.ctx); err != nil {
		t.Fatal("first exact-slot commit refused")
	}
	err = insertAttemptQuotaPlan(t, g, second, secondToken, secondPlan)
	if err == nil {
		err = second.Commit(f.ctx)
	}
	var rows, partial int
	var used int64
	count := func() {
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),COALESCE(sum(retained_bytes),0),(SELECT count(*) FROM nfo_write_commit_file_plans WHERE token=$1::uuid) FROM nfo_write_commit_attempt_reservations`, secondToken).Scan(&rows, &used, &partial); err != nil {
			t.Fatal("owned quota totals unavailable")
		}
	}
	if fenceDisabled {
		count()
		if err != nil || rows != seedCount+3 || partial != 1 || (kind == "bytes" && used != capBytes+otherBytes) {
			t.Fatal("fence-disabled control did not admit stale candidate")
		}
		return
	}
	if err == nil {
		t.Fatal("stale attempt capacity admitted")
	}
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || !((failure.Code == "23514" && failure.Message == "nfo attempt capacity reached") || (isolation != pgx.ReadCommitted && failure.Code == "40001")) {
		if failure != nil {
			t.Logf("unrelated quota refusal sqlstate=%s constraint=%s", failure.Code, failure.ConstraintName)
		}
		t.Fatal("unrelated attempt capacity refusal")
	}
	_ = second.Rollback(f.ctx)
	count()
	if rows != seedCount+2 || partial != 0 || (kind == "rows" && rows != 256) || (kind == "bytes" && used != capBytes) {
		t.Fatal("excess or partial attempt reservation retained")
	}
}

// Historical schema55 plans already retain their legacy names. An upgrade must
// charge them before admitting a new reservation and, when they exceed the
// global budget, refuse atomically without deleting or adopting any history.
func TestNFOCommitAttemptMigrationRefusesHistoricalExcess(t *testing.T) {
	f, base, prepared := nfoCommitFixture(t)
	stopAttemptQuotaJob(t, f, base)
	jobMetricMigration(t, f, "down", 55)
	const count = 7
	sizes := make([][2]int, count)
	for i := range sizes {
		sizes[i] = [2]int{8 << 20, 8 << 20}
	}
	if int64(count)*domain.NFOWriteCommitAttemptRetainedBytes(8<<20, 8<<20) <= 1073741824 {
		t.Fatal("owned historical excess recipe invalid")
	}
	lease, plans, tokens := makeAttemptQuotaBatch(t, f, base, prepared, 1, count, 0, sizes)
	for i := range plans {
		if _, err := f.s.SaveNFOWriteCommitFilePlan(f.ctx, lease, i+1, tokens[i], plans[i]); err != nil {
			t.Fatal("owned historical plan refused")
		}
	}
	stopAttemptQuotaJob(t, f, lease)
	history := func() string {
		var digest string
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT md5(COALESCE(string_agg(token::text||recorded_at::text||target_name,',' ORDER BY token),'')) FROM nfo_write_commit_file_plans`).Scan(&digest); err != nil {
			t.Fatal("owned historical plans unavailable")
		}
		return digest
	}
	before := history()
	if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "up"); err == nil {
		t.Fatal("historical excess upgraded")
	}
	var present bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('nfo_write_commit_attempt_reservations') IS NOT NULL OR to_regclass('nfo_commit_attempt_quota_fence') IS NOT NULL`).Scan(&present); err != nil || present {
		t.Fatal("refused upgrade left partial attempt storage")
	}
	if history() != before {
		t.Fatal("refused upgrade changed historical plans")
	}
	// The migrator marks the refused version dirty; the operator must resolve it
	// explicitly, and no schema56 object exists in the meantime.
	if version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status"); err != nil || version != 56 || !dirty {
		t.Fatal("refused upgrade did not stay dirty at the refused version")
	}
}
