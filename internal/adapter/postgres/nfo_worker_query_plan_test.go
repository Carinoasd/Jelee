package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoWorkerPlanNode struct {
	Type     string              `json:"Node Type"`
	Relation string              `json:"Relation Name"`
	Index    string              `json:"Index Name"`
	Rows     float64             `json:"Actual Rows"`
	Loops    float64             `json:"Actual Loops"`
	Removed  float64             `json:"Rows Removed by Filter"`
	Plans    []nfoWorkerPlanNode `json:"Plans"`
}

func nfoWorkerPlan(t *testing.T, f jobFixture, query string, args ...any) nfoWorkerPlanNode {
	t.Helper()
	var raw []byte
	if err := f.s.Pool.QueryRow(f.ctx, "EXPLAIN(ANALYZE,FORMAT JSON,COSTS false) "+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var result []struct{ Plan nfoWorkerPlanNode }
	if json.Unmarshal(raw, &result) != nil || len(result) != 1 {
		t.Fatal("invalid query plan")
	}
	return result[0].Plan
}
func walkNFOWorkerPlan(p nfoWorkerPlanNode, visit func(nfoWorkerPlanNode)) {
	visit(p)
	for _, child := range p.Plans {
		walkNFOWorkerPlan(child, visit)
	}
}

func TestNFOCurrentObservationPlanUsesScopedCursorIndex(t *testing.T) {
	f := newNFOQueryFixture(t)
	identity := domain.DefaultNFOIdentity()
	digest, err := domain.NFOIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.s.GetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy, _, err := f.s.SetNFOLibraryPolicy(f.ctx, f.a, f.registration.Library.ID, "query-plan-policy", before.Generation, domain.NFOModeReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	// These generated rows exist only in the disposable schema. All 5,000 rows
	// of this library match the current scope; another 5,000 belong elsewhere.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE nfo_cache SET identity_digest=$2,library_generation=$3,expires_at=clock_timestamp()+interval '1 day' WHERE library_id=$1::uuid`, f.registration.Library.ID, probeBytes(digest), policy.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, "ANALYZE nfo_cache; ANALYZE library_roots"); err != nil {
		t.Fatal(err)
	}
	page, err := f.s.ListNFOObservations(f.ctx, f.a, f.registration.Library.ID, "", 20, identity)
	if err != nil || len(page.Items) != 20 || page.NextCursor != page.Items[19].ID {
		t.Fatal("first page", err)
	}
	later, err := f.s.ListNFOObservations(f.ctx, f.a, f.registration.Library.ID, page.NextCursor, 20, identity)
	if err != nil || len(later.Items) != 20 || later.Items[0].ID <= page.NextCursor {
		t.Fatal("cursor did not advance", err)
	}
	plan := nfoWorkerPlan(t, f, nfoCurrentObservationSQL, f.registration.Library.ID, probeBytes(digest), policy.Generation, time.Now(), page.NextCursor, 21)
	if plan.Type != "Limit" || plan.Rows != 21 {
		t.Fatal("query did not return bounded prefix")
	}
	visited := float64(0)
	indexed := false
	walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
		if p.Relation == "nfo_cache" {
			visited += (p.Rows + p.Removed) * p.Loops
			indexed = p.Index == "nfo_cache_current_idx" || p.Index == "nfo_cache_observation_idx"
		}
	})
	if !indexed || visited > 21 {
		t.Fatalf("matching scope did not use cursor prefix: indexed=%t visited=%g", indexed, visited)
	}
	t.Logf("Current observations: 10,000 cache rows / 2 libraries; matching current-scope page visited %g cache rows for limit 21. Expired/root-stale rows may require more visits, bounded by cache quota and DB deadline.", visited)
}
func TestImageComparisonQueriesAggregateBoundedInventories(t *testing.T) {
	f := newJobFixture(t)
	j := f.submit(t, "image-aggregate-plan")
	var epoch int64
	if err := f.s.Pool.QueryRow(f.ctx, "SELECT inventory_generation FROM jobs WHERE id=$1::uuid", j.ID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline(library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation)
 SELECT $1::uuid,$2::uuid,'image-'||n::text||'.jpg',true,'image',7,123456789,$3 FROM generate_series(1,10000)n`, f.registration.Library.ID, f.registration.RootID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_inventory(job_id,root_id,parent_path,path,kind,size,modified_unix_nano)
 SELECT $1::uuid,$2::uuid,'.','image-'||n::text||'.jpg','image',CASE WHEN n<=1000 THEN 8 ELSE 7 END,123456789 FROM generate_series(1,10100)n WHERE n<=9500 OR n>10000`, j.ID, f.registration.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, "ANALYZE job_inventory; ANALYZE library_inventory_baseline"); err != nil {
		t.Fatal(err)
	}
	var added, changed, unchanged, uncompared int64
	if err = f.s.Pool.QueryRow(f.ctx, imageCurrentCountsSQL, f.registration.Library.ID, j.ID, epoch).Scan(&added, &changed, &unchanged, &uncompared); err != nil || added != 100 || changed != 1000 || unchanged != 8500 || uncompared != 0 {
		t.Fatalf("current counts %d/%d/%d/%d %v", added, changed, unchanged, uncompared, err)
	}
	var total, unknown, missing int64
	if err = f.s.Pool.QueryRow(f.ctx, imageMissingCountsSQL, f.registration.Library.ID, j.ID, epoch).Scan(&total, &unknown, &missing); err != nil || total != 10000 || unknown != 0 || missing != 500 {
		t.Fatalf("missing counts %d/%d/%d %v", total, unknown, missing, err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, inventoryMissingCountsSQL, f.registration.Library.ID, j.ID, epoch).Scan(&total, &unknown, &missing); err != nil || total != 10000 || unknown != 0 || missing != 500 {
		t.Fatalf("general missing counts %d/%d/%d %v", total, unknown, missing, err)
	}
	for name, query := range map[string]string{"current": imageCurrentCountsSQL, "missing": imageMissingCountsSQL, "general-missing": inventoryMissingCountsSQL} {
		plan := nfoWorkerPlan(t, f, query, f.registration.Library.ID, j.ID, epoch)
		if plan.Type != "Aggregate" || plan.Rows != 1 {
			t.Fatal("comparison did not aggregate in SQL")
		}
		visited := float64(0)
		walkNFOWorkerPlan(plan, func(p nfoWorkerPlanNode) {
			if p.Relation == "job_inventory" || p.Relation == "library_inventory_baseline" {
				visited += (p.Rows + p.Removed) * p.Loops
			}
		})
		if visited > 3*(10000+9600) {
			t.Fatalf("comparison revisited inventories excessively: %g", visited)
		}
		t.Logf("Image %s: 10,000 baseline / 9,600 current rows, %g relation rows visited; one aggregate returned. This is a single bounded fixture, not a constant-time or maximum-library performance claim.", name, visited)
	}
}
