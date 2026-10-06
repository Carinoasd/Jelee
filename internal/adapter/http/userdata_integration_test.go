package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestUserDataExportGate(t *testing.T) {
	var g userDataExportGate
	if !g.enter("a") || g.enter("a") || !g.enter("b") || g.enter("c") {
		t.Fatal("gate admitted the wrong exports")
	}
	g.leave("a")
	if !g.enter("c") {
		t.Fatal("gate did not free a slot")
	}
}

// TestUserDataRightsPostgres drives the export and the permanent deletion
// (G07.7) through HTTP and the real account tables.
func TestUserDataRightsPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	leakFixture(t, ctx, store)
	for _, name := range []string{"leak-viewer", "leak-admin"} {
		if _, err := store.SetLocalPassword(ctx, name, "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
			t.Fatal(err)
		}
	}
	passwords := &httpAccountPasswords{verify: func(_ context.Context, password, _ string) (bool, error) {
		return password == "correct horse battery", nil
	}}
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 100
	progress, err := app.NewProgress(store, store, app.ProgressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := leakHandlerWithAccounts(t, store, cfg, passwords, progress, leakRenderer{},
		app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute, Box: testWebhookBox(t)})
	serve := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := accountRequest(method, path, body, "")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	login := func(name string) (token, id string) {
		t.Helper()
		w := serve("POST", "/api/v1/auth/login", `{"name":"`+name+`","password":"correct horse battery"}`, "")
		var body struct {
			Data struct {
				Token string `json:"token"`
				User  struct {
					ID string `json:"id"`
				} `json:"user"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Token == "" {
			t.Fatalf("login %s: %d %s", name, w.Code, w.Body.String())
		}
		return body.Data.Token, body.Data.User.ID
	}
	viewer, viewerID := login("leak-viewer")
	admin, adminID := login("leak-admin")

	exportOf := func(id, token string) []domain.UserDataRecord {
		t.Helper()
		w := serve("GET", "/api/v1/users/"+id+"/data-export", "", token)
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("export: %d %v %s", w.Code, w.Header(), w.Body.String())
		}
		if w.Result().Trailer.Get("X-Jelee-Export-Complete") != "true" {
			t.Fatal("export not reported complete")
		}
		var records []domain.UserDataRecord
		lines := bufio.NewScanner(strings.NewReader(w.Body.String()))
		lines.Buffer(nil, 1<<20)
		for lines.Scan() {
			var r domain.UserDataRecord
			if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
				t.Fatalf("line %q: %v", lines.Text(), err)
			}
			records = append(records, r)
		}
		if len(records) < 3 || records[0].Type != "export" || records[1].Type != "account" || records[len(records)-1].Type != "end" {
			t.Fatalf("export shape %v", records)
		}
		var end struct {
			Records int `json:"records"`
		}
		var header domain.UserDataExportHeader
		if json.Unmarshal(records[len(records)-1].Data, &end) != nil || end.Records != len(records)-2 || json.Unmarshal(records[0].Data, &header) != nil || header.UserID != id {
			t.Fatalf("export header or end %s %s", records[0].Data, records[len(records)-1].Data)
		}
		return records
	}
	exportOf(viewerID, viewer)
	exportOf(viewerID, admin)
	assertProblem(t, serve("GET", "/api/v1/users/"+adminID+"/data-export", "", viewer), 403, "forbidden")
	assertProblem(t, serve("GET", "/api/v1/users/"+viewerID+"/data-export?format=csv", "", viewer), 400, "invalid_request")
	assertProblem(t, serve("GET", "/api/v1/users/not-an-id/data-export", "", viewer), 404, "not_found")
	assertProblem(t, serve("GET", "/api/v1/users/"+viewerID+"/data-export", "", ""), 401, "authentication_required")

	// Administrator deletion: never the own account, never by a viewer,
	// another user once.
	other, err := store.Provision(ctx, "data-rights-other", access.ClientNative, false)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := store.Authenticate(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	assertProblem(t, serve("POST", "/api/v1/users/"+adminID+"/purge", `{}`, admin), 403, "forbidden")
	assertProblem(t, serve("POST", "/api/v1/users/"+principal.UserID+"/purge", `{}`, viewer), 403, "forbidden")
	assertProblem(t, serve("POST", "/api/v1/users/"+principal.UserID+"/purge", `{"confirm":true}`, admin), 400, "invalid_request")
	if w := serve("POST", "/api/v1/users/"+principal.UserID+"/purge", `{}`, admin); w.Code != 204 {
		t.Fatalf("administrator purge: %d %s", w.Code, w.Body.String())
	}
	assertProblem(t, serve("POST", "/api/v1/users/"+principal.UserID+"/purge", `{}`, admin), 404, "not_found")
	assertProblem(t, serve("GET", "/api/v1/users/me", "", other), 401, "authentication_required")

	// Self deletion: password required and checked, a native session is
	// refused, the session ends with the account.
	assertProblem(t, serve("POST", "/api/v1/users/me/purge", `{}`, viewer), 400, "invalid_request")
	assertProblem(t, serve("POST", "/api/v1/users/me/purge", `{"password":"wrong"}`, viewer), 400, "invalid_password")
	assertProblem(t, serve("POST", "/api/v1/users/me/purge", `{"password":"correct horse battery","code":"123456","recoveryCode":"x"}`, viewer), 400, "invalid_request")
	if w := serve("PUT", "/api/v1/users/"+viewerID+"/native", `{"allowNative":true}`, admin); w.Code != 200 {
		t.Fatalf("allow native: %d", w.Code)
	}
	nw := serve("POST", "/api/v1/auth/login/native", `{"name":"leak-viewer","password":"correct horse battery","client":"Player","deviceId":"device-9"}`, "")
	var nativeGrant struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if nw.Code != 200 || json.Unmarshal(nw.Body.Bytes(), &nativeGrant) != nil {
		t.Fatalf("native login: %d %s", nw.Code, nw.Body.String())
	}
	assertProblem(t, serve("POST", "/api/v1/users/me/purge", `{"password":"correct horse battery"}`, nativeGrant.Data.Token), 403, "forbidden")
	w := serve("POST", "/api/v1/users/me/purge", `{"password":"correct horse battery"}`, viewer)
	if w.Code != 204 {
		t.Fatalf("self purge: %d %s", w.Code, w.Body.String())
	}
	assertProblem(t, serve("GET", "/api/v1/users/me", "", viewer), 401, "authentication_required")
	var left int
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE id=$1::uuid)+(SELECT count(*) FROM sessions WHERE user_id=$1::uuid)+(SELECT count(*) FROM library_acl WHERE user_id=$1::uuid)`, viewerID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("viewer rows left: %d %v", left, err)
	}
	// The last administrator stays.
	assertProblem(t, serve("POST", "/api/v1/users/me/purge", `{"password":"correct horse battery"}`, admin), 409, "last_admin")
}
