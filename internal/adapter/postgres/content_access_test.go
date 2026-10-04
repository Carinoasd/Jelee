package postgres

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// contentAccessFixture is the G48.10 permission matrix catalog: a granted
// library with rated, unrated, tagged and nested items, and a library the
// viewer is not granted.
type contentAccessFixture struct {
	jobFixture
	other   domain.LibraryRegistration
	items   map[string]string // name -> item ID
	names   map[string]string // item ID -> name
	sources map[string]string // name -> media source ID (movies only)
	viewer  contentAccessUser
	peer    contentAccessUser
	admin   contentAccessUser
}

type contentAccessUser struct {
	principal access.Principal
	actor     domain.Actor
}

func contentAccessPrincipal(t *testing.T, f jobFixture, name string, admin bool) contentAccessUser {
	t.Helper()
	token, err := f.s.Provision(f.ctx, name, access.ClientNative, admin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return contentAccessUser{principal: p, actor: domain.Actor{UserID: p.UserID, SessionID: p.SessionID, IP: "127.0.0.1"}}
}

func newContentAccessFixture(t *testing.T) contentAccessFixture {
	t.Helper()
	f := contentAccessFixture{jobFixture: newJobFixture(t), items: map[string]string{}, names: map[string]string{}, sources: map[string]string{}}
	var err error
	if f.other, err = f.s.RegisterLibrary(f.ctx, "content-other", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	lib := f.registration.Library.ID
	add := func(name, kind, library string, fields map[string]string, facts map[string]string) string {
		id := detailsItem(t, f.jobFixture, library, name, kind)
		f.items[name], f.names[id] = id, name
		if len(fields) > 0 || len(facts) > 0 {
			detailsMetadata(t, f.jobFixture, id, fields, facts)
		}
		if kind == "Movie" {
			root := f.registration.RootID
			if library != lib {
				root = f.other.RootID
			}
			var source string
			if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`,
				id, library, root, name+".mkv").Scan(&source); err != nil {
				t.Fatal(err)
			}
			f.sources[name] = source
		}
		return id
	}
	link := func(child, childKind, parent, parentKind string) {
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`,
			f.items[child], lib, childKind, f.items[parent], parentKind)
	}
	add("movie-g", "Movie", lib, map[string]string{"mpaa": "G"}, nil)
	add("movie-pg13", "Movie", lib, map[string]string{"mpaa": "US:PG-13"}, nil)
	add("movie-r", "Movie", lib, map[string]string{"mpaa": "Rated R"}, nil)
	add("movie-16", "Movie", lib, map[string]string{"certification": "DE: 16+"}, nil)
	add("movie-unrated", "Movie", lib, nil, nil)
	add("movie-unknown-code", "Movie", lib, map[string]string{"mpaa": "Not Rated"}, nil)
	add("movie-tagged", "Movie", lib, map[string]string{"mpaa": "G"}, map[string]string{"tags": `["Horror","Classic"]`})
	add("movie-genre", "Movie", lib, nil, map[string]string{"genres": `["GORE "]`})
	// A mature series whose episode carries a milder own rating: the
	// highest rating of the chain decides.
	add("series-ma", "Series", lib, map[string]string{"mpaa": "TV-MA"}, nil)
	add("season-ma", "Season", lib, nil, nil)
	add("episode-ma", "Episode", lib, map[string]string{"mpaa": "TV-PG"}, nil)
	link("season-ma", "Season", "series-ma", "Series")
	link("episode-ma", "Episode", "season-ma", "Season")
	// A family series: the unrated episode inherits the series rating, the
	// tagged one is blocked by its own tag.
	add("series-pg", "Series", lib, map[string]string{"mpaa": "TV-PG"}, nil)
	add("season-pg", "Season", lib, nil, nil)
	add("episode-pg", "Episode", lib, nil, nil)
	add("episode-pg-tagged", "Episode", lib, nil, map[string]string{"tags": `["horror"]`})
	link("season-pg", "Season", "series-pg", "Series")
	link("episode-pg", "Episode", "season-pg", "Season")
	link("episode-pg-tagged", "Episode", "season-pg", "Season")
	add("movie-other", "Movie", f.other.Library.ID, map[string]string{"mpaa": "G"}, nil)

	f.viewer = contentAccessPrincipal(t, f.jobFixture, "content-viewer", false)
	f.peer = contentAccessPrincipal(t, f.jobFixture, "content-peer", false)
	f.admin = contentAccessPrincipal(t, f.jobFixture, "content-admin", true)
	for _, u := range []contentAccessUser{f.viewer, f.peer} {
		imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, u.principal.UserID, lib)
	}
	// Every user has a resume point, a poster and statistics on every item,
	// so the continue watching list, images and statistics must filter.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at)
 SELECT u.id,i.id,600000000,false,0,now(),now() FROM users u CROSS JOIN items i`)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays)
 SELECT u.id,(now() AT TIME ZONE 'UTC')::date,i.id,i.library_id,60000,1,1,1 FROM users u CROSS JOIN items i`)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,remote_url) SELECT id,library_id,'Primary',0,'remote','https://images.example/'||id FROM items`)
	return f
}

func (f contentAccessFixture) all(library bool) []string {
	var names []string
	for name := range f.items {
		if library || name != "movie-other" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func without(names []string, hidden ...string) []string {
	var out []string
	for _, name := range names {
		if !slices.Contains(hidden, name) {
			out = append(out, name)
		}
	}
	return out
}

func (f contentAccessFixture) toNames(t *testing.T, ids []string) []string {
	t.Helper()
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		name, ok := f.names[id]
		if !ok {
			t.Fatalf("unknown item %s", id)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// observe reads every user-facing surface of the store as u and fails
// unless each one shows exactly want.
func (f contentAccessFixture) observe(t *testing.T, label string, u contentAccessUser, want []string) {
	t.Helper()
	ctx, s, userID := f.ctx, f.s, u.principal.UserID
	// SetPlayed below clears resume points; every observation starts from
	// the same ones.
	imageRepositoryExec(t, f.jobFixture, `UPDATE user_item_data SET resume_ticks=600000000,played=false,play_count=0`)
	check := func(surface string, got []string) {
		t.Helper()
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %s shows %v, want %v", label, surface, got, want)
		}
	}
	var listed []string
	for cursor := ""; ; {
		page, err := s.ListItems(ctx, userID, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page {
			listed = append(listed, item.ID)
		}
		if len(page) < 7 {
			break
		}
		cursor = page[len(page)-1].ID
	}
	check("ListItems", f.toNames(t, listed))
	browse, err := s.BrowseItems(ctx, userID, domain.BrowseQuery{Scope: domain.BrowseAll, Limit: domain.BrowseLimitMax})
	if err != nil {
		t.Fatal(err)
	}
	if browse.Total != len(want) {
		t.Errorf("%s: browse total %d, want %d", label, browse.Total, len(want))
	}
	check("BrowseItems", f.toNames(t, detailsIDs(browse)))
	search, err := s.BrowseItems(ctx, userID, domain.BrowseQuery{Scope: domain.BrowseAll, SearchTerm: "-", Limit: domain.BrowseLimitMax})
	if err != nil {
		t.Fatal(err)
	}
	check("search", f.toNames(t, detailsIDs(search)))
	resume, err := s.ListResume(ctx, userID, domain.ResumeQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var resumed []string
	for _, e := range resume.Items {
		resumed = append(resumed, e.Item.ID)
	}
	var playable []string
	for _, name := range want {
		if kind := strings.SplitN(name, "-", 2)[0]; kind == "movie" || kind == "episode" {
			playable = append(playable, name)
		}
	}
	if got := f.toNames(t, resumed); !slices.Equal(got, playable) {
		t.Errorf("%s: resume shows %v, want playable %v", label, got, playable)
	}
	ids := make([]string, 0, len(f.items))
	for _, id := range f.items {
		ids = append(ids, id)
	}
	data, err := s.UserItemData(ctx, userID, ids)
	if err != nil {
		t.Fatal(err)
	}
	var withData []string
	for id := range data {
		withData = append(withData, id)
	}
	check("UserItemData", f.toNames(t, withData))
	today := time.Now().UTC().Truncate(24 * time.Hour)
	report, err := s.WatchStatsReport(ctx, u.actor, domain.WatchStatsQuery{SubjectID: userID, From: today, To: today, Period: domain.WatchPeriodDay, Top: domain.WatchStatsTopMax})
	if err != nil {
		t.Fatal(err)
	}
	var watched []string
	for _, r := range report.TopItems {
		watched = append(watched, r.ItemID)
	}
	check("WatchStatsReport", f.toNames(t, watched))
	if report.Totals.Sessions != int64(len(want)) {
		t.Errorf("%s: statistics count %d sessions, want %d", label, report.Totals.Sessions, len(want))
	}
	// By ID: every surface answers a hidden item exactly like a missing one.
	byID := map[string]func(name, id string) error{
		"GetItem":        func(_, id string) error { _, err := s.GetItem(ctx, userID, id); return err },
		"GetBrowseItem":  func(_, id string) error { _, err := s.GetBrowseItem(ctx, userID, id); return err },
		"GetItemDetails": func(_, id string) error { _, err := s.GetItemDetails(ctx, userID, id); return err },
		"ListItemImages": func(_, id string) error { _, err := s.ListItemImages(ctx, u.actor, id); return err },
		"ListItemSources": func(_, id string) error {
			_, err := s.ListItemSources(ctx, u.actor, id)
			return err
		},
		"SetPlayed": func(_, id string) error {
			_, err := s.SetPlayed(ctx, userID, id, false, time.Now())
			return err
		},
		"Resolve": func(name, _ string) error {
			source, ok := f.sources[name]
			if !ok {
				return errSkip
			}
			_, err := s.Resolve(ctx, u.principal, source)
			return err
		},
		"ResolveImageSource": func(name, id string) error {
			if _, ok := f.sources[name]; !ok {
				return errSkip
			}
			_, err := s.ResolveImageSource(ctx, u.actor, id)
			return err
		},
	}
	surfaces := make([]string, 0, len(byID))
	for surface := range byID {
		surfaces = append(surfaces, surface)
	}
	sort.Strings(surfaces)
	for _, surface := range surfaces {
		var visible []string
		for name, id := range f.items {
			err := byID[surface](name, id)
			switch {
			case errors.Is(err, errSkip):
				continue
			case err == nil:
				visible = append(visible, name)
			case errors.Is(err, domain.ErrNotFound) || errors.Is(err, media.ErrNotFound):
			default:
				// ResolveImageSource reports a missing file as not found; any
				// other error is a failure.
				t.Fatalf("%s: %s(%s): %v", label, surface, name, err)
			}
		}
		sort.Strings(visible)
		var expected []string
		for _, name := range want {
			if surface == "Resolve" || surface == "ResolveImageSource" {
				if _, ok := f.sources[name]; !ok {
					continue
				}
			}
			expected = append(expected, name)
		}
		if !slices.Equal(visible, expected) {
			t.Errorf("%s: %s reaches %v, want %v", label, surface, visible, expected)
		}
	}
}

var errSkip = errors.New("not applicable")

func ptr[T any](v T) *T { return &v }

// TestContentAccessMatrixPostgres is the G48.10 permission matrix: users ×
// library grants × item rules × ratings × tags, read through every store
// surface (lists, search, resume, user data, statistics, details, images,
// sources and direct delivery).
func TestContentAccessMatrixPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	granted := f.all(false)
	everything := f.all(true)
	maSeries := []string{"series-ma", "season-ma", "episode-ma"}
	pgSeries := []string{"series-pg", "season-pg", "episode-pg", "episode-pg-tagged"}
	reset := func(t *testing.T) {
		t.Helper()
		for _, u := range []contentAccessUser{f.viewer, f.peer, f.admin} {
			if _, err := f.s.SetContentAccess(f.ctx, f.a, u.principal.UserID, domain.ContentAccess{}); err != nil {
				t.Fatal(err)
			}
		}
		imageRepositoryExec(t, f.jobFixture, `DELETE FROM user_item_access_rules`)
		if _, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{}); err != nil {
			t.Fatal(err)
		}
	}
	rule := func(t *testing.T, u contentAccessUser, name string, effect domain.ItemAccessEffect) {
		t.Helper()
		if _, err := f.s.SetItemAccessRule(f.ctx, f.a, u.principal.UserID, f.items[name], effect); err != nil {
			t.Fatal(err)
		}
	}
	restrict := func(t *testing.T, u contentAccessUser, c domain.ContentAccess) {
		t.Helper()
		if _, err := f.s.SetContentAccess(f.ctx, f.a, u.principal.UserID, c); err != nil {
			t.Fatal(err)
		}
	}
	unratedUnder13 := []string{"movie-unrated", "movie-unknown-code", "movie-genre"}
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T)
		viewer []string
		admin  []string
	}{
		{"no_rules", func(*testing.T) {}, granted, everything},
		{"ceiling_13_unrated_allowed", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(13)})
		}, without(granted, append([]string{"movie-r", "movie-16"}, maSeries...)...), everything},
		{"ceiling_16_numeric_code", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(16)})
		}, without(granted, append([]string{"movie-r"}, maSeries...)...), everything},
		{"ceiling_13_user_blocks_unrated", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(13), BlockUnrated: ptr(true)})
		}, without(granted, append(append([]string{"movie-r", "movie-16"}, maSeries...), unratedUnder13...)...), everything},
		{"ceiling_13_policy_blocks_unrated", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(13)})
			if _, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{BlockUnrated: true}); err != nil {
				t.Fatal(err)
			}
		}, without(granted, append(append([]string{"movie-r", "movie-16"}, maSeries...), unratedUnder13...)...), everything},
		{"ceiling_13_user_overrides_policy", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(13), BlockUnrated: ptr(false)})
			if _, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{BlockUnrated: true}); err != nil {
				t.Fatal(err)
			}
		}, without(granted, append([]string{"movie-r", "movie-16"}, maSeries...)...), everything},
		{"blocked_tags", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedTags: []string{" HORROR", "Gore"}})
		}, without(granted, "movie-tagged", "movie-genre", "episode-pg-tagged"), everything},
		{"hide_subtree", func(t *testing.T) {
			rule(t, f.viewer, "series-pg", domain.ItemAccessHide)
		}, without(granted, pgSeries...), everything},
		{"hide_subtree_allow_nearer", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedTags: []string{"horror"}})
			rule(t, f.viewer, "series-pg", domain.ItemAccessHide)
			rule(t, f.viewer, "season-pg", domain.ItemAccessAllow)
		}, without(granted, "series-pg", "movie-tagged"), everything},
		{"hide_nearer_than_allow", func(t *testing.T) {
			rule(t, f.viewer, "series-pg", domain.ItemAccessAllow)
			rule(t, f.viewer, "episode-pg", domain.ItemAccessHide)
		}, without(granted, "episode-pg"), everything},
		{"allow_beats_ceiling", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(0)})
			rule(t, f.viewer, "series-ma", domain.ItemAccessAllow)
			rule(t, f.viewer, "movie-r", domain.ItemAccessAllow)
		}, without(granted, "movie-pg13", "movie-16", "series-pg", "season-pg", "episode-pg", "episode-pg-tagged"), everything},
		{"allow_never_widens_library_grant", func(t *testing.T) {
			rule(t, f.viewer, "movie-other", domain.ItemAccessAllow)
		}, granted, everything},
		{"admin_rules_ignored_by_default", func(t *testing.T) {
			restrict(t, f.admin, domain.ContentAccess{ParentalRatingMax: ptr(0), BlockedTags: []string{"horror"}})
			rule(t, f.admin, "movie-g", domain.ItemAccessHide)
		}, granted, everything},
		{"admin_rules_with_restrict_admins", func(t *testing.T) {
			restrict(t, f.admin, domain.ContentAccess{BlockedTags: []string{"horror"}})
			rule(t, f.admin, "movie-g", domain.ItemAccessHide)
			rule(t, f.admin, "movie-other", domain.ItemAccessHide)
			if _, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{RestrictAdmins: true}); err != nil {
				t.Fatal(err)
			}
		}, granted, without(everything, "movie-g", "movie-other", "movie-tagged", "episode-pg-tagged")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			tc.setup(t)
			f.observe(t, "viewer", f.viewer, tc.viewer)
			// Another user's restrictions never leak across users.
			f.observe(t, "peer", f.peer, granted)
			f.observe(t, "admin", f.admin, tc.admin)
		})
	}
}

// TestContentAccessAdministrationPostgres covers the administrator API of
// the store: authorization, validation, audit rows, idempotence and the
// filtered flag.
func TestContentAccessAdministrationPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer := f.viewer.principal.UserID
	audit := func(event string) int {
		return syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event=$1`, event)
	}
	filtered := func() bool {
		var v bool
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT content_filtered FROM users WHERE id=$1::uuid`, viewer).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Viewers are refused before any lookup.
	if _, err := f.s.GetContentAccess(f.ctx, f.viewer.actor, viewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := f.s.SetItemAccessRule(f.ctx, f.viewer.actor, viewer, f.items["movie-g"], domain.ItemAccessAllow); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer rule: %v", err)
	}
	if _, err := f.s.SetAccessPolicy(f.ctx, f.viewer.actor, domain.AccessPolicy{RestrictAdmins: true}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer policy: %v", err)
	}
	// Validation.
	for _, bad := range []domain.ContentAccess{{ParentalRatingMax: ptr(22)}, {ParentalRatingMax: ptr(-1)}, {BlockedTags: []string{" "}}, {BlockedTags: []string{"a\x00b"}}, {BlockedTags: []string{strings.Repeat("x", 129)}}} {
		if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid %+v: %v", bad, err)
		}
	}
	if _, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["movie-g"], "deny"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid effect: %v", err)
	}
	if _, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer, "00000000-0000-4000-8000-000000000001", domain.ItemAccessHide); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	if _, err := f.s.SetContentAccess(f.ctx, f.a, "00000000-0000-4000-8000-000000000001", domain.ContentAccess{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	if filtered() {
		t.Fatal("unrestricted user starts filtered")
	}
	// Replace, normalize and audit once.
	set := domain.ContentAccess{ParentalRatingMax: ptr(13), BlockUnrated: ptr(true), BlockedTags: []string{"Horror", " horror ", "GORE"}}
	v, err := f.s.SetContentAccess(f.ctx, f.a, viewer, set)
	if err != nil || *v.ParentalRatingMax != 13 || !*v.BlockUnrated || !slices.Equal(v.BlockedTags, []string{"gore", "horror"}) {
		t.Fatalf("set content access: %+v %v", v, err)
	}
	if _, err = f.s.SetContentAccess(f.ctx, f.a, viewer, set); err != nil || audit("user.content_access_changed") != 1 || !filtered() {
		t.Fatalf("unchanged replacement audited or unfiltered: %v", err)
	}
	r, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"], domain.ItemAccessHide)
	if err != nil || r.Effect != domain.ItemAccessHide || r.Title != "series-pg" || r.Kind != "Series" {
		t.Fatalf("set rule: %+v %v", r, err)
	}
	if _, err = f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"], domain.ItemAccessHide); err != nil || audit("user.item_access_rule_set") != 1 {
		t.Fatalf("unchanged rule audited: %v", err)
	}
	if _, err = f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"], domain.ItemAccessAllow); err != nil || audit("user.item_access_rule_set") != 2 {
		t.Fatalf("changed rule not audited: %v", err)
	}
	view, err := f.s.GetContentAccess(f.ctx, f.a, viewer)
	if err != nil || len(view.Rules) != 1 || view.Rules[0].Effect != domain.ItemAccessAllow || view.Rules[0].ItemID != f.items["series-pg"] {
		t.Fatalf("read content access: %+v %v", view, err)
	}
	// Clearing everything returns the user to the unfiltered fast path.
	if err = f.s.DeleteItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"]); err != nil || audit("user.item_access_rule_removed") != 1 {
		t.Fatalf("delete rule: %v", err)
	}
	if err = f.s.DeleteItemAccessRule(f.ctx, f.a, viewer, f.items["series-pg"]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete missing rule: %v", err)
	}
	if !filtered() {
		t.Fatal("ceiling and tags no longer mark the user")
	}
	if _, err = f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{}); err != nil || filtered() || audit("user.content_access_changed") != 2 {
		t.Fatalf("clear content access: filtered=%t %v", filtered(), err)
	}
	// A rule written by any path marks its user, and the database refuses a
	// ceiling without the flag.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_blocked_tags(user_id,tag) VALUES($1::uuid,'x')`, viewer)
	if !filtered() {
		t.Fatal("direct tag insert did not mark the user")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET parental_rating_max=10,content_filtered=false WHERE id=$1::uuid`, viewer); err == nil {
		t.Fatal("ceiling accepted without the filtered flag")
	}
	// Policy.
	if p, err := f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{RestrictAdmins: true}); err != nil || !p.RestrictAdmins || audit("access.policy_changed") != 1 {
		t.Fatalf("set policy: %v", err)
	}
	if p, err := f.s.GetAccessPolicy(f.ctx, f.a); err != nil || !p.RestrictAdmins || p.BlockUnrated {
		t.Fatalf("read policy: %+v %v", p, err)
	}
	if _, err = f.s.SetAccessPolicy(f.ctx, f.a, domain.AccessPolicy{RestrictAdmins: true}); err != nil || audit("access.policy_changed") != 1 {
		t.Fatalf("unchanged policy audited: %v", err)
	}
	ratings, err := f.s.ListParentalRatings(f.ctx, f.a)
	if err != nil || len(ratings) < 20 || !slices.ContainsFunc(ratings, func(r domain.ParentalRating) bool { return r.Code == "PG-13" && r.Level == 13 }) {
		t.Fatalf("parental ratings: %v %v", ratings, err)
	}
	// The rule limit is enforced per user.
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'bulk '||n,'Movie' FROM generate_series(1,$2::int) n`, f.registration.Library.ID, domain.ItemAccessRulesMax)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_item_access_rules(user_id,item_id,effect) SELECT $1::uuid,id,'hide' FROM items WHERE title LIKE 'bulk %'`, viewer)
	if _, err = f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["movie-g"], domain.ItemAccessHide); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("rule limit: %v", err)
	}
	// Item deletion takes its rules along.
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM items WHERE title LIKE 'bulk %'`)
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM user_item_access_rules`); n != 0 {
		t.Fatalf("rules survived their items: %d", n)
	}
}

func TestContentAccessMigrationRoundTrip(t *testing.T) {
	f := newContentAccessFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	viewer := f.viewer.principal.UserID
	if _, err := f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["movie-g"], domain.ItemAccessHide); err != nil {
		t.Fatal(err)
	}
	want := migrationVersion(t, "content_access")
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("configured content rules downgraded")
	}
	version, dirty, err := Migrate(f.ctx, dsn, "status")
	if err != nil || version >= want || !dirty {
		t.Fatal("refused downgrade state", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM user_item_access_rules`) != 1 {
		t.Fatal("refused downgrade removed rules")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteItemAccessRule(f.ctx, f.a, viewer, f.items["movie-g"]); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='users' AND column_name IN ('parental_rating_max','block_unrated','content_filtered')`) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('access_policy','parental_ratings','user_item_access_rules','user_blocked_tags')`) != 0 {
		t.Fatal("downgrade left content access schema")
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event LIKE 'user.item_access_rule_%'`) != 2 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err = Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err = f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetItemAccessRule(f.ctx, f.a, viewer, f.items["movie-g"], domain.ItemAccessHide); err != nil {
		t.Fatal(err)
	}
	f.observe(t, "viewer after round trip", f.viewer, without(f.all(false), "movie-g"))
}
