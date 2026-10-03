package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func seasonDirectoryFixture(t *testing.T) (nfoFixture, domain.NFOItemScope, domain.NFOItemFields, string) {
	t.Helper()
	f := newNFOFixture(t)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Season 1"), 0700); err != nil {
		t.Fatal(err)
	}
	series, err := f.s.ImportDirectory(f.ctx, "primary", root, ".", "Series", "Series", "")
	if err != nil {
		t.Fatal(err)
	}
	season, err := f.s.ImportDirectory(f.ctx, "primary", root, "Season 1", "Season", "Season", series)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`<season><title>NFO season</title><seasonnumber>0</seasonnumber><lockdata>true</lockdata></season>`)
	if err := os.WriteFile(filepath.Join(root, "Season 1", "season.nfo"), content, 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, season, 1)
	if err != nil {
		t.Fatal(err)
	}
	reader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	fields, err := reader.ReadItemFields(f.ctx, scope.Source, "Season")
	if err != nil {
		t.Fatal(err)
	}
	return f, scope, fields, series
}

func directoryIntegrityCounts(t *testing.T, f nfoFixture) [6]int {
	t.Helper()
	var counts [6]int
	for i, table := range []string{"items", "media_sources", "item_directory_sources", "item_parent_links", "audit_logs", "item_nfo_observations"} {
		if err := f.s.Pool.QueryRow(f.ctx, "SELECT count(*) FROM "+table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestDirectorySeasonAtomicFactsAndManualClear(t *testing.T) {
	f, scope, fields, _ := seasonDirectoryFixture(t)
	before, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	counts := directoryIntegrityCounts(t, f)
	for _, table := range []string{"item_metadata_fields", "item_metadata_facts", "item_nfo_field_locks", "item_nfo_observations", "audit_logs"} {
		t.Run(table, func(t *testing.T) {
			if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_season_apply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_season_apply BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_season_apply()`); err != nil {
				t.Fatal(err)
			}
			_, applyErr := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
			after, readErr := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
			if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_season_apply ON `+table+`; DROP FUNCTION reject_season_apply()`); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil || readErr != nil || !reflect.DeepEqual(before, after) || directoryIntegrityCounts(t, f) != counts {
				t.Fatal("season failure left partial values or audit", applyErr, readErr)
			}
		})
	}
	result, err := f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil || len(result.Metadata.Facts) != 20 {
		t.Fatal("season global lock omitted supported fields", err)
	}
	fact := sourceRatingFact(t, result.Metadata, "seasonNumber")
	if string(fact.Value) != "0" || fact.NFOOrigin == nil || fact.NFOLockOrigin == nil || fact.NFOOrigin.Projection != domain.NFOItemSeasonFieldsVersion {
		t.Fatal("season value or source proof missing")
	}
	clear, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, scope.ItemID, 2, nil, []domain.ItemMetadataFactPatch{{Field: "seasonNumber", Value: json.RawMessage(`null`)}})
	if err != nil || string(sourceRatingFact(t, clear, "seasonNumber").Value) != "null" {
		t.Fatal("season manual clear failed", err)
	}
	scope.Revision = 3
	result, err = f.s.ApplyItemNFOObservation(f.ctx, f.a, scope, nfoObservationState(scope, fields, domain.NFOItemObservedValid))
	if err != nil {
		t.Fatal(err)
	}
	fact = sourceRatingFact(t, result.Metadata, "seasonNumber")
	if fact.Source != "manual" || string(fact.Value) != "null" || fact.NFOOrigin != nil || fact.NFOLockOrigin == nil {
		t.Fatal("season NFO overwrote manual clear")
	}
	nfoMigrationDenied(t, f.jobFixture, "000039_directory_nfo.down.sql")
}

func TestDirectoryImportRejectsInvalidParentsAndRollsBack(t *testing.T) {
	f, scope, _, series := seasonDirectoryFixture(t)
	root := scope.Source.RootPath
	before := directoryIntegrityCounts(t, f)
	for _, input := range []struct{ library, relative, kind, parent string }{
		{"primary", "Season 1", "Season", series},
		{"primary", "Season 2", "Season", ""},
		{"primary", "Season 2", "Season", scope.ItemID},
		{"primary", "Another", "Series", series},
		{"primary", "../outside", "Series", ""},
		{"other-library", "Another", "Season", series},
	} {
		if _, err := f.s.ImportDirectory(f.ctx, input.library, root, input.relative, "Invalid", input.kind, input.parent); err == nil || directoryIntegrityCounts(t, f) != before {
			t.Fatal("invalid directory import left catalog state")
		}
	}
	for _, table := range []string{"items", "item_directory_sources", "item_parent_links", "audit_logs"} {
		if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_directory_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_directory_import BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_directory_import()`); err != nil {
			t.Fatal(err)
		}
		_, applyErr := f.s.ImportDirectory(f.ctx, "primary", root, "Season 2", "Season 2", "Season", series)
		if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_directory_import ON `+table+`; DROP FUNCTION reject_directory_import()`); err != nil {
			t.Fatal(err)
		}
		if applyErr == nil || directoryIntegrityCounts(t, f) != before {
			t.Fatal("directory import did not roll back", table)
		}
	}
	source, err := f.s.ImportVideoWithParent(f.ctx, "primary", root, "Season 1/Episode.mkv", "Episode", "video/x-matroska", "Episode", scope.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	var episode string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT item_id::text FROM media_sources WHERE id=$1::uuid`, source).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	item, err := f.s.GetItem(f.ctx, f.a.UserID, episode)
	if err != nil || item.ParentID != scope.ItemID {
		t.Fatal("episode lost season parent", err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `UPDATE item_parent_links SET parent_id=$1::uuid,parent_kind='Episode' WHERE item_id=$2::uuid`, episode, scope.ItemID)
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23514" {
		t.Fatal("database accepted invalid parent kind", err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `UPDATE item_parent_links SET library_id=gen_random_uuid() WHERE item_id=$1::uuid`, episode)
	if !errors.As(err, &pgError) || pgError.Code != "23503" {
		t.Fatal("database accepted cross-library link", err)
	}
	before = directoryIntegrityCounts(t, f)
	if _, err := f.s.ImportVideoWithParent(f.ctx, "primary", root, "Outside.mkv", "Outside", "video/x-matroska", "Episode", scope.ItemID); err == nil || directoryIntegrityCounts(t, f) != before {
		t.Fatal("episode outside parent directory accepted")
	}
	nfoMigrationDenied(t, f.jobFixture, "000039_directory_nfo.down.sql")
}

func TestDirectoryScopeRejectsMixedSources(t *testing.T) {
	f, scope, _, series := seasonDirectoryFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1,$2,$3,'ambiguous.mkv','video/x-matroska')`, series, scope.LibraryID, scope.RootID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ResolveItemNFO(f.ctx, f.a, series, 1); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("mixed directory and media sources were accepted", err)
	}
}
