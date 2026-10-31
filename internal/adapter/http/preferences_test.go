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
	put := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"compact","layout":null,"display":null}`, "u"))
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
		// display is required too; each member it names is null or from its allow-list.
		`{"theme":"dark","density":"compact","layout":null}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":"neon","posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":"light","posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"posterRatio":"wide","posterSize":"medium","listDensity":"comfortable","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"posterRatio":"portrait","posterSize":"huge","listDensity":"comfortable","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"posterRatio":"portrait","posterSize":"medium","listDensity":"tiny","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"grid"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"view":"Poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"posterSize":""}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"poster","extra":1}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":{"palette":null,"palette":"sepia","posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"poster"}}`,
		`{"theme":"dark","density":"compact","layout":null,"display":[]}`,
		`{"theme":"dark","density":"compact","layout":null,"display":"sepia"}`,
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
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"light","density":"comfortable","layout":null,"display":null}`, "")), 401, "authentication_required")
	if calls != before {
		t.Fatal("invalid or unauthenticated preference requests reached storage")
	}

	// Display choices are stored and read back as sent; a preset travels with
	// its color scheme in theme, and null clears them.
	const displayJSON = `{"palette":"sepia","posterRatio":"square","posterSize":"large","listDensity":"compact","view":"list"}`
	withDisplay := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"light","density":"comfortable","layout":null,"display":`+displayJSON+`}`, "u"))
	sepia := domain.PaletteSepia
	if withDisplay.Code != 200 || stored.Display == nil || !sameDisplay(*stored.Display, domain.UserDisplay{Palette: &sepia, PosterRatio: ptr("square"), PosterSize: ptr("large"), ListDensity: ptr("compact"), View: ptr("list")}) {
		t.Fatalf("display not stored: %d %s", withDisplay.Code, withDisplay.Body.String())
	}
	var shown struct {
		Data struct {
			Display json.RawMessage `json:"display"`
		} `json:"data"`
	}
	if err := json.Unmarshal(withDisplay.Body.Bytes(), &shown); err != nil || string(shown.Data.Display) != displayJSON {
		t.Fatalf("display echo: %s", withDisplay.Body.String())
	}
	plain := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"comfortable","layout":null,"display":{"palette":null,"posterRatio":"portrait","posterSize":"medium","listDensity":"comfortable","view":"poster"}}`, "u"))
	if plain.Code != 200 || stored.Display == nil || stored.Display.Palette != nil || !strings.Contains(plain.Body.String(), `"palette":null`) {
		t.Fatalf("null palette: %d %s", plain.Code, plain.Body.String())
	}
	// Members may be left out or null: they follow the site default one by
	// one. Nothing set at all is the same as no display choices.
	partial := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"comfortable","layout":null,"display":{"posterSize":"large","view":null}}`, "u"))
	if partial.Code != 200 || stored.Display == nil || !sameDisplay(*stored.Display, domain.UserDisplay{PosterSize: ptr("large")}) ||
		!strings.Contains(partial.Body.String(), `"display":{"palette":null,"posterRatio":null,"posterSize":"large","listDensity":null,"view":null}`) {
		t.Fatalf("partial display: %d %s", partial.Code, partial.Body.String())
	}
	for _, empty := range []string{`{}`, `{"palette":null,"posterRatio":null,"posterSize":null,"listDensity":null,"view":null}`} {
		got := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"comfortable","layout":null,"display":`+empty+`}`, "u"))
		if got.Code != 200 || stored.Display != nil || !strings.Contains(got.Body.String(), `"display":null`) {
			t.Fatalf("empty display %s: %d %s", empty, got.Code, got.Body.String())
		}
	}
	cleared := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"comfortable","layout":null,"display":null}`, "u"))
	if cleared.Code != 200 || stored.Display != nil || !strings.Contains(cleared.Body.String(), `"display":null`) {
		t.Fatalf("display not cleared: %d %s", cleared.Code, cleared.Body.String())
	}
	// The account body limit (64 KiB) still applies.
	assertProblem(t, f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"dark","density":"comfortable","layout":null,"display":null}`+strings.Repeat(" ", 70*1024), "u")), 413, "body_too_large")

	// A layout with presets is stored and read back as sent.
	layout := `{"current":{"home":[{"id":"latest","visible":true},{"id":"welcome","visible":false}],"detail":[{"id":"files","visible":true}]},` +
		`"presets":[{"id":"custom-1","name":"Metadata review 審查","layout":{"home":[],"detail":[{"id":"nfo","visible":true}]}}]}`
	saved := f.serve(accountRequest(http.MethodPut, "/api/v1/users/me/preferences", `{"theme":"system","density":"comfortable","display":null,"layout":`+layout+`}`, "u"))
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

func ptr(value string) *string { return &value }

// sameDisplay compares display members by value.
func sameDisplay(a, b domain.UserDisplay) bool {
	same := func(x, y *string) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	return same(a.Palette, b.Palette) && same(a.PosterRatio, b.PosterRatio) && same(a.PosterSize, b.PosterSize) && same(a.ListDensity, b.ListDensity) && same(a.View, b.View)
}
