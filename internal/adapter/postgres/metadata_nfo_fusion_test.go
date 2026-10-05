package postgres

import (
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOFusionMixedSourcesOneRevisionAndAudit(t *testing.T) {
	f, scope, nfo := nfoItemApplyFixture(t)
	empty := ""
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 1, []domain.ItemMetadataPatch{{Field: "overview", Value: &empty}}); err != nil {
		t.Fatal(err)
	}
	scope.Revision = 2
	nfo.Fields = []domain.NFOTextField{nfo.Fields[0], nfo.Fields[3]}
	nfo.LockedFields = []string{"Name"}
	result, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true))
	if err != nil || result.Metadata.Revision != 3 || len(result.Applied) != 3 || len(result.Skipped) != 1 || result.NFO == nil || result.TMDB == nil || len(result.NFO.Applied) != 2 || len(result.TMDB.Applied) != 1 {
		t.Fatal("mixed fusion result differs", err, result)
	}
	fields := result.Metadata.Fields
	if fields[0].Source != "nfo" || fields[0].Value != "NFO title" || !fields[0].NFOOrigin.Locked || fields[1].Source != "tmdb" || fields[1].ProviderOrigin.ProviderID != 12 || fields[2].Source != "manual" || fields[2].Value != "" || fields[3].Source != "nfo" {
		t.Fatal("manual/NFO/TMDB priority differs")
	}
	var total, local, provider int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE event='item.nfo_metadata_applied'),count(*) FILTER(WHERE event='item.tmdb_metadata_applied') FROM audit_logs WHERE target_id=$1::uuid AND event IN ('item.nfo_metadata_applied','item.tmdb_metadata_applied')`, scope.ItemID).Scan(&total, &local, &provider); err != nil || total != 1 || local != 0 || provider != 1 {
		t.Fatal("fusion used separate audits", err, total, local, provider)
	}
	if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, scope.ItemID); err != nil || catalog.Title != "NFO title" {
		t.Fatal("fusion catalog differs", err)
	}
	if _, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true)); err != domain.ErrConflict {
		t.Fatal("stale fusion committed", err)
	}
	clone := domain.CloneMetadataApplyResult(result)
	clone.NFO.Applied[0] = "caller"
	clone.TMDB.Skipped[0].Reason = "caller"
	if result.NFO.Applied[0] == "caller" || result.TMDB.Skipped[0].Reason == "caller" {
		t.Fatal("source report ownership leaked")
	}
}

func TestNFOFusionProviderAndAuditFailureRollBackBothSources(t *testing.T) {
	for _, failure := range []string{"provider-field", "audit"} {
		t.Run(failure, func(t *testing.T) {
			f, scope, nfo := nfoItemApplyFixture(t)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='HomeVideo' WHERE id=$1::uuid`, scope.ItemID); err != nil {
				t.Fatal(err)
			}
			scope.Kind = "HomeVideo"
			nfo.Fields = []domain.NFOTextField{nfo.Fields[0], nfo.Fields[3]}
			before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if err != nil {
				t.Fatal(err)
			}
			ddl := `CREATE FUNCTION reject_fusion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.field='originalTitle' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_fusion BEFORE INSERT ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION reject_fusion()`
			if failure == "audit" {
				ddl = `CREATE FUNCTION reject_fusion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event='item.tmdb_metadata_applied' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_fusion BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_fusion()`
			}
			if _, err := f.s.Pool.Exec(f.ctx, ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true)); err == nil {
				t.Fatal("fusion failure accepted")
			}
			after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("partial NFO/provider/catalog/revision/kind survived", err)
			}
			var audit int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1::uuid AND event='item.tmdb_metadata_applied'`, scope.ItemID).Scan(&audit); err != nil || audit != 0 {
				t.Fatal("failed fusion left audit", err)
			}
		})
	}
}

func TestNFOFusionAllProtectedReviewAndFinalScopeChecks(t *testing.T) {
	f, scope, nfo := nfoItemApplyFixture(t)
	patches := []domain.ItemMetadataPatch{}
	for _, field := range nfo.Fields {
		value := field.Value
		patches = append(patches, domain.ItemMetadataPatch{Field: field.Field, Value: &value})
	}
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 1, patches); err != nil {
		t.Fatal(err)
	}
	scope.Revision = 2
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true))
	if err != nil || result.Metadata.Revision != 3 || len(result.Applied) != 0 || len(result.Skipped) != 4 || len(result.NFO.Skipped) != 4 || len(result.TMDB.Skipped) != 4 || !reflect.DeepEqual(before.Fields, result.Metadata.Fields) {
		t.Fatal("all-protected review differs", err)
	}
	scope.Revision = 3
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true)); err != domain.ErrConflict {
		t.Fatal("changed generation fused", err)
	}
	scope.Generation++
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sources SET relative_path='changed.mkv' WHERE id=$1::uuid`, scope.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true)); err != domain.ErrConflict {
		t.Fatal("changed media source fused", err)
	}
	scope.MediaPath = "changed.mkv"
	scope.Source.RelativePath = "changed.nfo"
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true)); err != domain.ErrUnauthenticated {
		t.Fatal("revoked fusion committed", err)
	}
}
