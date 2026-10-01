package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOInventoryPageSkipsLargeNonNFOInventory(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "nfo-kind-index")
	// Candidate UUIDs sort after all 10,000 non-NFO rows. A plain job/id index
	// would have to visit every non-NFO entry before returning its first NFO.
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_inventory(id,job_id,root_id,parent_path,path,kind,size,modified_unix_nano)
SELECT ('00000000-0000-4000-8000-'||lpad(n::text,12,'0'))::uuid,$1::uuid,$2::uuid,'.','image-'||n::text||'.jpg','image',7,123456789 FROM generate_series(1,10000) n
UNION ALL
SELECT ('ffffffff-ffff-4fff-8fff-'||lpad(n::text,12,'0'))::uuid,$1::uuid,$2::uuid,'.','candidate-'||n::text||'.nfo','nfo',7,123456789 FROM generate_series(1,64) n`, j.ID, f.registration.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `ANALYZE job_inventory; ANALYZE library_roots`); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = f.s.Pool.QueryRow(f.ctx, `EXPLAIN(ANALYZE,FORMAT JSON,COSTS false) `+nfoPrefixSQL, j.ID, f.registration.Library.ID, "", domain.NFOPageMax).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type node struct {
		Relation string `json:"Relation Name"`
		Index    string `json:"Index Name"`
		Rows     int    `json:"Actual Rows"`
		Removed  int    `json:"Rows Removed by Filter"`
		Plans    []node `json:"Plans"`
	}
	var outer []struct{ Plan node }
	if err = json.Unmarshal(raw, &outer); err != nil || len(outer) != 1 {
		t.Fatal("invalid NFO inventory query plan", err)
	}
	seen := false
	var walk func(node)
	walk = func(p node) {
		if p.Relation == "job_inventory" {
			seen = true
			if p.Index != "job_inventory_nfo_page_idx" || p.Rows+p.Removed > domain.NFOPageMax {
				t.Fatalf("NFO page scanned unrelated inventory: index=%s visited=%d", p.Index, p.Rows+p.Removed)
			}
			t.Logf("NFO inventory partial index visited %d rows with 10000 non-NFO rows", p.Rows+p.Removed)
		}
		for _, child := range p.Plans {
			walk(child)
		}
	}
	walk(outer[0].Plan)
	if !seen {
		t.Fatal("NFO inventory plan omitted candidate relation")
	}
	tx, err := f.s.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	entries, err := nfoPrefix(f.ctx, tx, domain.NFOPhase{JobID: j.ID, LibraryID: f.registration.Library.ID}, domain.NFOPageMax)
	if err != nil || len(entries) != domain.NFOPageMax {
		t.Fatal("NFO page result count", err)
	}
	for _, entry := range entries {
		if entry.Inventory.Kind != "nfo" {
			t.Fatal("NFO page contained a different candidate kind")
		}
	}
}

func TestNFOCacheIndexedMaintenanceAndExactQuota(t *testing.T) {
	f := newNFOQueryFixture(t)
	for name, query := range map[string]string{
		"global_expired": nfoExpiredGlobalSQL, "global_lru": nfoLRUGlobalSQL,
		"library_expired": nfoExpiredLibrarySQL, "library_lru": nfoLRULibrarySQL,
	} {
		t.Run(name, func(t *testing.T) {
			args := []any{domain.NFOSweepMax}
			if name == "library_expired" || name == "library_lru" {
				args = []any{f.registration.Library.ID, domain.NFOSweepMax}
			}
			assertNFOBoundedPlan(t, f, query, args...)
		})
	}
	beforeBytes := assertNFOPlanQuota(t, f, 10000)
	for _, badLimit := range []int{0, domain.NFOSweepMax + 1} {
		result, err := f.s.SweepNFOCache(f.ctx, badLimit)
		if !errors.Is(err, domain.ErrInvalid) || result != (domain.NFOSweepResult{}) {
			t.Fatal("invalid maintenance bound produced effects", err)
		}
	}
	if assertNFOPlanQuota(t, f, 10000) != beforeBytes {
		t.Fatal("invalid sweep changed quota")
	}
	result, err := f.s.SweepNFOCache(f.ctx, domain.NFOSweepMax)
	if err != nil || result.Deleted != domain.NFOSweepMax || result.DeletedRequests != 0 {
		t.Fatalf("sweep did not preserve bounded cache prefix: %+v %v", result, err)
	}
	afterBytes := assertNFOPlanQuota(t, f, 10000-domain.NFOSweepMax)
	if result.FreedBytes != beforeBytes-afterBytes || result.FreedBytes <= 0 {
		t.Fatal("sweep reported an inexact byte delta")
	}
	var expired int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_cache WHERE expires_at<=clock_timestamp()`).Scan(&expired); err != nil || expired != 5000-domain.NFOSweepMax {
		t.Fatal("sweep deleted nonexpired rows or exceeded its prefix", err)
	}
}

