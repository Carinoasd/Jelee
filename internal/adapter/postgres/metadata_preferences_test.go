package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.MetadataPreferencesRepository = (*Store)(nil)

func TestMetadataPreferencesPersistenceConflictAndAudit(t *testing.T) {
	f := newJobFixture(t)
	id := f.registration.Library.ID
	v, err := f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || v.Language != "zh-CN" || v.Revision != 1 {
		t.Fatalf("default=%+v error=%v", v, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, language := range []string{"zh-TW", "ja-JP"} {
		wg.Add(1)
		go func(language string) {
			defer wg.Done()
			_, err := f.s.UpdateMetadataPreferences(f.ctx, f.a, id, language, 1)
			results <- err
		}(language)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if err == domain.ErrConflict {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent update lost revision protection")
	}
	v, err = f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || v.Revision != 2 || v.Language == "zh-CN" {
		t.Fatal("update not persisted")
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='library.metadata_preferences_changed' AND target_id=$1::uuid AND before_state->>'revision'='1' AND after_state->>'revision'='2'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("successful update audit missing or stale update audited")
	}
	nfoMigrationDenied(t, f, "000019_metadata_language.down.sql")
	again, err := f.s.MetadataPreferences(f.ctx, f.a, id)
	if err != nil || !reflect.DeepEqual(again, v) {
		t.Fatal("denied downgrade changed preferences")
	}
}

func TestMetadataPreferencesAuthorizationAndValidation(t *testing.T) {
	f := newJobFixture(t)
	id := f.registration.Library.ID
	user, _, err := f.s.CreateUser(f.ctx, f.a, accountInput("metadata-user"), "metadata-user")
	if err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, f.ctx, f.s, user.Name))
	for _, read := range []bool{true, false} {
		var err error
		if read {
			_, err = f.s.MetadataPreferences(f.ctx, actor, id)
		} else {
			_, err = f.s.UpdateMetadataPreferences(f.ctx, actor, id, "en-US", 1)
		}
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatal("nonadmin metadata preference access", err)
		}
	}
	for _, language := range []string{"", "fr-FR", "en-US "} {
		if _, err = f.s.UpdateMetadataPreferences(f.ctx, f.a, id, language, 1); err != domain.ErrInvalid {
			t.Fatal("invalid language accepted")
		}
	}
	if _, err = f.s.UpdateMetadataPreferences(f.ctx, f.a, id, "en-US", 0); err != domain.ErrInvalid {
		t.Fatal("missing revision accepted")
	}
	if _, err = f.s.MetadataPreferences(f.ctx, f.a, "not-id"); err != domain.ErrInvalid {
		t.Fatal("invalid library accepted")
	}
	if _, err = f.s.MetadataPreferences(f.ctx, f.a, "00000000-0000-4000-8000-000000000001"); err != domain.ErrNotFound {
		t.Fatal("unknown library accepted", err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err = f.s.MetadataPreferences(ctx, f.a, id); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read accepted", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.UpdateMetadataPreferences(f.ctx, f.a, id, "en-US", 1); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatal("revoked session updated preference", err)
	}
}

func TestMetadataPreferencesMigrationDefaultAndCleanRollback(t *testing.T) {
	f := newJobFixture(t)
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
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	v, err := f.s.MetadataPreferences(f.ctx, f.a, f.registration.Library.ID)
	if err != nil || v.Revision != 1 || v.Language != "zh-CN" {
		t.Fatal("existing library default missing", err)
	}
	for _, query := range []string{`UPDATE libraries SET metadata_language='fr-FR'`, `UPDATE libraries SET metadata_preferences_revision=0`} {
		if _, err = f.s.Pool.Exec(f.ctx, query); err == nil {
			t.Fatal("database preference constraint missing")
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
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}
