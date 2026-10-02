package postgres

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nfoItemApplyFixture(t *testing.T) (jobFixture, domain.NFOItemScope, domain.NFOItemFields) {
	t.Helper()
	f := newJobFixture(t)
	item := metadataItem(t, f)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mkv','video/x-matroska')`, item, f.registration.Library.ID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	fields := domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: "Movie", Identity: domain.DefaultNFOIdentity(), Stamp: domain.NFOStamp{Size: 123, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{{Field: "title", Value: "NFO title"}, {Field: "originalTitle", Value: "Original NFO"}, {Field: "overview", Value: "NFO overview"}, {Field: "date", Value: "2024-02-29"}}}
	return f, scope, fields
}

func TestNFOItemPersistenceManualPriorityAndSeparateLocks(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	empty := ""
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 1, []domain.ItemMetadataPatch{{Field: "overview", Value: &empty}}); err != nil {
		t.Fatal(err)
	}
	scope.Revision = 2
	fields.LockedFields = []string{"Name"}
	result, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields)
	if err != nil || result.Metadata.Revision != 3 || len(result.Applied) != 3 || !reflect.DeepEqual(result.Skipped, []domain.MetadataFieldSkip{{Field: "overview", Reason: "manual"}}) {
		t.Fatal("manual empty overwritten", err, result)
	}
	title := result.Metadata.Fields[0]
	if title.Source != "nfo" || title.Locked || title.NFOOrigin == nil || !title.NFOOrigin.Locked || title.NFOOrigin.SourceID != scope.SourceID || title.NFOOrigin.SHA256 != fields.Stamp.SHA256 || title.ProviderOrigin != nil {
		t.Fatal("NFO origin not persisted")
	}
	if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, scope.ItemID); err != nil || catalog.Title != "NFO title" {
		t.Fatal("NFO catalog differs", err)
	}
	unlock := false
	value, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 3, []domain.ItemMetadataPatch{{Field: "title", Locked: &unlock}})
	if err != nil || value.Fields[0].NFOOrigin == nil || !value.Fields[0].NFOOrigin.Locked {
		t.Fatal("manual lock change discarded NFO origin", err)
	}
	scope.Revision = 4
	fields.Fields[0].Value = "Replacement NFO"
	fields.LockedFields = nil
	result, err = f.s.ApplyItemNFO(f.ctx, f.a, scope, fields)
	if err != nil || result.Metadata.Fields[0].Value != "NFO title" || len(result.Skipped) != 2 || result.Skipped[0].Reason != "locked" {
		t.Fatal("NFO overwrote prior NFO lock", err)
	}
	nfoMigrationDenied(t, f, "000023_nfo_item_origin.down.sql")
	manual := "Owner title"
	value, err = f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 5, []domain.ItemMetadataPatch{{Field: "title", Value: &manual}, {Field: "originalTitle", Value: &empty}, {Field: "date", Value: &empty}})
	if err != nil || value.Fields[0].Source != "manual" || value.Fields[0].NFOOrigin != nil {
		t.Fatal("manual takeover retained origin", err)
	}
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	value, err = f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || value.Revision != 6 || value.Fields[0].Value != manual {
		t.Fatal("clean NFO migration changed manual data", err)
	}
}

func TestNFOItemAtomicRollbackAndScopeInvalidation(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	if _, err := f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_nfo_overview() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.field='overview' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_nfo_overview BEFORE INSERT ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION reject_nfo_overview()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); err == nil {
		t.Fatal("injected failure accepted")
	}
	value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || value.Revision != 1 || len(value.Fields) != 1 || value.Fields[0].Value != "Original title" {
		t.Fatal("partial NFO transaction survived", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DROP TRIGGER reject_nfo_overview ON item_metadata_fields; DROP FUNCTION reject_nfo_overview()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_generation=3 WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); err != domain.ErrConflict {
		t.Fatal("changed NFO generation committed", err)
	}
	scope.Generation = 3
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE media_sources SET relative_path='replacement.mkv' WHERE id=$1::uuid`, scope.SourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); err != domain.ErrConflict {
		t.Fatal("changed source committed", err)
	}
	scope.MediaPath = "replacement.mkv"
	scope.Source.RelativePath = "replacement.nfo"
	if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`null`, `{}`, `{"sourceId":null}`, `{"sourceId":"fake"}`} {
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_fields SET nfo_origin=$2::jsonb WHERE item_id=$1::uuid AND field='title'`, scope.ItemID, bad); err == nil {
			t.Fatal("invalid NFO origin SQL accepted")
		}
	}
	for _, bad := range []struct{ key, value string }{
		{"sourceId", `null`}, {"rootId", `"invalid"`}, {"generation", `0`}, {"generation", `1.5`}, {"generation", `9223372036854775808`},
		{"sha256", `"invalid"`}, {"identityDigest", `"invalid"`}, {"projection", `"unknown"`}, {"locked", `"true"`},
		{"readAt", `"infinity"`}, {"readAt", `"0000-01-01T00:00:00Z"`}, {"readAt", `null`},
	} {
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_fields SET nfo_origin=jsonb_set(nfo_origin,ARRAY[$2::text],$3::jsonb) WHERE item_id=$1::uuid AND field='title'`, scope.ItemID, bad.key, bad.value); err == nil {
			t.Fatal("invalid NFO origin component accepted", bad.key)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_fields SET nfo_origin=nfo_origin||'{"extra":true}'::jsonb WHERE item_id=$1::uuid`, scope.ItemID); err == nil {
		t.Fatal("unknown NFO origin property accepted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE item_metadata_fields SET source='manual' WHERE item_id=$1::uuid`, scope.ItemID); err == nil {
		t.Fatal("manual source retained NFO origin")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	scope.Revision = 2
	if _, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields); err != domain.ErrUnauthenticated {
		t.Fatal("revoked NFO writer accepted", err)
	}
}

func TestNFOItemSourceRemainsAboveTMDBWhenReadingDisabled(t *testing.T) {
	f, scope, fields := nfoItemApplyFixture(t)
	first, err := f.s.ApplyItemNFO(f.ctx, f.a, scope, fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='off',nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, scope.ItemID, 2, tmdbMetadataUpdate(12, true))
	if err != nil || len(result.Applied) != 0 || len(result.Skipped) != 4 || result.Metadata.Revision != 3 {
		t.Fatal("TMDB overwrote persisted NFO source", err)
	}
	for i, field := range first.Metadata.Fields {
		if result.Skipped[i].Reason != "nfo" || !reflect.DeepEqual(result.Metadata.Fields[i], field) {
			t.Fatal("TMDB overwrote persisted NFO source")
		}
	}
}
