package postgres

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOActorAtomicPersistenceManualClearAndDowngrade(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	zero := 0
	fields.Version, fields.LockData = domain.NFOItemActorFieldsVersion, true
	fields.Actors = []domain.NFOActor{{Name: "演員甲", Role: "主角", Thumb: "https://images.example.invalid/a.jpg", Order: &zero}, {Name: "Actor B"}}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_actors() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_actors BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_actors()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_actors ON `+table+`; DROP FUNCTION reject_actors()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("actor fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 13 {
		t.Fatal("actor value and global lock facts failed", err)
	}
	var people []domain.NFOActor
	fact := result.Metadata.Facts[0]
	if fact.Field != "actors" || json.Unmarshal(fact.Value, &people) != nil || !reflect.DeepEqual(people, fields.Actors) || fact.Source != "nfo" || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
		t.Fatal("actors lost structure, provenance or order")
	}
	nfoMigrationDenied(t, f, "000032_nfo_actors.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "actors", Locked: &off}})
	if err != nil || flag.Facts[0].NFOOrigin == nil || flag.Facts[0].NFOLockOrigin == nil {
		t.Fatal("flag-only actor edit cleared provenance", err)
	}
	manual, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "actors", Value: json.RawMessage("null")}})
	if err != nil || string(manual.Facts[0].Value) != "null" || manual.Facts[0].Source != "manual" || manual.Facts[0].NFOOrigin != nil || manual.Facts[0].NFOLockOrigin != nil {
		t.Fatal("manual actor null did not take ownership", err)
	}
	nfoMigrationDenied(t, f, "000032_nfo_actors.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || string(review.Metadata.Facts[0].Value) != "null" || review.Metadata.Facts[0].Source != "manual" || review.Metadata.Facts[0].NFOOrigin != nil || review.Metadata.Facts[0].NFOLockOrigin == nil {
		t.Fatal("review replaced manual clear or lost new actor lock", err)
	}
	empty, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 5, nil, []domain.ItemMetadataFactPatch{{Field: "actors", Value: json.RawMessage("[]")}})
	if err != nil || string(empty.Facts[0].Value) != "[]" || empty.Facts[0].NFOOrigin != nil || empty.Facts[0].NFOLockOrigin != nil {
		t.Fatal("actor empty array clear lost its representation", err)
	}
	nfoMigrationDenied(t, f, "000032_nfo_actors.down.sql")
}

func TestNFOActorNamedLockAndPublishedListsRoundTrip(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemListFieldsVersion
	fields.Lists = []domain.NFOStringList{{Field: "genres", Values: []string{"Drama", "Mystery"}}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("actor migration changed published list data", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.Lists = domain.NFOItemActorFieldsVersion, nil, nil
	fields.LockedFields = []string{"Cast"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(locked.Applied) != 0 || len(locked.Metadata.Facts) != 2 {
		t.Fatal("actor named lock-only failed", err)
	}
	fact := locked.Metadata.Facts[0]
	if fact.Field != "actors" || fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
		t.Fatal("actor named lock invented value provenance")
	}
	if !reflect.DeepEqual(locked.Metadata.Facts[1], result.Metadata.Facts[0]) {
		t.Fatal("actor named lock changed an unrelated list")
	}
	nfoMigrationDenied(t, f, "000032_nfo_actors.down.sql")
}