// Large rows belong only to the disposable integration schema. The measured
// queries below are the repository's actual maintenance queries, without planner
// hints or disabling sequential scans to manufacture a passing plan.
func newNFOQueryFixture(t *testing.T) jobFixture {
	t.Helper()
	f := newJobFixture(t)
	p := domain.DefaultNFOCachePolicy()
	p.MaxRows, p.LibraryMaxRows = 10000, 5000
	if err := f.s.EnsureNFOCachePolicy(f.ctx, p); err != nil {
		t.Fatal("initialize NFO plan policy", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "nfo-plan-other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, library := range []domain.LibraryRegistration{f.registration, other} {
		if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM nfo_cache_quota`, library.Library.ID); err != nil {
			t.Fatal(err)
		}
		_, err = f.s.Pool.Exec(f.ctx, `WITH body AS (
 SELECT '{"schemaVersion":1,"status":"valid","encoding":"UTF-8","encodingGuessed":false,"root":"movie","entries":1,"failureCode":"","warningCount":0,"errorCount":0,"issueCount":0,"issuesTruncated":false,"issues":[]}'::jsonb AS summary
)
INSERT INTO nfo_cache(root_id,relative_path,library_id,size,modified_unix_nano,source_sha256,fingerprint_version,identity_digest,library_generation,root_generation,status,summary,expires_at,charge_bytes,last_used_at)
SELECT r.id,'fixture-'||lpad(n::text,6,'0')||'.nfo',r.library_id,7,123456789,decode(repeat('a',64),'hex'),'sha256-full-v1',decode(repeat('b',64),'hex'),lib.nfo_generation,r.nfo_generation,'valid',b.summary,
CASE WHEN n<=2500 THEN clock_timestamp()-interval '1 day' ELSE clock_timestamp()+interval '1 day' END-$2*interval '1 hour',2048+octet_length(b.summary::text),clock_timestamp()-$2*interval '1 hour'
FROM library_roots r JOIN libraries lib ON lib.id=r.library_id CROSS JOIN generate_series(1,5000) n CROSS JOIN body b WHERE r.id=$1::uuid`, library.RootID, i)
		if err != nil {
			t.Fatal("seed bounded NFO plan fixture", err)
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache_quota SET library_scopes=2,rows_used=10000,bytes_used=(SELECT sum(charge_bytes) FROM nfo_cache);
UPDATE nfo_library_quota q SET rows_used=5000,bytes_used=(SELECT sum(charge_bytes) FROM nfo_cache c WHERE c.library_id=q.library_id);
ANALYZE nfo_cache`); err != nil {
		t.Fatal(err)
	}
	assertNFOPlanQuota(t, f, 10000)
	return f
}

func assertNFOPlanQuota(t *testing.T, f jobFixture, expectedRows int64) int64 {
	t.Helper()
	var actualRows, actualBytes, chargedRows, chargedBytes, scopes, actualScopes int64
	err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM nfo_cache),COALESCE((SELECT sum(charge_bytes) FROM nfo_cache),0),rows_used,bytes_used,library_scopes,(SELECT count(*) FROM nfo_library_quota) FROM nfo_cache_quota`).Scan(&actualRows, &actualBytes, &chargedRows, &chargedBytes, &scopes, &actualScopes)
	if err != nil || actualRows != expectedRows || chargedRows != actualRows || chargedBytes != actualBytes || scopes != actualScopes {
		t.Fatalf("NFO global quota mismatch: rows=%d/%d expected=%d bytes=%d/%d scopes=%d/%d err=%v", actualRows, chargedRows, expectedRows, actualBytes, chargedBytes, scopes, actualScopes, err)
	}
	var mismatch bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM nfo_library_quota q WHERE q.rows_used<>(SELECT count(*) FROM nfo_cache c WHERE c.library_id=q.library_id) OR q.bytes_used<>COALESCE((SELECT sum(charge_bytes) FROM nfo_cache c WHERE c.library_id=q.library_id),0))`).Scan(&mismatch); err != nil || mismatch {
		t.Fatal("NFO library quota mismatch", err)
	}
	return actualBytes
}

func assertNFOBoundedPlan(t *testing.T, f jobFixture, query string, args ...any) {
	t.Helper()
	var raw []byte
	if err := f.s.Pool.QueryRow(f.ctx, `EXPLAIN(ANALYZE,FORMAT JSON,COSTS false) `+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		NodeType string     `json:"Node Type"`
		Index    string     `json:"Index Name"`
		Rows     int        `json:"Actual Rows"`
		Removed  int        `json:"Rows Removed by Filter"`
		Plans    []planNode `json:"Plans"`
	}
	var outer []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer) != 1 {
		t.Fatal("invalid NFO EXPLAIN result", err)
	}
	indexed := false
	var visit func(planNode)
	visit = func(p planNode) {
		if p.NodeType == "Sort" || p.NodeType == "Seq Scan" {
			t.Fatal("NFO cache page sorted/scanned the full fixture")
		}
		if p.Index != "" {
			indexed = true
			if p.Rows+p.Removed > domain.NFOSweepMax+1 {
				t.Fatalf("NFO index page visited %d rows", p.Rows+p.Removed)
			}
			t.Logf("%s %s visited %d rows", p.NodeType, p.Index, p.Rows+p.Removed)
		}
		for _, child := range p.Plans {
			visit(child)
		}
	}
	visit(outer[0].Plan)
	if !indexed {
		t.Fatal("NFO cache page did not use an index")
	}
}
