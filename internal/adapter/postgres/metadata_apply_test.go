package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"reflect"
	"testing"
	"time"
)

var _ app.MetadataApplyRepository = (*Store)(nil)

func tmdbMetadataUpdate(id int32, replace bool) domain.TMDBMetadataUpdate {
	origin := domain.MetadataProviderOrigin{Resource: "movie", ProviderID: id, SourceURL: domain.TMDBSourceURL("movie", id), RequestedLanguage: "zh-CN", FetchedAt: time.Now().UTC()}
	update := domain.TMDBMetadataUpdate{Resource: "movie", ProviderID: id, ReplaceExistingTitle: replace}
	for _, f := range []struct{ field, value string }{{"title", "Provider title"}, {"originalTitle", "Original provider title"}, {"overview", "Provider overview"}, {"date", "2024-02-29"}} {
		fieldOrigin := origin
		if f.field == "overview" {
			fieldOrigin.RequestedLanguage = "en-US"
		}
		update.Fields = append(update.Fields, domain.MetadataProviderField{Field: f.field, Value: f.value, Origin: fieldOrigin})
	}
	return update
}

func TestTMDBMetadataPersistencePriorityAndManualTakeover(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	result, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, false))
	if err != nil || result.Metadata.Revision != 2 || len(result.Applied) != 3 || !reflect.DeepEqual(result.Skipped, []domain.MetadataFieldSkip{{Field: "title", Reason: "existing"}}) {
		t.Fatal("legacy source protection", err, result)
	}
	result, err = f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 2, tmdbMetadataUpdate(12, true))
	if err != nil || len(result.Applied) != 4 || len(result.Skipped) != 0 || result.Metadata.Fields[0].Source != "tmdb" || result.Metadata.Fields[0].ProviderOrigin.ProviderID != 12 || result.Metadata.Fields[2].ProviderOrigin.RequestedLanguage != "en-US" {
		t.Fatal("provider origin not persisted", err, result)
	}
	lock := true
	empty := ""
	manual, err := f.s.UpdateItemMetadata(f.ctx, f.a, item, 3, []domain.ItemMetadataPatch{{Field: "title", Locked: &lock}, {Field: "overview", Value: &empty}})
	if err != nil || !manual.Fields[0].Locked || manual.Fields[0].ProviderOrigin.ProviderID != 12 || manual.Fields[2].Source != "manual" || manual.Fields[2].ProviderOrigin != nil {
		t.Fatal("manual source transition", err, manual)
	}
	before := domain.CloneItemMetadata(manual)
	update := tmdbMetadataUpdate(13, true)
	update.Fields[0].Value = "Replacement title"
	result, err = f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 4, update)
	if err != nil || result.Metadata.Revision != 5 || len(result.Applied) != 2 || !reflect.DeepEqual(result.Skipped, []domain.MetadataFieldSkip{{Field: "title", Reason: "locked"}, {Field: "overview", Reason: "manual"}}) || !reflect.DeepEqual(result.Metadata.Fields[0], before.Fields[0]) || !reflect.DeepEqual(result.Metadata.Fields[2], before.Fields[2]) {
		t.Fatal("TMDB overwrote locked or manual field", err, result)
	}
	catalog, err := f.s.GetItem(f.ctx, f.a.UserID, item)
	if err != nil || catalog.Title != "Provider title" {
		t.Fatal("catalog provider title differs", err)
	}
	if _, err = f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 4, update); err != domain.ErrConflict {
		t.Fatal("stale provider write", err)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.tmdb_metadata_applied' AND target_id=$1::uuid`, item).Scan(&count); err != nil || count != 3 {
		t.Fatal("provider audit mismatch", count, err)
	}
	nfoMigrationDenied(t, f, "000022_tmdb_metadata_origin.down.sql")
}

func TestTMDBMetadataMigrationConstraintsAndAtomicFailure(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET kind='HomeVideo' WHERE id=$1::uuid`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_provider_overview() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.field='overview' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_provider_overview BEFORE INSERT ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION reject_provider_overview()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true)); err == nil {
		t.Fatal("injected provider failure accepted")
	}
	value, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || value.Revision != 1 || value.Kind != "HomeVideo" || len(value.Fields) != 1 || value.Fields[0].Value != "Original title" {
		t.Fatal("partial provider write survived", err, value)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_provider_overview ON item_metadata_fields; DROP FUNCTION reject_provider_overview()`); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true))
	if err != nil || result.Metadata.Kind != "Movie" {
		t.Fatal("confirmed movie classification not applied", err)
	}
	for _, sql := range []string{`UPDATE item_metadata_fields SET provider_source_url='https://example.com' WHERE field='title'`, `UPDATE item_metadata_fields SET provider_id=NULL WHERE field='title'`, `UPDATE item_metadata_fields SET provider_resource='series' WHERE field='title'`, `UPDATE item_metadata_fields SET provider_language='fr-FR' WHERE field='title'`, `UPDATE item_metadata_fields SET provider_fetched_at='infinity' WHERE field='title'`, `UPDATE item_metadata_fields SET source='manual' WHERE field='title'`} {
		if _, err = f.s.Pool.Exec(f.ctx, sql); err == nil {
			t.Fatal("SQL invalid provenance accepted", sql)
		}
	}
	// Manual takeovers clear provenance; a clean provider-only downgrade preserves
	// these local values and all revision state from schema21.
	patches := []domain.ItemMetadataPatch{}
	for _, field := range result.Metadata.Fields {
		v := field.Value
		patches = append(patches, domain.ItemMetadataPatch{Field: field.Field, Value: &v})
	}
	if _, err = f.s.UpdateItemMetadata(f.ctx, f.a, item, 2, patches); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	value, err = f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || value.Revision != 3 || value.Fields[0].Source != "manual" || value.Fields[0].ProviderOrigin != nil {
		t.Fatal("provider downgrade lost manual value", err)
	}
}
