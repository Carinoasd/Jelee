package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func sourceRatingFact(t *testing.T, metadata domain.ItemMetadata, name string) domain.ItemMetadataFact {
	t.Helper()
	for _, fact := range metadata.Facts {
		if fact.Field == name {
			return fact
		}
	}
	t.Fatal("missing fact", name)
	return domain.ItemMetadataFact{}
}

func TestNFOSourceRatingsAtomicPersistenceManualClearAndDowngrade(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	maximum, votes := 100.0, 0
	fields.Version, fields.LockData = domain.NFOItemRatingFieldsVersion, true
	fields.Ratings = []domain.NFOSourceRating{{Name: "source", Value: 85, Max: &maximum, Votes: &votes, Default: true}, {Value: 0}}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_ratings() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_ratings BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_ratings()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_ratings ON `+table+`; DROP FUNCTION reject_ratings()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rating fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 15 {
		t.Fatal("ratings and complete global locks failed", err)
	}
	fact := sourceRatingFact(t, result.Metadata, "ratings")
	var ratings []domain.NFOSourceRating
	if json.Unmarshal(fact.Value, &ratings) != nil || !reflect.DeepEqual(ratings, fields.Ratings) || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
		t.Fatal("rating structure, source or lock lost")
	}
	for _, raw := range []string{`[]`, `null`, `[{"value":11}]`, `[{"value":1,"max":0}]`, `[{"value":1,"votes":1.5}]`, `[{"value":1,"default":null}]`, `[{"value":1,"name":null}]`, `[{"value":1,"extra":true}]`} {
		_, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$1::jsonb WHERE item_id=$2::uuid AND field='ratings'`, raw, scope.ItemID)
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatal("database allowed invalid source rating", raw, err)
		}
	}
	nfoMigrationDenied(t, f, "000034_nfo_ratings.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "ratings", Locked: &off}})
	if err != nil || sourceRatingFact(t, flag, "ratings").NFOOrigin == nil || sourceRatingFact(t, flag, "ratings").NFOLockOrigin == nil {
		t.Fatal("rating flag removed source proof", err)
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, []domain.ItemMetadataFactPatch{{Field: "ratings", Value: json.RawMessage("null")}})
	fact = sourceRatingFact(t, clear, "ratings")
	if err != nil || string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
		t.Fatal("rating clear retained source proof", err)
	}
	nfoMigrationDenied(t, f, "000034_nfo_ratings.down.sql")
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	fact = sourceRatingFact(t, review.Metadata, "ratings")
	if err != nil || string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
		t.Fatal("review overwrote manual rating clear", err)
	}
	empty, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 5, nil, []domain.ItemMetadataFactPatch{{Field: "ratings", Value: json.RawMessage("[]")}})
	fact = sourceRatingFact(t, empty, "ratings")
	if err != nil || string(fact.Value) != "[]" || fact.NFOLockOrigin != nil {
		t.Fatal("rating empty array lost clear representation", err)
	}
	nfoMigrationDenied(t, f, "000034_nfo_ratings.down.sql")
}

func TestNFOSourceRatingsPublishedIdentifiersRoundTripAndMissingLock(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemIdentifierFieldsVersion
	fields.UniqueIDs = []domain.NFOUniqueID{{Type: "imdb", Value: "tt1234567", Default: true}}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "down", 34)
	nfoMigrateVersion(t, f, "down", 33)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("rating migration changed published identifiers", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.UniqueIDs = domain.NFOItemRatingFieldsVersion, nil, nil
	fields.LockedFields = []string{"SourceRatings"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(locked.Metadata.Facts) != 2 {
		t.Fatal("missing rating lock failed", err)
	}
	fact := sourceRatingFact(t, locked.Metadata, "ratings")
	if fact.Value != nil || fact.Source != "existing" || fact.NFOOrigin != nil || fact.UpdatedAt != nil || fact.NFOLockOrigin == nil {
		t.Fatal("missing rating lock invented value source")
	}
	if !reflect.DeepEqual(sourceRatingFact(t, locked.Metadata, "uniqueIds"), result.Metadata.Facts[0]) {
		t.Fatal("rating lock changed identifiers")
	}
	nfoMigrationDenied(t, f, "000034_nfo_ratings.down.sql")
}
