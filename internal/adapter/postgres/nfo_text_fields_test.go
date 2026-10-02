package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOTextTaglinePersistsWithIndependentLock(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemTextFieldsVersion
	fields.Fields = []domain.NFOTextField{{Field: "tagline", Value: "A short tagline"}}
	fields.LockedFields = []string{"Tagline"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal("confirmed NFO tagline was not persisted", err)
	}
	for _, field := range result.Metadata.Fields {
		if field.Field == "tagline" {
			if field.Value != "A short tagline" || field.Source != "nfo" || field.NFOOrigin == nil || field.NFOOrigin.Projection != domain.NFOItemTextFieldsVersion || field.NFOLockOrigin == nil || !field.NFOOrigin.Locked {
				t.Fatal("tagline lost provenance or positive lock")
			}
			return
		}
	}
	t.Fatal("tagline was not returned after persistence")
}

func TestNFOExtendedTextMigrationRollbackAndManualTakeover(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 30)
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.Version = domain.NFOItemTextFieldsVersion
	fields.Fields = append(fields.Fields, domain.NFOTextField{Field: "sortTitle", Value: "Sorting title"}, domain.NFOTextField{Field: "tagline", Value: "Short tagline"}, domain.NFOTextField{Field: "outline", Value: "Short outline"}, domain.NFOTextField{Field: "mpaa", Value: "PG-13"}, domain.NFOTextField{Field: "certification", Value: "TW:12"})
	fields.LockData = true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_text_fields() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_text_fields BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_text_fields()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_text_fields ON `+table+`; DROP FUNCTION reject_text_fields()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed nine-field fusion retained partial state", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
	if err != nil || len(result.Applied) != 9 || result.Metadata.Revision != 2 {
		t.Fatal("nine-field fusion failed", err)
	}
	nfoMigrationDenied(t, f, "000028_nfo_text_fields.down.sql")
	empty, title := "", "Owner title"
	patches := []domain.ItemMetadataPatch{}
	for _, field := range result.Metadata.Fields {
		value := &empty
		if field.Field == "title" {
			value = &title
		}
		patches = append(patches, domain.ItemMetadataPatch{Field: field.Field, Value: value})
	}
	manual, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 2, patches)
	if err != nil {
		t.Fatal("manual nine-field takeover failed", err)
	}
	for _, field := range manual.Fields {
		if field.Source != "manual" || field.NFOOrigin != nil || field.NFOLockOrigin != nil {
			t.Fatal("manual takeover retained NFO proof", field.Field)
		}
	}
	nfoMigrationDenied(t, f, "000028_nfo_text_fields.down.sql")
	if restored, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(restored, manual) {
		t.Fatal("refused downgrade discarded manual empty values", err)
	}
}

func TestNFOExtendedLockOnlyAndPublishedProjectionBounds(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{domain.NFOItemFieldsVersion, domain.NFOItemLockFieldsVersion, domain.NFOItemSortFieldsVersion} {
		fields.Version, fields.Fields = version, []domain.NFOTextField{{Field: "tagline", Value: "Unsupported in old projection"}}
		if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("published projection silently accepted a new field", version, err)
		}
	}
	update := tmdbMetadataUpdate(16, true)
	update.Fields[0].Field = "certification"
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, scope.ItemID, 1, update); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("provider fabricated extended text provenance", err)
	}
	if unchanged, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(unchanged, before) {
		t.Fatal("rejected projection changed state", err)
	}
	fields.Version, fields.Fields, fields.LockedFields = domain.NFOItemTextFieldsVersion, nil, []string{"OfficialRating"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Applied) != 0 || len(result.Metadata.Fields) != 3 {
		t.Fatal("official rating lock-only failed", err)
	}
	for _, field := range result.Metadata.Fields[1:] {
		if (field.Field != "mpaa" && field.Field != "certification") || field.Value != "" || field.Source != "existing" || field.NFOOrigin != nil || field.UpdatedAt != nil || field.NFOLockOrigin == nil {
			t.Fatal("official rating lock invented value provenance", field.Field)
		}
	}
	unlock := false
	flag, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 2, []domain.ItemMetadataPatch{{Field: "mpaa", Locked: &unlock}})
	if err != nil || flag.Fields[1].NFOLockOrigin == nil {
		t.Fatal("manual flag bypassed independent rating lock", err)
	}
}

func TestNFOExtendedTextFieldsPersistTogether(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemTextFieldsVersion
	fields.Fields = []domain.NFOTextField{{Field: "tagline", Value: "Short tagline"}, {Field: "outline", Value: "Short outline"}, {Field: "mpaa", Value: "PG-13"}, {Field: "certification", Value: "TW:12"}}
	fields.LockData = true
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Applied) != 4 || len(result.Metadata.Fields) != 9 {
		t.Fatal("extended NFO text was not saved together", err)
	}
	for _, incoming := range fields.Fields {
		found := false
		for _, actual := range result.Metadata.Fields {
			if actual.Field == incoming.Field {
				found = actual.Value == incoming.Value && actual.Source == "nfo" && actual.NFOOrigin != nil && actual.NFOOrigin.Projection == domain.NFOItemTextFieldsVersion && actual.NFOLockOrigin != nil
			}
		}
		if !found {
			t.Fatal("extended text lost value, origin or lock", incoming.Field)
		}
	}
}
