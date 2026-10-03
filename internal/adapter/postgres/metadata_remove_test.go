package postgres

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func removeMediaSource(t *testing.T, f jobFixture, item, relative string) {
	t.Helper()
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska')`, item, f.registration.Library.ID, root, relative); err != nil {
		t.Fatal(err)
	}
}

func removeAuditCount(t *testing.T, f jobFixture, item string) int {
	t.Helper()
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.external_metadata_removed' AND target_id=$1::uuid`, item).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func removeField(value domain.ItemMetadata, name string) *domain.ItemMetadataField {
	for i := range value.Fields {
		if value.Fields[i].Field == name {
			return &value.Fields[i]
		}
	}
	return nil
}

func TestRemoveExternalMetadataKeepsNFOAndManualSources(t *testing.T) {
	f, scope, nfo := nfoItemApplyFixture(t)
	empty := ""
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, scope.ItemID, 1, []domain.ItemMetadataPatch{{Field: "overview", Value: &empty}}); err != nil {
		t.Fatal(err)
	}
	scope.Revision = 2
	nfo.Fields = []domain.NFOTextField{nfo.Fields[0], nfo.Fields[3]}
	applied, err := f.s.ApplyTMDBWithNFO(f.ctx, f.a, scope, nfo, tmdbMetadataUpdate(12, true))
	if err != nil || removeField(applied.Metadata, "originalTitle").Source != "tmdb" {
		t.Fatal("mixed fixture differs", err, applied)
	}
	result, err := f.s.RemoveExternalMetadata(f.ctx, f.a, scope.ItemID, 3)
	if err != nil || result.Metadata.Revision != 4 || !reflect.DeepEqual(result.Removed, []string{"originalTitle"}) || len(result.Skipped) != 0 {
		t.Fatal("removal result differs", err, result)
	}
	if removeField(result.Metadata, "originalTitle") != nil {
		t.Fatal("provider value survived")
	}
	for _, name := range []string{"title", "overview", "date"} {
		if !reflect.DeepEqual(removeField(result.Metadata, name), removeField(applied.Metadata, name)) {
			t.Fatal("local source changed", name)
		}
	}
	if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, scope.ItemID); err != nil || catalog.Title != "NFO title" {
		t.Fatal("NFO catalog title changed", err)
	}
	var before, after string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT before_state::text,after_state::text FROM audit_logs WHERE event='item.external_metadata_removed' AND target_id=$1::uuid`, scope.ItemID).Scan(&before, &after); err != nil {
		t.Fatal("exactly one removal audit expected", err)
	}
	if !strings.Contains(before, `"providerId": 12`) || strings.Contains(before+after, "Original provider title") {
		t.Fatal("audit must keep provider origin without removed provider text", before, after)
	}
	if _, err := f.s.RemoveExternalMetadata(f.ctx, f.a, scope.ItemID, 3); err != domain.ErrConflict {
		t.Fatal("stale removal accepted", err)
	}
	if value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID); err != nil || value.Revision != 4 || removeAuditCount(t, f, scope.ItemID) != 1 {
		t.Fatal("stale removal changed state", err, value.Revision)
	}
}

func TestRemoveExternalMetadataLocksAndTitleFallback(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	removeMediaSource(t, f, item, "Movies/Some.Film.2020.mkv")
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true)); err != nil {
		t.Fatal(err)
	}
	lock := true
	locked, err := f.s.UpdateItemMetadata(f.ctx, f.a, item, 2, []domain.ItemMetadataPatch{{Field: "date", Locked: &lock}})
	if err != nil || removeField(locked, "date").Source != "tmdb" {
		t.Fatal(err)
	}
	result, err := f.s.RemoveExternalMetadata(f.ctx, f.a, item, 3)
	if err != nil || result.Metadata.Revision != 4 || !reflect.DeepEqual(result.Removed, []string{"title", "originalTitle", "overview"}) || !reflect.DeepEqual(result.Skipped, []domain.MetadataFieldSkip{{Field: "date", Reason: "locked"}}) {
		t.Fatal("lock-aware removal differs", err, result)
	}
	title := removeField(result.Metadata, "title")
	if title.Value != "Some.Film.2020" || title.Source != "existing" || title.ProviderOrigin != nil || removeField(result.Metadata, "overview") != nil {
		t.Fatal("title did not fall back to local name", title)
	}
	if !reflect.DeepEqual(removeField(result.Metadata, "date"), removeField(locked, "date")) {
		t.Fatal("locked provider field changed")
	}
	if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, item); err != nil || catalog.Title != "Some.Film.2020" || catalog.Kind != "Movie" {
		t.Fatal("catalog title not restored", err, catalog)
	}
	again, err := f.s.RemoveExternalMetadata(f.ctx, f.a, item, 4)
	if err != nil || again.Metadata.Revision != 5 || len(again.Removed) != 0 || len(again.Skipped) != 1 {
		t.Fatal("repeat removal differs", err, again)
	}
	unlock := false
	if _, err = f.s.UpdateItemMetadata(f.ctx, f.a, item, 5, []domain.ItemMetadataPatch{{Field: "date", Locked: &unlock}}); err != nil {
		t.Fatal(err)
	}
	final, err := f.s.RemoveExternalMetadata(f.ctx, f.a, item, 6)
	if err != nil || !reflect.DeepEqual(final.Removed, []string{"date"}) || len(final.Metadata.Fields) != 1 || removeAuditCount(t, f, item) != 3 {
		t.Fatal("unlocked provider field not removed", err, final)
	}
	var remaining int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM item_metadata_fields WHERE item_id=$1::uuid AND (source='tmdb' OR provider_id IS NOT NULL)`, item).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("provider rows remain", err, remaining)
	}
}

