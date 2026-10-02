package postgres

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOYearFactsAtomicPersistenceLocksAndManualClear(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "down", 32)
	nfoMigrateVersion(t, f, "down", 31)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemYearFieldsVersion
	fields.Facts = []domain.NFOIntegerFact{{Field: "year", Value: 2024}}
	fields.LockedFields = []string{"ProductionYear"}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_year() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_year BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_year()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_year ON `+table+`; DROP FUNCTION reject_year()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("year fusion was not atomic", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 1 {
		t.Fatal("year fusion failed", err)
	}
	fact := result.Metadata.Facts[0]
	if string(fact.Value) != "2024" || fact.Source != "nfo" || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil || !fact.NFOOrigin.Locked || fact.UpdatedAt == nil {
		t.Fatal("year lost type or provenance")
	}
	nfoMigrationDenied(t, f, "000029_nfo_year_fact.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "year", Locked: &off}})
	if err != nil || flag.Facts[0].NFOOrigin == nil || flag.Facts[0].NFOLockOrigin == nil {
		t.Fatal("manual flag cleared NFO year intent", err)
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "year", Value: json.RawMessage("null")}})
	if err != nil || string(clear.Facts[0].Value) != "null" || clear.Facts[0].Source != "manual" || clear.Facts[0].NFOOrigin != nil || clear.Facts[0].NFOLockOrigin != nil {
		t.Fatal("manual year clear failed", err)
	}
	nfoMigrationDenied(t, f, "000029_nfo_year_fact.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || string(review.Metadata.Facts[0].Value) != "null" || review.Metadata.Facts[0].Source != "manual" || review.Metadata.Facts[0].NFOOrigin != nil || review.Metadata.Facts[0].NFOLockOrigin == nil {
		t.Fatal("NFO review overwrote manual clear or lost renewed lock", err)
	}
	title := "Owner title"
	patches := []domain.ItemMetadataPatch{{Field: "title", Value: &title}}
	facts := []domain.ItemMetadataFactPatch{{Field: "year", Value: json.RawMessage("2023")}}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_manual_year() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_manual_year BEFORE INSERT OR UPDATE ON item_metadata_facts FOR EACH ROW EXECUTE FUNCTION reject_manual_year()`); err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 5, patches, facts)
	afterFailure, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_manual_year ON item_metadata_facts; DROP FUNCTION reject_manual_year()`); err != nil {
		t.Fatal(err)
	}
	if writeErr == nil || readErr != nil || !reflect.DeepEqual(afterFailure, review.Metadata) {
		t.Fatal("mixed manual edit left text, fact, revision or lock changes", writeErr, readErr)
	}
	manual, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 5, patches, facts)
	if err != nil || manual.Revision != 6 || manual.Fields[0].Value != "Owner title" || string(manual.Facts[0].Value) != "2023" || manual.Facts[0].Source != "manual" || manual.Facts[0].NFOOrigin != nil || manual.Facts[0].NFOLockOrigin != nil {
		t.Fatal("mixed manual edit did not commit together", err)
	}
}

func TestNFOYearLockOnlyHasNoInventedValue(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemYearFieldsVersion
	fields.Fields = nil
	fields.LockedFields = []string{"Year"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Applied) != 0 || len(result.Metadata.Facts) != 1 {
		t.Fatal("year lock-only failed", err)
	}
	fact := result.Metadata.Facts[0]
	if fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
		t.Fatal("year lock-only invented a fact")
	}
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "year", Locked: &off}})
	if err != nil || string(flag.Facts[0].Value) != "null" || flag.Facts[0].Source != "existing" || flag.Facts[0].NFOOrigin != nil || flag.Facts[0].NFOLockOrigin == nil {
		t.Fatal("absent year manual flag changed value provenance or lost lock", err)
	}
	nfoMigrationDenied(t, f, "000029_nfo_year_fact.down.sql")
}
