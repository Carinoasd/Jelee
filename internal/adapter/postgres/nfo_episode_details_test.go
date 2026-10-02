package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func episodeDetailsFixture(t *testing.T) (jobFixture, domain.NFOItemScope, domain.NFOItemFields) {
	t.Helper()
	f, scope, fields := nfoItemApplyFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='Episode' WHERE id=$1::uuid`, scope.ItemID); err != nil {
		t.Fatal(err)
	}
	scope.Kind, fields.Kind = "Episode", "Episode"
	return f, scope, fields
}

func TestNFOEpisodeDetailsAtomicPersistenceAndManualClear(t *testing.T) {
	f, scope, fields := episodeDetailsFixture(t)
	unknown, zero := 1, 0
	fields.Version, fields.LockData = domain.NFOItemEpisodeFieldsVersion, true
	fields.EpisodeDetails = &domain.NFOEpisodeDetails{SeasonNumber: &zero, EpisodeNumber: &unknown, DisplaySeason: &zero, DisplayEpisode: &unknown, Aired: "2024-02-29", ShowTitle: "Series"}
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_episode_details() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_episode_details BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_episode_details()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_episode_details ON `+table+`; DROP FUNCTION reject_episode_details()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("episode apply left partial metadata", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || result.Metadata.Revision != 2 || len(result.Metadata.Facts) != 25 {
		t.Fatal("episode details and global locks failed", err)
	}
	expected := map[string]string{"seasonNumber": `0`, "episodeNumber": `1`, "displaySeason": `0`, "displayEpisode": `1`, "aired": `"2024-02-29"`, "showTitle": `"Series"`}
	for name, value := range expected {
		fact := sourceRatingFact(t, result.Metadata, name)
		if string(fact.Value) != value || fact.Source != "nfo" || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil {
			t.Fatal("episode value or proof lost", name)
		}
	}
	for field, invalid := range map[string][]string{
		"seasonNumber":   {`null`, `-1`, `1000001`, `1.5`, `"1"`},
		"episodeNumber":  {`null`, `-1`, `1000001`, `1.5`, `"1"`},
		"displaySeason":  {`null`, `-1`, `1000001`, `1.5`, `"1"`},
		"displayEpisode": {`null`, `-1`, `1000001`, `1.5`, `"1"`},
		"aired":          {`null`, `"2023-02-29"`, `0`, `[]`},
		"showTitle":      {`null`, `""`, `"\t"`, `"\u00a0"`, `"\u3000"`, `0`, `{}`},
	} {
		for _, raw := range invalid {
			_, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_facts SET value=$1::jsonb WHERE item_id=$2::uuid AND field=$3`, raw, scope.ItemID, field)
			var pgError *pgconn.PgError
			if !errors.As(err, &pgError) || pgError.Code != "23514" {
				t.Fatal("database accepted invalid episode value", field, raw, err)
			}
		}
	}
	nfoMigrationDenied(t, f, "000038_nfo_episode_details.down.sql")
	patches := []domain.ItemMetadataFactPatch{}
	for _, field := range domain.ItemMetadataEpisodeFieldNames() {
		patches = append(patches, domain.ItemMetadataFactPatch{Field: field, Value: json.RawMessage(`null`)})
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, patches)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range patches {
		fact := sourceRatingFact(t, clear, patch.Field)
		if string(fact.Value) != "null" || fact.Source != "manual" || fact.NFOOrigin != nil || fact.NFOLockOrigin != nil {
			t.Fatal("episode clear retained proofs", patch.Field)
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
			t.Fatal("episode review overwrote manual clear", patch.Field)
		}
	}
}

func TestNFOEpisodeDetailsMissingLocks(t *testing.T) {
	f, scope, fields := episodeDetailsFixture(t)
	fields.Version, fields.Fields = domain.NFOItemEpisodeFieldsVersion, nil
	fields.LockedFields = []string{"ParentIndexNumber", "IndexNumber", "DisplaySeason", "DisplayEpisode", "Aired", "SeriesName"}
	locked, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range domain.ItemMetadataEpisodeFieldNames() {
		fact := sourceRatingFact(t, locked.Metadata, name)
		if fact.Value != nil || fact.Source != "existing" || fact.UpdatedAt != nil || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil || fact.NFOLockOrigin.Projection != domain.NFOItemEpisodeFieldsVersion {
			t.Fatal("missing episode lock invented value provenance", name)
		}
	}

	nfoMigrationDenied(t, f, "000038_nfo_episode_details.down.sql")
}

func TestNFOEpisodeDetailsManualClearPreventsDowngrade(t *testing.T) {
	for _, field := range domain.ItemMetadataEpisodeFieldNames() {
		t.Run(field, func(t *testing.T) {
			f, scope, _ := episodeDetailsFixture(t)
			if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 1, nil, []domain.ItemMetadataFactPatch{{Field: field, Value: json.RawMessage(`null`)}}); err != nil {
				t.Fatal(err)
			}
			nfoMigrationDenied(t, f, "000038_nfo_episode_details.down.sql")
		})
	}
}

func TestNFOEpisodeDetailsPublishedSeriesRoundTrip(t *testing.T) {
	f, scope, fields := seriesDetailsFixture(t)
	count := -1
	fields.Version = domain.NFOItemSeriesFieldsVersion
	fields.SeriesDetails = &domain.NFOSeriesDetails{SeasonCount: &count, Status: "Continuing"}
	before, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
	nfoMigrateVersion(t, f, "down", 39)
	nfoMigrateVersion(t, f, "down", 38)
	nfoMigrateVersion(t, f, "down", 37)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	after, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || !reflect.DeepEqual(before.Metadata, after) {
		t.Fatal("episode migration changed published series metadata", err)
	}
}