func TestRemoveExternalMetadataTitleWithoutLocalSourceIsKept(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true)); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.RemoveExternalMetadata(f.ctx, f.a, item, 2)
	if err != nil || !reflect.DeepEqual(result.Removed, []string{"originalTitle", "overview", "date"}) || !reflect.DeepEqual(result.Skipped, []domain.MetadataFieldSkip{{Field: "title", Reason: "required"}}) || result.Metadata.Fields[0].Source != "tmdb" {
		t.Fatal("required title handling differs", err, result)
	}
}

func TestRemoveExternalMetadataRollsBackOnFailure(t *testing.T) {
	for _, failure := range []string{"field", "audit"} {
		t.Run(failure, func(t *testing.T) {
			f := newJobFixture(t)
			item := metadataItem(t, f)
			removeMediaSource(t, f, item, "film.mkv")
			if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true)); err != nil {
				t.Fatal(err)
			}
			before, err := f.s.ItemMetadata(f.ctx, f.a, item)
			if err != nil {
				t.Fatal(err)
			}
			ddl := `CREATE FUNCTION reject_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.field='date' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN OLD; END $$; CREATE TRIGGER reject_removal BEFORE DELETE ON item_metadata_fields FOR EACH ROW EXECUTE FUNCTION reject_removal()`
			if failure == "audit" {
				ddl = `CREATE FUNCTION reject_removal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event='item.external_metadata_removed' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_removal BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_removal()`
			}
			if _, err = f.s.Pool.Exec(f.ctx, ddl); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.RemoveExternalMetadata(f.ctx, f.a, item, 2); err == nil {
				t.Fatal("injected failure accepted")
			}
			after, err := f.s.ItemMetadata(f.ctx, f.a, item)
			if err != nil || !reflect.DeepEqual(before, after) || removeAuditCount(t, f, item) != 0 {
				t.Fatal("partial removal survived", err, after)
			}
			if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, item); err != nil || catalog.Title != "Provider title" {
				t.Fatal("catalog title not rolled back", err)
			}
		})
	}
}

func TestRemoveExternalMetadataActualHTTPAndPostgres(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	removeMediaSource(t, f, item, "film.mkv")
	if _, err := f.s.ApplyTMDBMetadata(f.ctx, f.a, item, 1, tmdbMetadataUpdate(12, true)); err != nil {
		t.Fatal(err)
	}
	admin := accountLogin(t, f.ctx, f.s, "job-admin")
	createAccount(t, f.ctx, f.s, f.a, "remove-viewer")
	viewer := accountLogin(t, f.ctx, f.s, "remove-viewer")
	if _, err := f.s.RemoveExternalMetadata(f.ctx, accountActor(viewer), item, 2); err != domain.ErrForbidden {
		t.Fatal("store accepted non-administrator", err)
	}
	cfg, err := config.LoadWith(func(key string) (string, bool) {
		if key == "JELEE_DATABASE_URL" {
			return f.s.Pool.Config().ConnString(), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnableAccounts = true
	cfg.EnableCatalog = true
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.NewLocalMetadata(f.s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, metadata)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "http://localhost"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/items/" + item + "/metadata/external"
	for _, tc := range []struct {
		path, body, token string
		status            int
		code              string
	}{
		{path + "?expectedRevision=2", "", "", 401, "authentication_required"},
		{path + "?expectedRevision=2", "", viewer.Token, 403, "forbidden"},
		{"/api/v1/items/00000000-0000-4000-8000-000000000000/metadata/external?expectedRevision=1", "", admin.Token, 404, "not_found"},
		{path + "?expectedRevision=1", "", admin.Token, 409, "conflict"},
		{path, "", admin.Token, 400, "invalid_request"},
		{path + "?expectedRevision=02", "", admin.Token, 400, "invalid_request"},
		{path + "?expectedRevision=2&expectedRevision=2", "", admin.Token, 400, "invalid_request"},
		{path + "?expectedRevision=2&force=true", "", admin.Token, 400, "invalid_request"},
		{path + "?expectedRevision=2", `{"expectedRevision":2}`, admin.Token, 400, "invalid_request"},
	} {
		w := request(tc.path, tc.body, tc.token)
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if w.Code != tc.status || json.Unmarshal(w.Body.Bytes(), &problem) != nil || problem.Error.Code != tc.code {
			t.Fatal("removal precondition differs", tc.path, tc.status, w.Code, w.Body.String())
		}
	}
	if value, err := f.s.ItemMetadata(f.ctx, f.a, item); err != nil || value.Revision != 2 || removeAuditCount(t, f, item) != 0 {
		t.Fatal("rejected request changed metadata", err, value.Revision)
	}
	w := request(path+"?expectedRevision="+strconv.Itoa(2), "", admin.Token)
	var body struct {
		Data domain.MetadataRemoveResult `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Metadata.Revision != 3 || len(body.Data.Removed) != 4 || body.Data.Metadata.Fields[0].Value != "film" || removeAuditCount(t, f, item) != 1 {
		t.Fatal("HTTP removal differs", w.Code, w.Body.String())
	}
}
