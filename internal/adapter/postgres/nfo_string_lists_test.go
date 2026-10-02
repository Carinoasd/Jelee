package postgres

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOStringListsAtomicPersistenceManualClearAndDowngrade(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemListFieldsVersion
	fields.Lists = []domain.NFOStringList{{Field: "genres", Values: []string{"Drama", "Mystery"}}, {Field: "tags", Values: []string{"Favorite"}}, {Field: "studios", Values: []string{"Studio"}}, {Field: "countries", Values: []string{"TW"}}, {Field: "languages", Values: []string{"zh"}}, {Field: "directors", Values: []string{"Director"}}, {Field: "writers", Values: []string{"Writer"}}, {Field: "producers", Values: []string{"Producer"}}}
	fields.LockData = true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_lists() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_lists BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_lists()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_lists ON `+table+`; DROP FUNCTION reject_lists()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("string list fusion left partial values, locks or revision", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 12 {
		t.Fatal("list values and global numeric lock-only facts failed", err)
	}
	for _, fact := range result.Metadata.Facts {
		if fact.NFOLockOrigin == nil || fact.NFOLockOrigin.Projection != domain.NFOItemListFieldsVersion {
			t.Fatal("global lock missed a list or number", fact.Field)
		}
		if fact.Field == "genres" {
			var values []string
			if json.Unmarshal(fact.Value, &values) != nil || !reflect.DeepEqual(values, []string{"Drama", "Mystery"}) || fact.Source != "nfo" || fact.NFOOrigin == nil {
				t.Fatal("genres lost order, type or origin")
			}
		}
	}
	nfoMigrationDenied(t, f, "000031_nfo_string_lists.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "genres", Locked: &off}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range flag.Facts {
		if fact.Field == "genres" && (fact.NFOOrigin == nil || fact.NFOLockOrigin == nil) {
			t.Fatal("flag-only cleared list provenance")
		}
	}
	manual, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "genres", Value: json.RawMessage("null")}, {Field: "tags", Value: json.RawMessage("[]")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range manual.Facts {
		if fact.Field == "genres" || fact.Field == "tags" {
			want := "null"
			if fact.Field == "tags" {
				want = "[]"
			}
			if string(fact.Value) != want || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
				t.Fatal("manual list clear lost ownership", fact.Field)
			}
		}
	}
	nfoMigrationDenied(t, f, "000031_nfo_string_lists.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range review.Metadata.Facts {
		if fact.Field == "genres" || fact.Field == "tags" {
			want := "null"
			if fact.Field == "tags" {
				want = "[]"
			}
			if string(fact.Value) != want || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
				t.Fatal("NFO overwrote manual list clear or lost fresh lock", fact.Field)
			}
		}
	}
}

func TestNFOStringListNamedLockOnlyAndPublishedNumericRoundTrip(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version, fields.Fields = domain.NFOItemNumericFieldsVersion, nil
	fields.Facts = []domain.NFOIntegerFact{{Field: "runtimeMinutes", Value: 92}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("string-list migration changed published numeric data", err)
	}
	scope.Revision = 2
	fields.Version, fields.Facts = domain.NFOItemListFieldsVersion, nil
	fields.LockedFields = []string{"Genres", "Tags", "Studios", "ProductionLocations", "Languages", "Directors", "Credits", "Producers"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(locked.Applied) != 0 || len(locked.Metadata.Facts) != 9 {
		t.Fatal("named list lock-only failed", err)
	}
	for _, fact := range locked.Metadata.Facts {
		if fact.Field == "runtimeMinutes" {
			if string(fact.Value) != "92" || fact.NFOLockOrigin != nil {
				t.Fatal("named list locks changed an unrelated numeric value")
			}
			continue
		}
		if fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
			t.Fatal("named list lock invented a value", fact.Field)
		}
	}
	nfoMigrationDenied(t, f, "000031_nfo_string_lists.down.sql")
}

func TestNFOStringListDatabaseRejectsBlankEntries(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemListFieldsVersion
	fields.Lists = []domain.NFOStringList{{Field: "genres", Values: []string{"Drama"}}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`["\t"]`, `["\u00a0"]`, `[" "]`, `[""]`, `[]`, `null`, `[null]`, `[1]`, `true`, `{"value":"Drama"}`} {
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$2::jsonb WHERE item_id=$1::uuid AND field='genres'`, scope.ItemID, value); err == nil {
			t.Fatal("database accepted a blank or non-string NFO list", value)
		}
	}
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("rejected direct list writes changed metadata", err)
	}
}
