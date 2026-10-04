package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestUserPreferencesHTTPReadAndReplaceOwnPreferences(t *testing.T) {
	stored := domain.DefaultUserPreferences()
	calls := 0
	var actors []domain.Actor
	repo := httpAccountRepository{preferences: func(_ context.Context, a domain.Actor, p *domain.UserPreferences) (domain.UserPreferences, error) {
		calls++
		actors = append(actors, a)
		if p != nil {
			stored = *p
		}
		return stored, nil
	}}
	f := newAccountHTTPFixture(t, repo, nil)
	decode := func(w *httptest.ResponseRecorder) domain.UserPreferences {
		t.Helper()
		var body struct {
			Data domain.UserPreferences `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	read := f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences", "", "u"))
	if read.Code != 200 || read.Header().Get("Cache-Control") != "no-store" || decode(read) != domain.DefaultUserPreferences() {
		t.Fatalf("defaults not served: %d %s", read.Code, read.Body.String())
	}
	put := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"compact"}`, "u"))
	want := domain.UserPreferences{Theme: domain.ThemeDark, Density: domain.DensityCompact}
	if put.Code != 200 || decode(put) != want || stored != want {
		t.Fatalf("replacement not stored: %d %s", put.Code, put.Body.String())
	}
	for _, a := range actors {
		if a.UserID != userID || a.SessionID != sessionID {
			t.Fatalf("preferences used another identity: %+v", a)
		}
	}

	before := calls
	for _, body := range []string{
		`{"theme":"dark"}`, `{"density":"compact"}`, `{}`, `{"theme":"auto","density":"compact"}`,
		`{"theme":"dark","density":"tiny"}`, `{"theme":"dark","density":"compact","accent":"red"}`,
		`{"theme":null,"density":"compact"}`, `[]`, ``,
	} {
		assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", body, "u")), 400, "invalid_request")
	}
	assertProblem(t, f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences?x=1", "", "u")), 400, "invalid_request")
	assertProblem(t, f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences", "", "")), 401, "authentication_required")
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"light","density":"comfortable"}`, "")), 401, "authentication_required")
	if calls != before {
		t.Fatal("invalid or unauthenticated preference requests reached storage")
	}
}
