package postgres

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoObservationState(scope domain.NFOItemScope, fields domain.NFOItemFields, status string) domain.NFOItemObservationState {
	state := domain.NFOItemObservationState{Status: status, Identity: fields.Identity, ReadAt: fields.ReadAt, Selection: domain.NFOItemSelection{CandidateDigest: domain.NFOCandidateDigest([]string{})}}
	if status != domain.NFOItemObservedMissing {
		state.Stamp = fields.Stamp
		state.Selection.RelativePath = scope.Source.RelativePath
		state.Selection.CandidateDigest = domain.NFOCandidateDigest([]string{scope.Source.RelativePath})
	}
	if status == domain.NFOItemObservedValid {
		state.Selection.Fields = fields
	}
	return state
}

func TestNFOItemObservationPersistenceHistoryAndMigration(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	for index, status := range []string{domain.NFOItemObservedMissing, domain.NFOItemObservedInvalid, domain.NFOItemObservedValid} {
		state := nfoObservationState(scope, fields, status)
		result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, state, tmdbMetadataUpdate(12, true))
		if err != nil || result.Metadata.Revision != scope.Revision+1 || result.NFO == nil || result.NFO.Status != status || result.Metadata.LastConfirmedNFOObservation == nil {
			t.Fatal("observation not atomic with accepted review", status, err)
		}
		confirmed := result.Metadata.LastConfirmedNFOObservation
		if confirmed.Status != status || confirmed.SourceID != scope.SourceID || confirmed.RootID != scope.RootID || confirmed.Generation != scope.Generation || confirmed.AcceptedRevision != result.Metadata.Revision || !confirmed.ReadAt.Equal(fields.ReadAt) {
			t.Fatal("confirmed historical identity differs", status)
		}
		if status == domain.NFOItemObservedMissing && confirmed.Stamp != nil || status != domain.NFOItemObservedMissing && (confirmed.Stamp == nil || confirmed.Stamp.SHA256 != fields.Stamp.SHA256) {
			t.Fatal("missing/invalid original stamp differs", status)
		}
		for _, field := range result.Metadata.Fields {
			if status != domain.NFOItemObservedValid && (field.Source != "tmdb" || field.NFOOrigin != nil) || status == domain.NFOItemObservedValid && field.Source != "nfo" {
				t.Fatal("fallback invented NFO field provenance", status)
			}
		}
		var raw []byte
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT observation FROM item_nfo_observations WHERE item_id=$1::uuid`, scope.ItemID).Scan(&raw); err != nil || strings.Contains(string(raw), "film.nfo") || strings.Contains(string(raw), scope.Source.RootPath) || strings.Contains(string(raw), "NFO title") {
			t.Fatal("saved observation exposed private data", err)
		}
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.tmdb_metadata_applied' AND target_id=$1::uuid`, scope.ItemID).Scan(&count); err != nil || count != index+1 {
			t.Fatal("review audit count differs", err)
		}
		scope.Revision++
	}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	after, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, scope.Revision, []domain.ItemMetadataPatch{{Field: "overview", Value: &empty}})
	if err != nil || !reflect.DeepEqual(after.LastConfirmedNFOObservation, before.LastConfirmedNFOObservation) || after.LastConfirmedNFOObservation.AcceptedRevision == after.Revision {
		t.Fatal("manual edit erased history or invented freshness", err)
	}
	clone := domain.CloneItemMetadata(after)
	clone.LastConfirmedNFOObservation.Stamp.SHA256 = strings.Repeat("b", 64)
	if after.LastConfirmedNFOObservation.Stamp.SHA256 != fields.Stamp.SHA256 {
		t.Fatal("caller changed history through clone")
	}
	scope.Revision = after.Revision
	fields.LockData = true
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedMissing), tmdbMetadataUpdate(13, true))
	if err != nil || len(result.Applied) != 0 || len(result.Skipped) != 4 || result.Metadata.Fields[2].Source != "manual" || result.Metadata.Fields[2].Value != "" || result.Metadata.Fields[0].Source != "nfo" {
		t.Fatal("missing fallback overwrote retained NFO/manual clear", err)
	}
	nfoMigrationDenied(t, f, "000024_item_nfo_observation.down.sql")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM item_nfo_observations WHERE item_id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || value.LastConfirmedNFOObservation != nil || value.Revision != result.Metadata.Revision {
		t.Fatal("clean observation migration changed metadata", err)
	}
}

func TestNFOItemObservationProviderObservationAndAuditRollback(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='HomeVideo' WHERE id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	scope.Kind = "HomeVideo"
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			ddl := `CREATE FUNCTION reject_observation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_observation BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION reject_observation()`
			if _, err := f.s.Pool.Exec(f.ctx, ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedInvalid), tmdbMetadataUpdate(12, true)); err == nil {
				t.Fatal("failed atomic observation write accepted")
			}
			after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("partial observation/provider/catalog/revision/kind survived", err)
			}
			var count int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.tmdb_metadata_applied' AND target_id=$1::uuid`, scope.ItemID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed observation left audit", err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_observation ON `+table+`; DROP FUNCTION reject_observation()`); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedMissing), tmdbMetadataUpdate(12, true)); err != domain.ErrConflict {
		t.Fatal("stale scope persisted missing observation", err)
	}
}

func TestNFOItemObservationDatabaseRejectsUnsafeRecords(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedInvalid), tmdbMetadataUpdate(12, true))
	if err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(result.Metadata.LastConfirmedNFOObservation)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["status"] = "unknown" },
		func(v map[string]any) { v["relativePath"] = "private/film.nfo" },
		func(v map[string]any) { delete(v, "stamp") },
		func(v map[string]any) { v["stamp"] = nil },
		func(v map[string]any) { v["status"] = "missing" },
		func(v map[string]any) { v["generation"] = 0 },
		func(v map[string]any) { v["generation"] = 1.5 },
		func(v map[string]any) { v["acceptedRevision"] = 1 },
		func(v map[string]any) { v["acceptedRevision"] = domain.ItemMetadataRevisionMax + 1 },
		func(v map[string]any) { v["readAt"] = "infinity" },
		func(v map[string]any) { v["readAt"] = "today" },
		func(v map[string]any) { v["readAt"] = "2026-10-02T00:00:00+08:00" },
		func(v map[string]any) { v["readAt"] = nil },
		func(v map[string]any) { v["sourceId"] = "private/path" },
		func(v map[string]any) { v["identityDigest"] = "bad" },
		func(v map[string]any) { v["candidateDigest"] = domain.NFOCandidateDigest([]string{}) },
		func(v map[string]any) { v["stamp"].(map[string]any)["size"] = domain.NFOMaxSourceBytes + 1 },
		func(v map[string]any) { v["stamp"].(map[string]any)["size"] = -1 },
		func(v map[string]any) { v["stamp"].(map[string]any)["modifiedUnixNano"] = "1" },
		func(v map[string]any) { v["stamp"].(map[string]any)["sha256"] = strings.Repeat("A", 64) },
		func(v map[string]any) { v["stamp"].(map[string]any)["fingerprintVersion"] = "partial" },
		func(v map[string]any) { v["stamp"].(map[string]any)["path"] = "private.nfo" },
	} {
		var value map[string]any
		if err := json.Unmarshal(good, &value); err != nil {
			t.Fatal(err)
		}
		mutate(value)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_nfo_observations SET observation=$2::jsonb WHERE item_id=$1::uuid`, scope.ItemID, raw); err == nil {
			t.Fatal("database accepted unsafe observation", string(raw))
		}
	}
	if value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(value, result.Metadata) {
		t.Fatal("rejected records changed accepted proof", err)
	}
}
