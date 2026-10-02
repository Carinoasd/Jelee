package postgres

import (
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestMetadataImagePreferencePersistenceAndLegacyUpdates(t *testing.T) {
	f := newJobFixture(t)
	id := f.registration.Library.ID
	languages := []string{"en", "null"}
	v, err := f.s.UpdateMetadataPreferences(f.ctx, f.a, id, "ja-JP", 1, languages)
	if err != nil || !reflect.DeepEqual(v.ImageLanguages, languages) {
		t.Fatal("image preference update failed", err)
	}
	languages[0] = "zh"
	v.ImageLanguages[0] = "ja"
	v, err = f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || !reflect.DeepEqual(v.ImageLanguages, []string{"en", "null"}) {
		t.Fatal("caller mutated persistent preference")
	}
	v, err = f.s.UpdateMetadataPreferences(f.ctx, f.a, id, "zh-TW", 2)
	if err != nil || v.Revision != 3 || !reflect.DeepEqual(v.ImageLanguages, []string{"en", "null"}) {
		t.Fatal("legacy update discarded image preference", err)
	}
	if _, err = f.s.UpdateMetadataPreferences(f.ctx, f.a, id, "en-US", 2, []string{"zh"}); err != domain.ErrConflict {
		t.Fatal("stale image update accepted")
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='library.metadata_preferences_changed' AND target_id=$1::uuid AND after_state->'imageLanguages'='["en","null"]'::jsonb`, id).Scan(&count); err != nil || count != 2 {
		t.Fatal("image preference audit missing", err)
	}
	nfoMigrationDenied(t, f, "000020_metadata_image_languages.down.sql")
	again, err := f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || !reflect.DeepEqual(v, again) {
		t.Fatal("denied image downgrade changed preferences")
	}
}

func TestMetadataImagePreferenceMigrationAndConstraints(t *testing.T) {
	f := newJobFixture(t, legacyMigrationAt44)
	id := f.registration.Library.ID
	nfoMigrateVersion(t, f, "down", 43)
	nfoMigrateVersion(t, f, "down", 42)
	nfoMigrateVersion(t, f, "down", 41)
	nfoMigrateVersion(t, f, "down", 40)
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
	nfoMigrateVersion(t, f, "down", 25)
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET metadata_language='ja-JP',metadata_preferences_revision=2 WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	v, err := f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || v.Language != "ja-JP" || v.Revision != 2 || !reflect.DeepEqual(v.ImageLanguages, domain.DefaultMetadataImageLanguages("zh-CN")) {
		t.Fatal("upgrade changed old preferences", err)
	}
	for _, expression := range []string{`ARRAY[]::text[]`, `ARRAY['en','en']`, `ARRAY['fr']`, `ARRAY['en',NULL]`, `ARRAY[['en','null']]`, `'[0:1]={en,null}'::text[]`} {
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET metadata_image_languages=`+expression+` WHERE id=$1::uuid`, id); err == nil {
			t.Fatal("invalid database preference accepted", expression)
		}
	}
	nfoMigrationDenied(t, f, "000020_metadata_image_languages.down.sql")
}
