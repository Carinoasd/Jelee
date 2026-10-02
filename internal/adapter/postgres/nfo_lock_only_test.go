package postgres

import (
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLockOnlyNFOPersistsIntentWithoutTextOrClassification(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='HomeVideo' WHERE id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	scope.Kind = "HomeVideo"
	fields.Version = domain.NFOItemLockFieldsVersion
	fields.Fields = nil
	fields.LockData = true
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal("valid lock-only observation was not saved", err)
	}
	if result.Metadata.Revision != 2 || result.Metadata.Kind != "HomeVideo" || len(result.Applied) != 0 || result.NFO == nil || result.NFO.Status != domain.NFOItemObservedValid || result.Metadata.LastConfirmedNFOObservation == nil || result.Metadata.LastConfirmedNFOObservation.Status != domain.NFOItemObservedValid || len(result.Metadata.Fields) != 4 {
		t.Fatal("lock-only review invented text, classification or fallback state")
	}
	for _, field := range result.Metadata.Fields {
		if field.Source != "existing" || field.NFOOrigin != nil || field.ProviderOrigin != nil || field.UpdatedAt != nil || field.NFOLockOrigin == nil || field.NFOLockOrigin.Projection != domain.NFOItemLockFieldsVersion || field.NFOLockOrigin.Stamp.SHA256 != fields.Stamp.SHA256 {
			t.Fatal("lock-only review lost intent or invented value provenance", field.Field)
		}
		if field.Field == "title" && field.Value != "Original title" || field.Field != "title" && field.Value != "" {
			t.Fatal("lock-only review changed text", field.Field)
		}
	}
}

func TestLockOnlyNFOFusionPersistenceManualTakeoverAndMigration(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	fields.LockedFields = []string{"Overview"}
	legacy, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	// Legacy locks remain valid under schema25; the downgrade changes only the constraint.
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if restored, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(restored, legacy.Metadata) {
		t.Fatal("schema26 changed legacy lock proof", err)
	}
	scope.Revision = legacy.Metadata.Revision
	fields.Version = domain.NFOItemLockFieldsVersion
	fields.Fields = nil
	fields.LockData = true
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(13, true))
	if err != nil || len(result.NFO.Applied) != 0 || len(result.TMDB.Applied) != 0 || len(result.TMDB.Skipped) != 4 || result.Metadata.Revision != scope.Revision+1 {
		t.Fatal("lock-only fusion applied text or bypassed locks", err)
	}
	for i, field := range result.Metadata.Fields {
		if field.Value != legacy.Metadata.Fields[i].Value || !reflect.DeepEqual(field.NFOOrigin, legacy.Metadata.Fields[i].NFOOrigin) || !reflect.DeepEqual(field.UpdatedAt, legacy.Metadata.Fields[i].UpdatedAt) || field.NFOLockOrigin == nil || field.NFOLockOrigin.Projection != domain.NFOItemLockFieldsVersion {
			t.Fatal("lock-only fusion altered existing value provenance", field.Field)
		}
	}
	nfoMigrationDenied(t, f, "000026_nfo_lock_only_projection.down.sql")
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='off' WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	unlock := false
	updated, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, result.Metadata.Revision, []domain.ItemMetadataPatch{{Field: "title", Locked: &unlock}})
	if err != nil || updated.Fields[0].NFOLockOrigin == nil {
		t.Fatal("manual flag bypassed lock-only proof", err)
	}
	provider, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, scope.ItemID, updated.Revision, tmdbMetadataUpdate(14, true))
	if err != nil || len(provider.Applied) != 0 || len(provider.Skipped) != 4 {
		t.Fatal("persisted lock-only proof lost after NFO mode off", err)
	}
	manual, empty := "Owner title", ""
	updated, err = f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, provider.Metadata.Revision, []domain.ItemMetadataPatch{{Field: "title", Value: &manual}, {Field: "originalTitle", Value: &empty}, {Field: "overview", Value: &empty}, {Field: "date", Value: &empty}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range updated.Fields {
		if field.NFOLockOrigin != nil || field.NFOOrigin != nil || field.Source != "manual" {
			t.Fatal("manual takeover retained lock-only proof")
		}
	}
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || !reflect.DeepEqual(value, updated) {
		t.Fatal("clean projection migration changed manual data", err)
	}
}

func TestLockOnlyNFOObservationAndAuditRollback(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version, fields.Fields, fields.LockData = domain.NFOItemLockFieldsVersion, nil, true
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_lock_only() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_lock_only BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_lock_only()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(13, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_lock_only ON `+table+`; DROP FUNCTION reject_lock_only()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("failed lock-only fusion retained partial data", applyErr, readErr)
			}
		})
	}
}
