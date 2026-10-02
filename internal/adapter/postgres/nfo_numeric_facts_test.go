package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFONumericManualWhitespaceNullDoesNotBecomeZero(t *testing.T) {
	f, scope, _ := nfoItemApplyFixture(t)
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 1, nil, []domain.ItemMetadataFactPatch{{Field: "rating", Value: json.RawMessage(" null ")}})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("noncanonical null was accepted as a numeric zero", err)
	}
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("invalid numeric edit changed metadata", err)
	}
}

func TestNFONumericFactsAtomicPersistenceAndManualPriority(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemNumericFieldsVersion
	fields.Facts = []domain.NFOIntegerFact{{Field: "year", Value: 2024}, {Field: "runtimeMinutes", Value: 92}}
	fields.NumberFacts = []domain.NFONumberFact{{Field: "rating", Value: 8.5}, {Field: "userRating", Value: 9.25}}
	fields.LockData = true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_numeric() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_numeric BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_numeric()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_numeric ON `+table+`; DROP FUNCTION reject_numeric()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("numeric fusion left partial values, locks or revision", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 4 {
		t.Fatal("numeric fusion failed", err)
	}
	expected := map[string]string{"year": "2024", "runtimeMinutes": "92", "rating": "8.5", "userRating": "9.25"}
	for _, fact := range result.Metadata.Facts {
		if string(fact.Value) != expected[fact.Field] || fact.Source != "nfo" || fact.NFOOrigin == nil || fact.NFOOrigin.Projection != domain.NFOItemNumericFieldsVersion || !fact.NFOOrigin.Locked || fact.NFOLockOrigin == nil || fact.UpdatedAt == nil {
			t.Fatal("numeric value lost type, source or independent lock", fact.Field)
		}
	}
	nfoMigrationDenied(t, f, "000030_nfo_numeric_facts.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "rating", Locked: &off}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range flag.Facts {
		if fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
			t.Fatal("manual flag removed numeric provenance")
		}
	}
	manual, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "year", Value: json.RawMessage("2023")}, {Field: "runtimeMinutes", Value: json.RawMessage("0")}, {Field: "rating", Value: json.RawMessage("null")}, {Field: "userRating", Value: json.RawMessage("0")}})
	if err != nil || manual.Revision != 4 || len(manual.Facts) != 4 {
		t.Fatal("manual numeric edits failed", err)
	}
	expected = map[string]string{"year": "2023", "runtimeMinutes": "0", "rating": "null", "userRating": "0"}
	for _, fact := range manual.Facts {
		if string(fact.Value) != expected[fact.Field] || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
			t.Fatal("manual clear or zero lost ownership", fact.Field)
		}
	}
	nfoMigrationDenied(t, f, "000030_nfo_numeric_facts.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range review.Metadata.Facts {
		if string(fact.Value) != expected[fact.Field] || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
			t.Fatal("NFO overwrote manual numeric choice or lost renewed lock", fact.Field)
		}
	}
}

func TestNFONumericLockOnlyHasNoInventedValues(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version, fields.Fields = domain.NFOItemNumericFieldsVersion, nil
	fields.LockedFields = []string{"Runtime", "CommunityRating", "UserRating"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Applied) != 0 || len(result.Metadata.Facts) != 3 {
		t.Fatal("numeric lock-only failed", err)
	}
	for _, fact := range result.Metadata.Facts {
		if fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
			t.Fatal("numeric lock-only invented a value", fact.Field)
		}
	}
	nfoMigrationDenied(t, f, "000030_nfo_numeric_facts.down.sql")
}

func TestNFONumericMigrationPreservesPublishedYearProjection(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemYearFieldsVersion
	fields.Facts = []domain.NFOIntegerFact{{Field: "year", Value: 2024}}
	fields.LockedFields = []string{"ProductionYear"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("numeric migration changed published year data or proof", err)
	}
}
