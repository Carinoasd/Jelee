package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNFOCollectionAtomicPersistenceManualClearAndDowngrade(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version, fields.LockData = domain.NFOItemCollectionFieldsVersion, true
	fields.Collection = &domain.NFOCollection{Name: "Collection A", Overview: "Plot"}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_collection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_collection BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_collection()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_collection ON `+table+`; DROP FUNCTION reject_collection()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("collection fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 16 {
		t.Fatal("collection and global locks failed", err)
	}
	fact := sourceRatingFact(t, result.Metadata, "collection")
	var value domain.NFOCollection
	if json.Unmarshal(fact.Value, &value) != nil || value != *fields.Collection || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
		t.Fatal("collection structure or proofs lost")
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"name":null}`, `{"name":" "}`, `{"name":"\t"}`, `{"name":"\u00a0"}`, `{"name":"A","overview":null}`, `{"name":"A","overview":1}`, `{"name":"A","extra":true}`} {
		_, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$1::jsonb WHERE item_id=$2::uuid AND field='collection'`, raw, scope.ItemID)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatal("database accepted invalid collection", raw, err)
		}
	}
	nfoMigrationDenied(t, f, "000035_nfo_collection.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "collection", Locked: &off}})
	if err != nil || sourceRatingFact(t, flag, "collection").NFOOrigin == nil || sourceRatingFact(t, flag, "collection").NFOLockOrigin == nil {
		t.Fatal("collection flag removed proofs", err)
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "collection", Value: json.RawMessage("null")}})
	fact = sourceRatingFact(t, clear, "collection")
	if err != nil || string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
		t.Fatal("collection clear retained proofs", err)
	}
	nfoMigrationDenied(t, f, "000035_nfo_collection.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	fact = sourceRatingFact(t, review.Metadata, "collection")
	if err != nil || string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
		t.Fatal("review overwrote collection clear", err)
	}
}

func TestNFOCollectionPublishedRatingsRoundTripAndMissingLock(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemRatingFieldsVersion
	maximum, votes := 100.0, 0
	fields.Ratings = []domain.NFOSourceRating{{Name: "source", Value: 85, Max: &maximum, Votes: &votes, Default: true}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 42)
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("collection migration changed published ratings", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.Ratings = domain.NFOItemCollectionFieldsVersion, nil, nil
	fields.LockedFields = []string{"Set"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	fact := sourceRatingFact(t, locked.Metadata, "collection")
	if fact.Value != nil || fact.UpdatedAt != nil || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil || fact.NFOLockOrigin.Projection != domain.NFOItemCollectionFieldsVersion {
		t.Fatal("missing collection lock invented value proof")
	}
	if !reflect.DeepEqual(sourceRatingFact(t, after, "ratings"), sourceRatingFact(t, locked.Metadata, "ratings")) {
		t.Fatal("collection lock changed old ratings")
	}
	nfoMigrationDenied(t, f, "000035_nfo_collection.down.sql")
}
