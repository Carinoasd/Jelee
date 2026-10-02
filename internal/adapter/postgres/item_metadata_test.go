package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.ItemMetadataRepository = (*Store)(nil)

func metadataItem(t *testing.T, f jobFixture) string {
	t.Helper()
	var id string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Original title','Movie') RETURNING id::text`, f.registration.Library.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestItemMetadataPersistenceConflictAndAudit(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	value, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || value.Revision != 1 || value.Fields[0].Source != "existing" || value.Fields[0].UpdatedAt != nil {
		t.Fatal("initial read", err, value)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_metadata_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read created state", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, title := range []string{"Manual A", "Manual B"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			_, err := f.s.UpdateItemMetadata(f.ctx, f.a, item, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title}})
			results <- err
		}(title)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent metadata revision protection missing", success, conflict)
	}
	empty := ""
	lock := true
	value, err = f.s.UpdateItemMetadata(f.ctx, f.a, item, 2, []domain.ItemMetadataPatch{{Field: "overview", Value: &empty, Locked: &lock}, {Field: "title", Locked: &lock}})
	if err != nil || value.Revision != 3 || value.Fields[0].Source != "manual" || !value.Fields[0].Locked || value.Fields[1].Value != "" || value.Fields[1].Source != "manual" || !value.Fields[1].Locked {
		t.Fatal("manual clear or locks missing", err, value)
	}
	*value.Fields[0].UpdatedAt = (*value.Fields[0].UpdatedAt).AddDate(1, 0, 0)
	again, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || reflect.DeepEqual(value, again) {
		t.Fatal("timestamp ownership", err)
	}
	catalog, err := f.s.GetItem(f.ctx, f.a.UserID, item)
	if err != nil || catalog.Title != again.Fields[0].Value {
		t.Fatal("catalog title not synchronized", err)
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.metadata_changed' AND target_id=$1::uuid`, item).Scan(&count); err != nil || count != 2 {
		t.Fatal("metadata audit mismatch", count, err)
	}
	nfoMigrationDenied(t, f, "000021_item_metadata.down.sql")
}

func TestItemMetadataAuthorizationCancellationAndMigration(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
	value, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || value.Fields[0].Value != "Original title" || value.Revision != 1 {
		t.Fatal("upgrade altered original", err)
	}
	user, _, err := f.s.CreateUser(f.ctx, f.a, accountInput("item-editor-user"), "item-editor-user")
	if err != nil {
		t.Fatal(err)
	}
	actor := accountActor(accountLogin(t, f.ctx, f.s, user.Name))
	title := "Forbidden"
	if _, err = f.s.ItemMetadata(f.ctx, actor, item); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("nonadmin read", err)
	}
	if _, err = f.s.UpdateItemMetadata(f.ctx, actor, item, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title}}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("nonadmin write", err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err = f.s.UpdateItemMetadata(ctx, f.a, item, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title}}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled update", err)
	}
	if _, err = f.s.ItemMetadata(f.ctx, f.a, "11111111-1111-4111-8111-111111111111"); err != domain.ErrNotFound {
		t.Fatal("missing item", err)
	}
	var count int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_metadata_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied operations created state", err)
	}
	// SQL failure after the revision insert must roll back state and the catalog.
	if _, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION reject_metadata_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$; CREATE TRIGGER reject_metadata_update BEFORE INSERT ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION reject_metadata_update()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.UpdateItemMetadata(f.ctx, f.a, item, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title}}); err == nil {
		t.Fatal("injected failure accepted")
	}
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_metadata_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial state survived", err)
	}
	again, err := f.s.GetItem(f.ctx, f.a.UserID, item)
	if err != nil || again.Title != "Original title" {
		t.Fatal("partial catalog update survived", err)
	}
}

func TestItemMetadataSQLConstraints(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2)`, item); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ field, value, source string }{{"unknown", "x", "manual"}, {"title", "", "manual"}, {"title", " ", "manual"}, {"date", "2023-02-29", "manual"}, {"date", "0000-01-01", "manual"}, {"overview", "x", "TMDB"}} {
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES($1::uuid,$2,$3,$4,false,now())`, item, c.field, c.value, c.source); err == nil {
			t.Fatal("SQL invalid metadata accepted", c.field, c.source)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES($1::uuid,'title',repeat('界',342),'manual',false,now())`, item); err == nil {
		t.Fatal("SQL oversized UTF8 title accepted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES($1::uuid,'overview',repeat('x',16385),'manual',false,now())`, item); err == nil {
		t.Fatal("SQL oversized overview accepted")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES($1::uuid,'date','2024-02-29','manual',false,now())`, item); err != nil {
		t.Fatal("SQL valid leap date rejected", err)
	}
	nfoMigrationDenied(t, f, "000021_item_metadata.down.sql")
	for _, original := range []string{strings.Repeat("界", 400), " \t "} {
		t.Run(map[bool]string{true: "multibyte", false: "whitespace"}[len(original) > 1024], func(t *testing.T) {
			legacy := metadataItem(t, f)
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE items SET title=$2 WHERE id=$1::uuid`, legacy, original); err != nil {
				t.Fatal(err)
			}
			lock := true
			value, err := f.s.UpdateItemMetadata(f.ctx, f.a, legacy, 1, []domain.ItemMetadataPatch{{Field: "title", Locked: &lock}})
			if err != nil || value.Revision != 2 || value.Fields[0].Value != original || value.Fields[0].Source != "existing" || !value.Fields[0].Locked {
				t.Fatal("legacy title lock lost original value", err, value)
			}
			catalog, err := f.s.GetItem(f.ctx, f.a.UserID, legacy)
			if err != nil || catalog.Title != original {
				t.Fatal("legacy catalog title changed", err)
			}
			if _, err = f.s.UpdateItemMetadata(f.ctx, f.a, legacy, 2, []domain.ItemMetadataPatch{{Field: "title", Value: &original}}); err != domain.ErrInvalid {
				t.Fatal("legacy exception permitted invalid new manual value", err)
			}
		})
	}
}
