package postgres

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOIdentifierAtomicPersistenceAndRetainedDataRollback(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemIdentifierFieldsVersion
	fields.UniqueIDs = []domain.NFOUniqueID{{Type: "imdb", Value: "tt1234567", Default: true}, {Type: "custom", Value: "vendor-42"}}
	fields.LockedFields = []string{"ProviderIds"}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_identifiers() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_identifiers BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_identifiers()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_identifiers ON `+table+`; DROP FUNCTION reject_identifiers()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("identifier fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 1 {
		t.Fatal("identifier save failed", err)
	}
	fact := result.Metadata.Facts[0]
	var ids []domain.NFOUniqueID
	if fact.Field != "uniqueIds" || json.Unmarshal(fact.Value, &ids) != nil || !reflect.DeepEqual(ids, fields.UniqueIDs) || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
		t.Fatal("identifier source, lock or structure lost")
	}
	nfoMigrationDenied(t, f, "000033_nfo_identifiers.down.sql")
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "uniqueIds", Value: json.RawMessage("null")}})
	if err != nil || string(clear.Facts[0].Value) != "null" || clear.Facts[0].Source != "manual" || clear.Facts[0].NFOOrigin != nil || clear.Facts[0].NFOLockOrigin != nil {
		t.Fatal("identifier manual clear lost ownership", err)
	}
	nfoMigrationDenied(t, f, "000033_nfo_identifiers.down.sql")
	scope.Revision = 3
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || string(review.Metadata.Facts[0].Value) != "null" || review.Metadata.Facts[0].Source != "manual" || review.Metadata.Facts[0].NFOOrigin != nil || review.Metadata.Facts[0].NFOLockOrigin == nil {
		t.Fatal("identifier review replaced manual clear or lost lock", err)
	}
}

func TestNFOIdentifierPublishedActorsRoundTripAndMissingValueLock(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemActorFieldsVersion
	fields.Actors = []domain.NFOActor{{Name: "Actor", Role: "Lead"}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("identifier migration changed published actors", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.Actors = domain.NFOItemIdentifierFieldsVersion, nil, nil
	fields.LockedFields = []string{"ProviderIds"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(locked.Metadata.Facts) != 2 {
		t.Fatal("missing identifier lock failed", err)
	}
	fact := locked.Metadata.Facts[1]
	if fact.Field != "uniqueIds" || fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
		t.Fatal("missing identifier lock invented value provenance")
	}
	if !reflect.DeepEqual(locked.Metadata.Facts[0], result.Metadata.Facts[0]) {
		t.Fatal("identifier lock changed actors")
	}
	nfoMigrationDenied(t, f, "000033_nfo_identifiers.down.sql")
}
