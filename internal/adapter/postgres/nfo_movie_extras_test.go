package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNFOMovieExtrasAtomicPersistenceAndManualClear(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	zero := 0
	fields.Version, fields.LockData = domain.NFOItemMovieFieldsVersion, true
	fields.DateAdded = "2024-02-29T12:34:56.123+08:00"
	fields.Trailers = []string{"trailers/a.mp4", "plugin://video/trailer"}
	fields.Art = []domain.NFOArtwork{{Kind: "poster", Location: "poster.jpg", Preview: "preview.jpg", Season: &zero}, {Kind: "fanart", Location: "fanart.jpg"}}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_movie_extras() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_movie_extras BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_movie_extras()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), tmdbMetadataUpdate(16, true))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_movie_extras ON `+table+`; DROP FUNCTION reject_movie_extras()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("movie extras fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 19 {
		t.Fatal("movie extras and global locks failed", err)
	}
	for name, value := range map[string]any{"dateAdded": fields.DateAdded, "trailers": fields.Trailers, "art": fields.Art} {
		fact := sourceRatingFact(t, result.Metadata, name)
		want, _ := json.Marshal(value)
		var a, b any
		if json.Unmarshal(fact.Value, &a) != nil || json.Unmarshal(want, &b) != nil || !reflect.DeepEqual(a, b) || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
			t.Fatal("movie extras value or proof lost", name)
		}
	}
	for field, invalid := range map[string][]string{
		"dateAdded": {`null`, `""`, `"0000-01-01"`, `"2023-02-29"`, `"2024-01-01T24:00:00Z"`, `"2024-01-01T00:00:00+24:00"`, `0`},
		"trailers":  {`null`, `[]`, `[null]`, `[1]`, `["\t"]`, `["\u00a0"]`},
		"art":       {`null`, `[]`, `[{}]`, `[{"kind":"\t","location":"a"}]`, `[{"kind":"poster","location":"\u00a0"}]`, `[{"kind":"poster","location":"a","preview":null}]`, `[{"kind":"poster","location":"a","season":-1}]`, `[{"kind":"poster","location":"a","season":1.5}]`, `[{"kind":"poster","location":"a","season":1000001}]`, `[{"kind":"poster","location":"a","extra":true}]`},
	} {
		for _, raw := range invalid {
			_, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$1::jsonb WHERE item_id=$2::uuid AND field=$3`, raw, scope.ItemID, field)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23514" {
				t.Fatal("database accepted invalid movie extras", field, raw, err)
			}
		}
	}
	nfoMigrationDenied(t, f, "000036_nfo_movie_extras.down.sql")
	off := false
	flag, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "art", Locked: &off}})
	if err != nil || sourceRatingFact(t, flag, "art").NFOOrigin == nil || sourceRatingFact(t, flag, "art").NFOLockOrigin == nil {
		t.Fatal("artwork flag removed provenance", err)
	}
	patches := []domain.ItemMetadataFactPatch{{Field: "dateAdded", Value: json.RawMessage(`null`)}, {Field: "trailers", Value: json.RawMessage(`[]`)}, {Field: "art", Value: json.RawMessage(`null`)}}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 3, nil, patches)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range patches {
		fact := sourceRatingFact(t, clear, patch.Field)
		if string(fact.Value) != string(patch.Value) || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
			t.Fatal("movie extras clear retained proof", patch.Field)
		}
	}
	scope.Revision = 4
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range patches {
		fact := sourceRatingFact(t, review.Metadata, patch.Field)
		if string(fact.Value) != string(patch.Value) || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
			t.Fatal("movie extras review replaced manual clear", patch.Field)
		}
	}
}

func TestNFOMovieExtrasPublishedCollectionRoundTripAndMissingLocks(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	fields.Version = domain.NFOItemCollectionFieldsVersion
	fields.Collection = &domain.NFOCollection{Name: "Collection", Overview: "Plot"}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 43)
	nfoMigrateVersion(t, f, "down", 42)
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "down", 36)
	nfoMigrateVersion(t, f, "down", 35)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("movie extras migration changed published collection", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.Collection = domain.NFOItemMovieFieldsVersion, nil, nil
	fields.LockedFields = []string{"DateCreated", "RemoteTrailers", "Images"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dateAdded", "trailers", "art"} {
		fact := sourceRatingFact(t, locked.Metadata, name)
		if fact.Value != nil || fact.Source != "existing" || fact.UpdatedAt != nil || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil || fact.NFOLockOrigin.Projection != domain.NFOItemMovieFieldsVersion {
			t.Fatal("missing movie value lock invented provenance", name)
		}
	}
	if !reflect.DeepEqual(sourceRatingFact(t, after, "collection"), sourceRatingFact(t, locked.Metadata, "collection")) {
		t.Fatal("movie lock changed published collection")
	}
	nfoMigrationDenied(t, f, "000036_nfo_movie_extras.down.sql")
}

func TestNFOMovieExtrasManualClearPreventsDowngrade(t *testing.T) {
	for _, field := range []string{"dateAdded", "trailers", "art"} {
		t.Run(field, func(t *testing.T) {
			f, scope, _ := nfoItemApplyFixture(t)
			if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 1, nil, []domain.ItemMetadataFactPatch{{Field: field, Value: json.RawMessage(`null`)}}); err != nil {
				t.Fatal(err)
			}
			nfoMigrationDenied(t, f, "000036_nfo_movie_extras.down.sql")
		})
	}
}
