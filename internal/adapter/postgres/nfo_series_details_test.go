package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func seriesDetailsFixture(t *testing.T) (jobFixture, domain.NFOItemScope, domain.NFOItemFields) {
	t.Helper()
	f, scope, fields := nfoItemApplyFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='Series' WHERE id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	scope.Kind, fields.Kind = "Series", "Series"
	return f, scope, fields
}

func TestNFOSeriesDetailsAtomicPersistenceAndManualClear(t *testing.T) {
	f, scope, fields := seriesDetailsFixture(t)
	unknown, zero := -1, 0
	fields.Version, fields.LockData = domain.NFOItemSeriesFieldsVersion, true
	fields.SeriesDetails = &domain.NFOSeriesDetails{SeasonCount: &unknown, EpisodeCount: &zero, Status: "Continuing", AirsDayOfWeek: "Friday", AirsTime: "9 PM"}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	update := tmdbMetadataUpdate(16, true)
	update.Resource = "series"
	for i := range update.Fields {
		update.Fields[i].Origin.Resource = "series"
		update.Fields[i].Origin.SourceURL = domain.TMDBSourceURL("series", update.ProviderID)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_series_details() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_series_details BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_series_details()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), update)
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_series_details ON `+table+`; DROP FUNCTION reject_series_details()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("series fusion left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyTMDBWithNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid), update)
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 24 {
		t.Fatal("series details and global locks failed", err)
	}
	expected := map[string]string{"seasonCount": `-1`, "episodeCount": `0`, "seriesStatus": `"Continuing"`, "airsDayOfWeek": `"Friday"`, "airsTime": `"9 PM"`}
	for name, value := range expected {
		fact := sourceRatingFact(t, result.Metadata, name)
		if string(fact.Value) != value || fact.Source != "nfo" || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
			t.Fatal("series value or proof lost", name)
		}
	}
	for field, invalid := range map[string][]string{
		"seasonCount":   {`null`, `-2`, `1000001`, `1.5`, `"1"`},
		"episodeCount":  {`null`, `-2`, `1000001`, `1.5`, `"1"`},
		"seriesStatus":  {`null`, `""`, `"\t"`, `"\u00a0"`, `0`, `[]`},
		"airsDayOfWeek": {`null`, `""`, `"\u3000"`, `{}`},
		"airsTime":      {`null`, `""`, `"\t"`, `0`},
	} {
		for _, raw := range invalid {
			_, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$1::jsonb WHERE item_id=$2::uuid AND field=$3`, raw, scope.ItemID, field)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23514" {
				t.Fatal("database accepted invalid series value", field, raw, err)
			}
		}
	}
	nfoMigrationDenied(t, f, "000037_nfo_series_details.down.sql")
	patches := []domain.ItemMetadataFactPatch{}
	for _, field := range domain.ItemMetadataSeriesFieldNames() {
		patches = append(patches, domain.ItemMetadataFactPatch{Field: field, Value: json.RawMessage(`null`)})
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, patches)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range patches {
		fact := sourceRatingFact(t, clear, patch.Field)
		if string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
			t.Fatal("series clear retained proofs", patch.Field)
		}
	}
	scope.Revision = 3
	review, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range patches {
		fact := sourceRatingFact(t, review.Metadata, patch.Field)
		if string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
			t.Fatal("series review overwrote manual clear", patch.Field)
		}
	}
}

func TestNFOSeriesDetailsPublishedMovieRoundTripAndMissingLocks(t *testing.T) {
	f, scope, fields := seriesDetailsFixture(t)
	fields.Version = domain.NFOItemMovieFieldsVersion
	fields.DateAdded = "2024-02-29"
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(result.Metadata, after) {
		t.Fatal("series migration changed published movie fields", err)
	}
	scope.Revision = 2
	fields.Version, fields.Fields, fields.DateAdded = domain.NFOItemSeriesFieldsVersion, nil, ""
	fields.LockedFields = []string{"SeasonCount", "EpisodeCount", "Status", "AirDays", "AirTime"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range domain.ItemMetadataSeriesFieldNames() {
		fact := sourceRatingFact(t, locked.Metadata, name)
		if fact.Value != nil || fact.Source != "existing" || fact.UpdatedAt != nil || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil || fact.NFOLockOrigin.Projection != domain.NFOItemSeriesFieldsVersion {
			t.Fatal("missing series lock invented value provenance", name)
		}
	}
	if !reflect.DeepEqual(sourceRatingFact(t, after, "dateAdded"), sourceRatingFact(t, locked.Metadata, "dateAdded")) {
		t.Fatal("series lock changed published date")
	}
	nfoMigrationDenied(t, f, "000037_nfo_series_details.down.sql")
}

func TestNFOSeriesDetailsManualClearPreventsDowngrade(t *testing.T) {
	for _, field := range domain.ItemMetadataSeriesFieldNames() {
		t.Run(field, func(t *testing.T) {
			f, scope, _ := seriesDetailsFixture(t)
			if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 1, nil, []domain.ItemMetadataFactPatch{{Field: field, Value: json.RawMessage(`null`)}}); err != nil {
				t.Fatal(err)
			}
			nfoMigrationDenied(t, f, "000037_nfo_series_details.down.sql")
		})
	}
}
