package postgres

import (
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOSortTitlePersistsAndManualTakeoverClearsLock(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemSortFieldsVersion
	fields.Fields = []domain.NFOTextField{{Field: "sortTitle", Value: "Sorting title"}}
	fields.LockedFields = []string{"SortName"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal("confirmed NFO sort title was not persisted", err)
	}
	if result.Metadata.Revision != 2 || len(result.Applied) != 1 || result.Applied[0] != "sortTitle" {
		t.Fatal("sort title did not share the accepted revision")
	}
	var sortTitle domain.ItemMetadataField
	for _, field := range result.Metadata.Fields {
		if field.Field == "sortTitle" {
			sortTitle = field
		}
	}
	if sortTitle.Value != "Sorting title" || sortTitle.Source != "nfo" || sortTitle.NFOOrigin == nil || sortTitle.NFOOrigin.Projection != domain.NFOItemSortFieldsVersion || sortTitle.NFOLockOrigin == nil || !sortTitle.NFOOrigin.Locked {
		t.Fatal("sort title lost value provenance or positive lock")
	}
	manual := ""
	updated, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 2, []domain.ItemMetadataPatch{{Field: "sortTitle", Value: &manual}})
	if err != nil {
		t.Fatal("manual sort title takeover failed", err)
	}
	for _, field := range updated.Fields {
		if field.Field == "sortTitle" && (field.Value != "" || field.Source != "manual" || field.NFOOrigin != nil || field.NFOLockOrigin != nil) {
			t.Fatal("explicit manual clear retained NFO ownership")
		}
	}
}

func TestNFOSortTitleMigrationAndAtomicFailure(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemSortFieldsVersion
	fields.Fields = []domain.NFOTextField{{Field: "sortTitle", Value: "Sorting title"}}
	fields.LockData = true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_sort_title() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_sort_title BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_sort_title()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(15, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_sort_title ON `+table+`; DROP FUNCTION reject_sort_title()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed sort title fusion retained partial data", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(15, true))
	if err != nil || result.Metadata.Revision != 2 || len(result.NFO.Applied) != 1 || len(result.TMDB.Skipped) != 4 || len(result.Metadata.Fields) != 5 {
		t.Fatal("sort title fusion lost five independent locks", err)
	}
	nfoMigrationDenied(t, f, "000027_nfo_sort_title.down.sql")
	if restored, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(restored, result.Metadata) {
		t.Fatal("refused downgrade changed sort title", err)
	}
}

func TestNFOAbsentSortTitleLockDoesNotInventText(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version, fields.Fields = domain.NFOItemSortFieldsVersion, nil
	fields.LockedFields = []string{"SortName"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Applied) != 0 {
		t.Fatal("sort title lock-only review failed", err)
	}
	for _, field := range result.Metadata.Fields {
		if field.Field == "sortTitle" {
			if field.Value != "" || field.Source != "existing" || field.NFOOrigin != nil || field.UpdatedAt != nil || field.NFOLockOrigin == nil {
				t.Fatal("sort title lock invented text provenance")
			}
			return
		}
	}
	t.Fatal("missing sort title lost positive lock")
}
