package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// httpVersionRepository answers version and preference calls; err, when set,
// is returned by every decision.
type httpVersionRepository struct {
	err      error
	calls    []string
	source   string
	pref     domain.TrackPreference
	hiddenID string
}

func (r *httpVersionRepository) op(name, item string) (domain.VersionOperation, error) {
	r.calls = append(r.calls, name)
	if item == r.hiddenID {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	return domain.VersionOperation{ID: sourceID, Kind: name, ItemID: item, SourceIDs: []string{}}, r.err
}

func (r *httpVersionRepository) SplitVersion(_ context.Context, _ domain.Actor, item string, in domain.SplitVersionInput) (domain.VersionOperation, error) {
	r.source = in.SourceID
	return r.op("split", item)
}
func (r *httpVersionRepository) MergeItems(_ context.Context, _ domain.Actor, target, source string) (domain.VersionOperation, error) {
	r.source = source
	return r.op("merge", target)
}
func (r *httpVersionRepository) SetPrimaryVersion(_ context.Context, _ domain.Actor, item, source string) (domain.VersionOperation, error) {
	r.source = source
	return r.op("primary", item)
}
func (r *httpVersionRepository) RemoveVersionExclusion(_ context.Context, _ domain.Actor, item, _ string) (domain.VersionOperation, error) {
	return r.op("unexclude", item)
}
func (r *httpVersionRepository) UndoVersionOperation(_ context.Context, _ domain.Actor, id string) (domain.VersionOperation, error) {
	return r.op("undo", id)
}
func (r *httpVersionRepository) VersionOverview(_ context.Context, _ domain.Actor, item string) (domain.VersionOverview, error) {
	if item == r.hiddenID {
		return domain.VersionOverview{}, domain.ErrNotFound
	}
	return domain.VersionOverview{ItemID: item, Exclusions: []domain.VersionExclusion{}, Operations: []domain.VersionOperation{}}, nil
}
func (r *httpVersionRepository) TrackPreferences(_ context.Context, _ domain.Actor, item string) (domain.TrackPreferenceView, error) {
	if item == r.hiddenID {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	return domain.TrackPreferenceView{ItemID: item, Versions: []domain.VersionTrackPreference{}}, nil
}
func (r *httpVersionRepository) SetTrackPreference(_ context.Context, _ domain.Actor, item, source string, p domain.TrackPreference) (domain.TrackPreferenceView, error) {
	r.source, r.pref = source, p
	if item == r.hiddenID {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	return domain.TrackPreferenceView{ItemID: item, Item: &p, Versions: []domain.VersionTrackPreference{}}, nil
}
func (r *httpVersionRepository) UserTrackPreference(context.Context, domain.Actor) (*domain.TrackPreference, error) {
	return nil, nil
}
func (r *httpVersionRepository) SetUserTrackPreference(_ context.Context, _ domain.Actor, p domain.TrackPreference) (*domain.TrackPreference, error) {
	r.pref = p
	return &p, nil
}
func (r *httpVersionRepository) EffectiveTrackPreferences(context.Context, domain.Actor, string) (domain.TrackPreferenceSet, error) {
	return domain.TrackPreferenceSet{}, nil
}

func TestVersionRoutesAuthorizationBodiesAndErrors(t *testing.T) {
	admin, viewer := strings.Repeat("a", 43), strings.Repeat("n", 43)
	backend := &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		switch token {
		case admin:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative, Admin: true}, nil
		case viewer:
			return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientNative}, nil
		}
		return access.Principal{}, domain.ErrUnauthenticated
	}}
	hidden := "77777777-7777-4777-8777-777777777777"
	repo := &httpVersionRepository{hiddenID: hidden}
	catalog, err := app.NewCatalog(&fakeRepository{}).WithVersions(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	handler, err := New(cfg, backend, catalog, &fakeResolver{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	item := "/api/v1/items/" + itemID
	decisions := []struct{ method, path, body string }{
		{"GET", item + "/versions", ""},
		{"POST", item + "/versions/split", `{"sourceId":"` + sourceID + `","exclude":true,"title":"Cut"}`},
		{"POST", item + "/versions/merge", `{"sourceItemId":"` + libraryID + `"}`},
		{"PUT", item + "/versions/primary", `{"sourceId":null}`},
		{"DELETE", item + "/versions/exclusions/" + sourceID, ""},
		{"POST", "/api/v1/version-operations/" + sourceID + "/undo", `{}`},
	}
	for _, d := range decisions {
		if w := playbackHTTPRequest(handler, d.method, d.path, d.body, viewer); w.Code != 403 || !strings.Contains(w.Body.String(), `"forbidden"`) {
			t.Fatalf("%s %s by a viewer: %d %s", d.method, d.path, w.Code, w.Body.String())
		}
	}
	if len(repo.calls) != 0 {
		t.Fatal("viewer reached storage", repo.calls)
	}
	for i, d := range decisions {
		want := 200
		if i == 1 || i == 2 {
			want = 201
		}
		if w := playbackHTTPRequest(handler, d.method, d.path, d.body, admin); w.Code != want || !strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("%s %s: %d %s", d.method, d.path, w.Code, w.Body.String())
		}
	}
	if repo.source != "" {
		t.Fatal("null main version not cleared", repo.source)
	}
	if w := playbackHTTPRequest(handler, "PUT", item+"/versions/primary", `{"sourceId":"`+sourceID+`"}`, admin); w.Code != 200 || repo.source != sourceID {
		t.Fatal("main version", w.Code)
	}
	for name, body := range map[string]string{"empty source": `{"sourceId":""}`, "unknown member": `{"sourceId":null,"x":1}`} {
		if w := playbackHTTPRequest(handler, "PUT", item+"/versions/primary", body, admin); w.Code != 400 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if w := playbackHTTPRequest(handler, "POST", item+"/versions/split", `{"sourceId":"`+sourceID+`","title":"   "}`, admin); w.Code != 400 {
		t.Fatal("blank title", w.Code)
	}
	if w := playbackHTTPRequest(handler, "GET", "/api/v1/items/"+hidden+"/versions", "", admin); w.Code != 404 {
		t.Fatal("hidden overview", w.Code)
	}
	for code, err := range map[string]error{
		"version_identity_conflict":  domain.ErrVersionIdentityConflict,
		"version_merge_incompatible": domain.ErrVersionMergeIncompatible,
		"version_undo_unavailable":   domain.ErrVersionUndoUnavailable,
		"version_item_busy":          domain.ErrVersionItemBusy,
	} {
		repo.err = err
		w := playbackHTTPRequest(handler, "POST", item+"/versions/merge", `{"sourceItemId":"`+libraryID+`"}`, admin)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"`+code+`"`) {
			t.Fatalf("%s: %d %s", code, w.Code, w.Body.String())
		}
	}

	// Track preferences: every user, null members inherit.
	w := playbackHTTPRequest(handler, "PUT", item+"/track-preferences", `{"sourceId":"`+sourceID+`","audioLanguage":"jpn","audioTrack":"e:2","subtitleMode":null}`, viewer)
	var view struct {
		Data domain.TrackPreferenceView `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Data.Item == nil || *view.Data.Item.AudioLanguage != "ja" || repo.source != sourceID || repo.pref.SubtitleMode != nil {
		t.Fatalf("set version preference: %d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{"item track": `{"audioTrack":"e:2"}`, "mode": `{"subtitleMode":"sometimes"}`, "empty source": `{"sourceId":""}`, "unknown": `{"volume":3}`} {
		if w := playbackHTTPRequest(handler, "PUT", item+"/track-preferences", body, viewer); w.Code != 400 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if w := playbackHTTPRequest(handler, "GET", item+"/track-preferences", "", viewer); w.Code != 200 || !strings.Contains(w.Body.String(), `"itemId":"`+itemID+`"`) {
		t.Fatal("read preferences", w.Code)
	}
	for _, method := range []string{"GET", "PUT"} {
		body := ""
		if method == "PUT" {
			body = "{}"
		}
		if w := playbackHTTPRequest(handler, method, "/api/v1/items/"+hidden+"/track-preferences", body, viewer); w.Code != 404 {
			t.Fatalf("hidden %s: %d", method, w.Code)
		}
	}
	if w := playbackHTTPRequest(handler, "PUT", "/api/v1/users/me/track-preferences", `{"subtitleLanguage":"zh-TW","subtitleMode":"always"}`, viewer); w.Code != 200 || *repo.pref.SubtitleLanguage != "zh-TW" {
		t.Fatal("user defaults", w.Code, w.Body.String())
	}
	if w := playbackHTTPRequest(handler, "PUT", "/api/v1/users/me/track-preferences", `{"subtitleTrack":"e:1"}`, viewer); w.Code != 400 {
		t.Fatal("user named track", w.Code)
	}
	if w := playbackHTTPRequest(handler, "GET", "/api/v1/users/me/track-preferences", "", viewer); w.Code != 200 || !strings.Contains(w.Body.String(), `"preference":null`) {
		t.Fatal("user defaults read", w.Code, w.Body.String())
	}
}
