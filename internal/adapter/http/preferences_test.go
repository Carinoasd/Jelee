package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	put := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"compact","layout":null}`, "u"))
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
		// layout is required like every field; null means not customized.
		`{"theme":"dark","density":"compact"}`,
		`{"theme":"dark","density":"compact","layout":{}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[]}}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[{"id":"a","visible":true},{"id":"a","visible":true}],"detail":[]},"presets":[]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[{"id":"a"}],"detail":[]},"presets":[]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[]},"presets":[{"id":"custom-1","name":" padded ","layout":{"home":[],"detail":[]}}]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[]},"presets":[{"id":"mine","name":"x","layout":{"home":[],"detail":[]}}]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[]},"presets":[],"extra":1}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[],"x":[]},"presets":[]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[{"id":"a","visible":null}],"detail":[]},"presets":[]}}`,
		`{"theme":"dark","density":"compact","layout":{"current":{"home":[],"detail":[]},"presets":[` + strings.Repeat(`{"id":"custom-1","name":"x","layout":{"home":[],"detail":[]}},`, 11) + `{"id":"custom-2","name":"y","layout":{"home":[],"detail":[]}}]}}`,
	} {
		assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", body, "u")), 400, "invalid_request")
	}
	assertProblem(t, f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences?x=1", "", "u")), 400, "invalid_request")
	// Too many blocks in one area.
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"compact","layout":{"current":{"home":[`+strings.TrimSuffix(strings.Repeat(`{"id":"a","visible":true},`, 33), ",")+`],"detail":[]},"presets":[]}}`, "u")), 400, "invalid_request")
	assertProblem(t, f.serve(accountRequest(http.MethodGet, "/api/v1/users/me/preferences", "", "")), 401, "authentication_required")
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"light","density":"comfortable","layout":null}`, "")), 401, "authentication_required")
	if calls != before {
		t.Fatal("invalid or unauthenticated preference requests reached storage")
	}

	// A layout with presets is stored and read back as sent.
	layout := `{"current":{"home":[{"id":"latest","visible":true},{"id":"welcome","visible":false}],"detail":[{"id":"files","visible":true}]},` +
		`"presets":[{"id":"custom-1","name":"Metadata review 審查","layout":{"home":[],"detail":[{"id":"nfo","visible":true}]}}]}`
	saved := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"system","density":"comfortable","layout":`+layout+`}`, "u"))
	if saved.Code != 200 || stored.Layout == nil || len(stored.Layout.Presets) != 1 || stored.Layout.Presets[0].Name != "Metadata review 審查" || !stored.Layout.Current.Home[0].Visible || stored.Layout.Current.Home[1].Visible {
		t.Fatalf("layout not stored: %d %s %+v", saved.Code, saved.Body.String(), stored.Layout)
	}
	var echoed struct {
		Data struct {
			Layout json.RawMessage `json:"layout"`
		} `json:"data"`
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &echoed); err != nil || string(echoed.Data.Layout) != layout {
		t.Fatalf("layout echo: %s", saved.Body.String())
	}
}
