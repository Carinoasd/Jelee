package postgres

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The established fusion repository boundary must preserve every existing
// value protected by lockdata, even when that value is absent from the NFO.
func TestNFOItemLockDataProtectsFieldsAbsentFromNFO(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	seed, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedMissing), tmdbMetadataUpdate(12, true))
	if err != nil {
		t.Fatal(err)
	}
	scope.Revision = seed.Metadata.Revision
	fields.Fields = fields.Fields[:1]
	fields.LockData = true
	incoming := tmdbMetadataUpdate(13, true)
	incoming.Fields[2].Value = "Must not replace the locked overview"
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), incoming)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TMDB.Applied) != 0 || len(result.TMDB.Skipped) != 4 {
		t.Fatal("lockdata allowed provider to overwrite fields absent from NFO")
	}
	for index := 1; index < len(seed.Metadata.Fields); index++ {
		old, current := seed.Metadata.Fields[index], result.Metadata.Fields[index]
		if current.Value != old.Value || current.Source != old.Source || current.ProviderOrigin == nil || current.ProviderOrigin.ProviderID != 12 {
			t.Fatal("NFO global lock lost existing field value or provenance", current.Field)
		}
	}
}

func TestNFOItemLockIntentDatabaseRejectsUnsafeProof(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Fields = fields.Fields[:1]
	fields.LockedFields = []string{"Overview"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(result.Metadata.Fields[1].NFOLockOrigin)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["path"] = "private.nfo" },
		func(v map[string]any) { delete(v, "sourceId") },
		func(v map[string]any) { v["sourceId"] = "invalid" },
		func(v map[string]any) { v["generation"] = "2" },
		func(v map[string]any) { v["generation"] = 0 },
		func(v map[string]any) { v["generation"] = json.Number("9223372036854775808") },
		func(v map[string]any) { v["identityDigest"] = strings.Repeat("A", 64) },
		func(v map[string]any) { v["projection"] = "caller-projection" },
		func(v map[string]any) { v["locked"] = false },
		func(v map[string]any) { v["locked"] = "true" },
		func(v map[string]any) { v["readAt"] = "infinity" },
		func(v map[string]any) { v["readAt"] = "2026-10-02T08:00:00+08:00" },
		func(v map[string]any) { v["stamp"] = nil },
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
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_nfo_field_locks SET origin=$2::jsonb WHERE item_id=$1::uuid`, scope.ItemID, raw); err == nil {
			t.Fatal("database accepted unsafe NFO lock proof")
		}
	}
	if value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(value, result.Metadata) {
		t.Fatal("rejected proof changed metadata", err)
	}
}

func TestNFOItemLockIntentPersistenceAndManualTakeover(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Fields = fields.Fields[:1]
	fields.LockedFields = []string{"Overview", "unknown"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Metadata.Fields) != 2 || result.Metadata.Fields[1].Field != "overview" || result.Metadata.Fields[1].Value != "" || result.Metadata.Fields[1].Source != "existing" || result.Metadata.Fields[1].NFOOrigin != nil || result.Metadata.Fields[1].NFOLockOrigin == nil || result.Metadata.Fields[1].UpdatedAt != nil {
		t.Fatal("absent locked field invented a text value or origin")
	}
	nfoMigrationDenied(t, f, "000025_item_nfo_field_locks.down.sql")
	// Subsequent TMDB-only reviews read the saved proof even after NFO mode is off.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='off' WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	stored, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	expected := domain.CloneItemMetadata(result.Metadata)
	expected.NFOMode = domain.NFOModeOff
	if err != nil || !reflect.DeepEqual(stored, expected) {
		t.Fatal("stored metadata unavailable", err)
	}
	clone := domain.CloneItemMetadata(stored)
	clone.Fields[1].NFOLockOrigin.Stamp.SHA256 = "changed clone"
	if stored.Fields[1].NFOLockOrigin.Stamp.SHA256 != fields.Stamp.SHA256 {
		t.Fatal("lock proof aliased")
	}
	unlock := false
	stored, err = f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, stored.Revision, []domain.ItemMetadataPatch{{Field: "overview", Locked: &unlock}})
	if err != nil || stored.Fields[1].NFOLockOrigin == nil {
		t.Fatal("manual flag bypassed NFO lock", err)
	}
	applied, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, scope.ItemID, stored.Revision, tmdbMetadataUpdate(13, true))
	if err != nil {
		t.Fatal(err)
	}
	if applied.Metadata.Fields[2].Value != "" || applied.Metadata.Fields[2].NFOLockOrigin == nil {
		t.Fatal("persisted absent-field lock did not protect TMDB-only review")
	}
	manual := "Owner overview"
	stored, err = f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, applied.Metadata.Revision, []domain.ItemMetadataPatch{{Field: "overview", Value: &manual}})
	if err != nil || stored.Fields[2].NFOLockOrigin != nil || stored.Fields[2].Source != "manual" || stored.Fields[2].Value != manual {
		t.Fatal("manual takeover retained lock intent", err)
	}
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	stored, err = f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || stored.Fields[2].Value != manual {
		t.Fatal("clean lock migration changed metadata", err)
	}
}

func TestNFOItemLockIntentAndManualTakeoverRollback(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Fields = fields.Fields[:1]
	fields.LockData = true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_nfo_field_locks", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_lock_intent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_lock_intent BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_lock_intent()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_lock_intent ON `+table+`; DROP FUNCTION reject_lock_intent()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed write retained values, locks, observation or revision", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_manual_lock() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_manual_lock BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_manual_lock()`); err != nil {
		t.Fatal(err)
	}
	manual := "Owner title"
	_, updateErr := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, result.Metadata.Revision, []domain.ItemMetadataPatch{{Field: "title", Value: &manual}})
	after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_manual_lock ON audit_logs; DROP FUNCTION reject_manual_lock()`); err != nil {
		t.Fatal(err)
	}
	if updateErr == nil || readErr != nil || !reflect.DeepEqual(after, result.Metadata) {
		t.Fatal("failed manual takeover lost lock or changed metadata", updateErr, readErr)
	}
}
