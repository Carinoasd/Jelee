package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// at returns ctx with u's principal and a request taken at when: the
// request time restricted time windows are decided at (G48.4).
func (u contentAccessUser) at(ctx context.Context, when time.Time) context.Context {
	p := u.principal
	p.Request = &access.RequestScope{Kind: access.ClientNative, At: when}
	return access.WithPrincipal(ctx, p)
}

// field sets a manual metadata field of the named item.
func (f contentAccessFixture) field(t *testing.T, name, field, value string) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, `WITH s AS (INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2) ON CONFLICT DO NOTHING)
 INSERT INTO item_metadata_fields(item_id,field,value,source,updated_at) VALUES($1::uuid,$2,$3,'manual',now())
 ON CONFLICT(item_id,field) DO UPDATE SET value=EXCLUDED.value`, f.items[name], field, value); err != nil {
		t.Fatal(err)
	}
}

// TestContentAccessKeywordsAndWindowsPostgres extends the G48.10 matrix
// with blocked keywords and restricted time windows (G48.4), read through
// every store surface at injected request times.
func TestContentAccessKeywordsAndWindowsPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	granted := f.all(false)
	everything := f.all(true)
	pgSeries := []string{"series-pg", "season-pg", "episode-pg", "episode-pg-tagged"}
	maSeries := []string{"series-ma", "season-ma", "episode-ma"}
	f.field(t, "movie-g", "originalTitle", "Ｇｈｏｓｔ\u3000Ｓｔｏｒｙ")
	f.field(t, "movie-pg13", "title", "La Nuit NOIRE")
	// Friday 2026-10-09 22:30 in Taipei (UTC+8), and the night after.
	friday := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	saturdayNight := friday.Add(4 * time.Hour)    // Saturday 02:30, still Friday's window
	saturdayEvening := friday.Add(24 * time.Hour) // Saturday 22:30, no window opens on Saturday
	reset := func(t *testing.T) {
		t.Helper()
		for _, u := range []contentAccessUser{f.viewer, f.peer, f.admin} {
			if _, err := f.s.SetContentAccess(f.ctx, f.a, u.principal.UserID, domain.ContentAccess{}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.SetAccessWindows(f.ctx, f.a, u.principal.UserID, []domain.AccessWindow{}); err != nil {
				t.Fatal(err)
			}
		}
		imageRepositoryExec(t, f.jobFixture, `DELETE FROM user_item_access_rules`)
	}
	restrict := func(t *testing.T, u contentAccessUser, c domain.ContentAccess) {
		t.Helper()
		if _, err := f.s.SetContentAccess(f.ctx, f.a, u.principal.UserID, c); err != nil {
			t.Fatal(err)
		}
	}
	windows := func(t *testing.T, u contentAccessUser, w ...domain.AccessWindow) {
		t.Helper()
		if _, err := f.s.SetAccessWindows(f.ctx, f.a, u.principal.UserID, w); err != nil {
			t.Fatal(err)
		}
	}
	night := domain.AccessWindow{Weekdays: []int{5}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei"}
	capped := func(level int) domain.AccessWindow {
		w := night
		w.RatingMax = &level
		return w
	}
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T)
		when   time.Time
		viewer []string
		admin  []string
	}{
		{"keyword_full_width_title", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedKeywords: []string{" ＴＡＧＧＥＤ "}})
		}, friday, without(granted, "movie-tagged", "episode-pg-tagged"), everything},
		{"keyword_ancestor_title_hides_subtree", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedKeywords: []string{"Series-PG"}})
		}, friday, without(granted, pgSeries...), everything},
		{"keyword_original_and_metadata_title", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedKeywords: []string{"ghost story", "noire"}})
		}, friday, without(granted, "movie-g", "movie-pg13"), everything},
		{"keyword_allow_rule_wins", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockedKeywords: []string{"tagged"}})
			if _, err := f.s.SetItemAccessRule(f.ctx, f.a, f.viewer.principal.UserID, f.items["movie-tagged"], domain.ItemAccessAllow); err != nil {
				t.Fatal(err)
			}
		}, friday, without(granted, "episode-pg-tagged"), everything},
		{"window_closed_inside", func(t *testing.T) { windows(t, f.viewer, night) }, friday, nil, everything},
		{"window_crosses_midnight", func(t *testing.T) { windows(t, f.viewer, night) }, saturdayNight, nil, everything},
		{"window_other_day_open", func(t *testing.T) { windows(t, f.viewer, night) }, saturdayEvening, granted, everything},
		{"window_caps_rating", func(t *testing.T) { windows(t, f.viewer, capped(13)) }, friday,
			without(granted, append([]string{"movie-r", "movie-16"}, maSeries...)...), everything},
		{"window_cap_outside_is_off", func(t *testing.T) { windows(t, f.viewer, capped(13)) }, saturdayEvening, granted, everything},
		{"window_lower_of_two_ceilings", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{ParentalRatingMax: ptr(16)})
			windows(t, f.viewer, capped(13))
		}, friday, without(granted, append([]string{"movie-r", "movie-16"}, maSeries...)...), everything},
		{"window_not_lifted_by_allow", func(t *testing.T) {
			windows(t, f.viewer, capped(13))
			if _, err := f.s.SetItemAccessRule(f.ctx, f.a, f.viewer.principal.UserID, f.items["movie-r"], domain.ItemAccessAllow); err != nil {
				t.Fatal(err)
			}
		}, friday, without(granted, append([]string{"movie-r", "movie-16"}, maSeries...)...), everything},
		{"window_unrated_follows_user", func(t *testing.T) {
			restrict(t, f.viewer, domain.ContentAccess{BlockUnrated: ptr(true)})
			windows(t, f.viewer, capped(13))
		}, friday, without(granted, append([]string{"movie-r", "movie-16", "movie-unrated", "movie-unknown-code", "movie-genre"}, maSeries...)...), everything},
		{"admin_window_ignored_by_default", func(t *testing.T) { windows(t, f.admin, night) }, friday, granted, everything},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			tc.setup(t)
			f.observeIn(f.viewer.at(f.ctx, tc.when), t, tc.name+" viewer", f.viewer, tc.viewer)
			f.observeIn(f.peer.at(f.ctx, tc.when), t, tc.name+" peer", f.peer, granted)
			f.observeIn(f.admin.at(f.ctx, tc.when), t, tc.name+" admin", f.admin, tc.admin)
		})
	}
}

// TestAccessWindowSQLMatchesReference sweeps request times across daylight
// saving changes and compares the filter's window SQL with the reference
// AccessWindow.Contains, so the SQL reading of time zones, weekdays and
// windows across midnight is pinned.
func TestAccessWindowSQLMatchesReference(t *testing.T) {
	f := newContentAccessFixture(t)
	user := f.viewer.principal.UserID
	for _, w := range []domain.AccessWindow{
		{Weekdays: []int{5}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei"},
		{Weekdays: []int{1, 2}, Start: "08:30", End: "17:45", TimeZone: "Europe/Berlin"},
		{Weekdays: []int{0}, Start: "23:00", End: "01:00", TimeZone: "America/New_York"},
		{Start: "22:00", End: "06:00", TimeZone: "Australia/Sydney"},
		{Weekdays: []int{6}, Start: "00:00", End: "24:00", TimeZone: "UTC"},
	} {
		if _, err := f.s.SetAccessWindows(f.ctx, f.a, user, []domain.AccessWindow{w}); err != nil {
			t.Fatal(err)
		}
		// 2026-10-24 to 2026-11-03 holds the European and American daylight
		// saving ends; Sydney is in daylight saving time.
		rows, err := f.s.Pool.Query(f.ctx, `SELECT g.t,(SELECT closed FROM (`+activeWindowsSQL(`jsonb_build_object('at',g.t)`)+`) vz_w)
 FROM users u CROSS JOIN generate_series('2026-10-24T00:00:00Z'::timestamptz,'2026-11-03T00:00:00Z','17 minutes') g(t) WHERE u.id=$1::uuid`, user)
		if err != nil {
			t.Fatal(err)
		}
		checked, inside := 0, 0
		for rows.Next() {
			var at time.Time
			var closed bool
			if err = rows.Scan(&at, &closed); err != nil {
				t.Fatal(err)
			}
			if want := w.Contains(at); closed != want {
				t.Errorf("%+v at %s: SQL %t, reference %t", w, at.UTC().Format(time.RFC3339), closed, want)
			}
			checked++
			if closed {
				inside++
			}
		}
		rows.Close()
		if rows.Err() != nil || checked < 800 || inside == 0 || inside == checked {
			t.Fatalf("%+v: %d times, %d inside: %v", w, checked, inside, rows.Err())
		}
	}
}

// TestContentAccessKeywordsAndWindowsAdministrationPostgres covers the
// administration of keywords, windows and the rating code table:
// validation, normalization, audits, no-op writes and the filtered flag.
func TestContentAccessKeywordsAndWindowsAdministrationPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer := f.viewer.principal.UserID
	audits := func(event string) int {
		return syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event=$1`, event)
	}
	view, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: []string{"ＺＯＭＢＩＥ", " zombie", "Gore　"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.BlockedKeywords, []string{"gore", "zombie"}) || audits("user.content_access_changed") != 1 {
		t.Fatalf("keywords %q, audits %d", view.BlockedKeywords, audits("user.content_access_changed"))
	}
	if _, err = f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: []string{"zombie", "GORE"}}); err != nil || audits("user.content_access_changed") != 1 {
		t.Fatalf("unchanged keywords audited: %v", err)
	}
	for _, bad := range [][]string{{" "}, {"　"}, {"a\x01"}, {string(make([]byte, 129))}} {
		if _, err = f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: bad}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("keyword %q: %v", bad, err)
		}
	}
	night := domain.AccessWindow{Weekdays: []int{6, 5}, Start: "21:00", End: "07:00", TimeZone: "Asia/Taipei", RatingMax: ptr(7)}
	if view, err = f.s.SetAccessWindows(f.ctx, f.a, viewer, []domain.AccessWindow{night}); err != nil {
		t.Fatal(err)
	}
	if len(view.Windows) != 1 || !slices.Equal(view.Windows[0].Weekdays, []int{5, 6}) || view.Windows[0].End != "07:00" || *view.Windows[0].RatingMax != 7 || audits("user.access_windows_changed") != 1 {
		t.Fatalf("windows %+v", view.Windows)
	}
	if _, err = f.s.SetAccessWindows(f.ctx, f.a, viewer, []domain.AccessWindow{night}); err != nil || audits("user.access_windows_changed") != 1 {
		t.Fatalf("unchanged windows audited: %v", err)
	}
	for _, bad := range []domain.AccessWindow{
		{Start: "21:00", End: "21:00", TimeZone: "UTC"},
		{Start: "24:00", End: "07:00", TimeZone: "UTC"},
		{Start: "21:00", End: "7:00", TimeZone: "UTC"},
		{Start: "21:00", End: "07:00", TimeZone: "Local"},
		{Start: "21:00", End: "07:00", TimeZone: "Mars/Olympus"},
		{Weekdays: []int{7}, Start: "21:00", End: "07:00", TimeZone: "UTC"},
		{Weekdays: []int{1, 1}, Start: "21:00", End: "07:00", TimeZone: "UTC"},
		{Start: "21:00", End: "07:00", TimeZone: "UTC", RatingMax: ptr(22)},
	} {
		if _, err = f.s.SetAccessWindows(f.ctx, f.a, viewer, []domain.AccessWindow{bad}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("window %+v: %v", bad, err)
		}
	}
	// The flag follows the last restriction.
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM users WHERE id=$1::uuid AND content_filtered`, viewer) != 1 {
		t.Fatal("restrictions without the filtered flag")
	}
	if _, err = f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{}); err != nil {
		t.Fatal(err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM users WHERE id=$1::uuid AND content_filtered`, viewer) != 1 {
		t.Fatal("a window left without the filtered flag")
	}
	if _, err = f.s.SetAccessWindows(f.ctx, f.a, viewer, nil); err != nil {
		t.Fatal(err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM users WHERE id=$1::uuid AND content_filtered`, viewer) != 0 {
		t.Fatal("filtered flag kept without restrictions")
	}
	if _, err = f.s.SetAccessWindows(f.ctx, f.viewer.actor, viewer, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-administrator set windows: %v", err)
	}
}

// TestParentalRatingCodesPostgres replaces the rating code table: a custom
// code takes effect on the next read, codes the filter could never match
// and duplicates are refused, and changes are audited.
func TestParentalRatingCodesPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer := f.viewer.principal.UserID
	f.field(t, "movie-unrated", "mpaa", "KR-15")
	if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{ParentalRatingMax: ptr(16), BlockUnrated: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	granted := f.all(false)
	mature := []string{"movie-r", "series-ma", "season-ma", "episode-ma", "movie-genre"}
	// KR-15 and "Not Rated" are unknown codes: unrated, so blocked.
	f.observe(t, "built-in codes", f.viewer, without(granted, append([]string{"movie-unrated", "movie-unknown-code"}, mature...)...))
	ratings, err := f.s.ListParentalRatings(f.ctx, f.a)
	if err != nil {
		t.Fatal(err)
	}
	// Add the Korean 15 rating and "Not Rated" as all ages; G goes away.
	next := []domain.ParentalRating{{Code: " Not Rated ", Level: 0}, {Code: " pg ", Level: 10}}
	for _, r := range ratings {
		if r.Code != "G" {
			next = append(next, r)
		}
	}
	if _, err = f.s.ReplaceParentalRatings(f.ctx, f.a, next); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a duplicate of a built-in code: %v", err)
	}
	next[1] = domain.ParentalRating{Code: "KR-15", Level: 15}
	for _, bad := range [][]domain.ParentalRating{
		append(slices.Clone(next), domain.ParentalRating{Code: "US:X", Level: 3}),
		append(slices.Clone(next), domain.ParentalRating{Code: "Rated X", Level: 3}),
		append(slices.Clone(next), domain.ParentalRating{Code: "not rated", Level: 1}),
		append(slices.Clone(next), domain.ParentalRating{Code: "X", Level: 22}),
	} {
		if _, err = f.s.ReplaceParentalRatings(f.ctx, f.a, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("rating table %v: %v", bad[len(bad)-1], err)
		}
	}
	got, err := f.s.ReplaceParentalRatings(f.ctx, f.a, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ratings)+1 || syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='access.rating_codes_changed'`) != 1 {
		t.Fatalf("table %d codes", len(got))
	}
	var changes json.RawMessage
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT after_state->'changes' FROM audit_logs WHERE event='access.rating_codes_changed'`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if string(changes) != `[{"code": "G", "after": null, "before": 0}, {"code": "KR-15", "after": 15, "before": null}, {"code": "NOT RATED", "after": 0, "before": null}]` {
		t.Fatalf("audited changes %s", changes)
	}
	// The new codes rate their items on the next read; G items without
	// another rating are unrated now and blocked.
	f.observe(t, "custom codes", f.viewer, without(granted, append([]string{"movie-g", "movie-tagged"}, mature...)...))
	if _, err = f.s.ReplaceParentalRatings(f.ctx, f.a, next); err != nil || syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event='access.rating_codes_changed'`) != 1 {
		t.Fatalf("unchanged table audited: %v", err)
	}
	if _, err = f.s.ReplaceParentalRatings(f.ctx, f.viewer.actor, next); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-administrator replaced codes: %v", err)
	}
}

// TestAccessBulkGrantsAndTemplatesPostgres covers the G48.7 administration:
// the grant matrix, bulk grant previews that write nothing, applied bulk
// changes whose audits carry the preview's counts, templates and their
// application.
func TestAccessBulkGrantsAndTemplatesPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer, peer, admin := f.viewer.principal.UserID, f.peer.principal.UserID, f.admin.principal.UserID
	lib, other := f.registration.Library.ID, f.other.Library.ID
	matrix, err := f.s.GetAccessGrantMatrix(f.ctx, f.a)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]domain.AccessGrantMatrixUser{}
	for _, u := range matrix.Users {
		rows[u.ID] = u
	}
	if len(matrix.Libraries) != 2 || matrix.Truncated || !slices.Equal(rows[viewer].LibraryIDs, []string{lib}) || !rows[admin].Admin || len(rows[admin].LibraryIDs) != 0 {
		t.Fatalf("matrix %+v", matrix)
	}
	acl := func() int { return syncCount(t, f.jobFixture, `SELECT count(*) FROM library_acl`) }
	audits := func(event string) int {
		return syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event=$1`, event)
	}
	grantOther := []domain.AccessGrantOperation{{Action: domain.AccessGrantAdd, UserIDs: []string{viewer, peer}, LibraryIDs: []string{other}},
		{Action: domain.AccessGrantRemove, UserIDs: []string{peer}, LibraryIDs: []string{lib}}}
	before := acl()
	preview, err := f.s.ApplyAccessGrants(f.ctx, f.a, grantOther, true)
	if err != nil {
		t.Fatal(err)
	}
	// movie-other becomes visible to both; the peer loses the whole granted
	// library (17 items).
	granted := len(f.all(false))
	if preview.Applied || preview.Users != 2 || preview.Shown != 2 || preview.Hidden != int64(granted) || preview.Items != int64(granted+1) || len(preview.Changes) != 2 ||
		acl() != before || audits("access.grants_bulk_applied") != 0 || audits("user.library_access_replaced") != 0 {
		t.Fatalf("preview %+v, acl %d->%d", preview, before, acl())
	}
	for _, c := range preview.Changes {
		if c.UserID == peer && (!slices.Equal(c.AddedLibraryIDs, []string{other}) || !slices.Equal(c.RemovedLibraryIDs, []string{lib}) || c.Shown != 1 || c.Hidden != int64(granted)) {
			t.Fatalf("peer change %+v", c)
		}
	}
	applied, err := f.s.ApplyAccessGrants(f.ctx, f.a, grantOther, false)
	if err != nil {
		t.Fatal(err)
	}
	preview.Applied = true
	if applied.Users != preview.Users || applied.Shown != preview.Shown || applied.Hidden != preview.Hidden || !applied.Applied || audits("user.library_access_replaced") != 2 {
		t.Fatalf("applied %+v, preview %+v", applied, preview)
	}
	var summary map[string]any
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT after_state FROM audit_logs WHERE event='access.grants_bulk_applied'`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	if summary["users"] != float64(2) || summary["shown"] != float64(2) || summary["hidden"] != float64(granted) || len(summary["operations"].([]any)) != 2 {
		t.Fatalf("bulk audit %v", summary)
	}
	f.observe(t, "peer after bulk", f.peer, []string{"movie-other"})
	// Applying the same change again changes nobody and still records it.
	if again, err := f.s.ApplyAccessGrants(f.ctx, f.a, grantOther, false); err != nil || again.Users != 0 || len(again.Changes) != 0 || audits("access.grants_bulk_applied") != 2 {
		t.Fatalf("repeat %+v %v", again, err)
	}
	for _, bad := range [][]domain.AccessGrantOperation{
		nil,
		{{Action: "grant", UserIDs: []string{viewer}, LibraryIDs: []string{lib}}},
		{{Action: domain.AccessGrantAdd, UserIDs: []string{viewer, viewer}, LibraryIDs: []string{lib}}},
		{{Action: domain.AccessGrantAdd, UserIDs: []string{}, LibraryIDs: []string{lib}}},
	} {
		if _, err = f.s.ApplyAccessGrants(f.ctx, f.a, bad, true); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("bulk %+v: %v", bad, err)
		}
	}
	if _, err = f.s.ApplyAccessGrants(f.ctx, f.a, []domain.AccessGrantOperation{{Action: domain.AccessGrantAdd, UserIDs: []string{viewer}, LibraryIDs: []string{f.items["movie-g"]}}}, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown library: %v", err)
	}
	if _, err = f.s.ApplyAccessGrants(f.ctx, f.viewer.actor, grantOther, true); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("non-administrator bulk: %v", err)
	}

	// Templates.
	kids := domain.AccessTemplateInput{Name: "Kids", LibraryIDs: []string{lib}, ContentAccess: domain.ContentAccess{ParentalRatingMax: ptr(13), BlockedTags: []string{" Horror "}, BlockedKeywords: []string{"ＧＯＲＥ"}}}
	tmpl, err := f.s.CreateAccessTemplate(f.ctx, f.a, kids)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Name != "Kids" || !slices.Equal(tmpl.BlockedTags, []string{"Horror"}) || *tmpl.ParentalRatingMax != 13 || audits("access.template_created") != 1 {
		t.Fatalf("template %+v", tmpl)
	}
	if _, err = f.s.CreateAccessTemplate(f.ctx, f.a, domain.AccessTemplateInput{Name: "KIDS", LibraryIDs: []string{}, ContentAccess: domain.ContentAccess{BlockedTags: []string{}}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err = f.s.UpdateAccessTemplate(f.ctx, f.a, tmpl.ID, kids); err != nil || audits("access.template_updated") != 0 {
		t.Fatalf("unchanged template audited: %v", err)
	}
	preview, err = f.s.ApplyAccessTemplate(f.ctx, f.a, tmpl.ID, []string{viewer, peer}, true)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Applied || preview.Users != 2 || audits("access.template_applied") != 0 || audits("user.content_access_changed") != 0 {
		t.Fatalf("template preview %+v", preview)
	}
	applied, err = f.s.ApplyAccessTemplate(f.ctx, f.a, tmpl.ID, []string{viewer, peer}, false)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Shown != preview.Shown || applied.Hidden != preview.Hidden || applied.Items != preview.Items || audits("access.template_applied") != 1 || audits("user.content_access_changed") != 2 {
		t.Fatalf("template applied %+v, preview %+v", applied, preview)
	}
	kidsView := without(f.all(false), "movie-r", "movie-16", "series-ma", "season-ma", "episode-ma", "movie-tagged", "episode-pg-tagged")
	f.observe(t, "viewer with template", f.viewer, kidsView)
	f.observe(t, "peer with template", f.peer, kidsView)
	if again, err := f.s.ApplyAccessTemplate(f.ctx, f.a, tmpl.ID, []string{viewer, peer}, false); err != nil || again.Users != 0 || again.Shown != 0 || again.Hidden != 0 {
		t.Fatalf("repeated template %+v %v", again, err)
	}
	if err = f.s.DeleteAccessTemplate(f.ctx, f.a, tmpl.ID); err != nil || audits("access.template_deleted") != 1 {
		t.Fatalf("delete: %v", err)
	}
	f.observe(t, "viewer keeps template settings", f.viewer, kidsView)
	if _, err = f.s.ApplyAccessTemplate(f.ctx, f.a, tmpl.ID, []string{viewer}, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted template: %v", err)
	}
	templates, err := f.s.ListAccessTemplates(f.ctx, f.a)
	if err != nil || len(templates) != 0 {
		t.Fatalf("templates %v %v", templates, err)
	}
}

// TestAccessControlsMigrationRoundTrip refuses the downgrade while
// keywords, windows or templates exist, and goes down and up once they are
// gone.
func TestAccessControlsMigrationRoundTrip(t *testing.T) {
	f := newContentAccessFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	viewer := f.viewer.principal.UserID
	if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: []string{"movie-g"}}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture, "access_controls")
	for _, clear := range []func(){
		func() {
			if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.SetAccessWindows(f.ctx, f.a, viewer, []domain.AccessWindow{{Start: "00:00", End: "24:00", TimeZone: "UTC"}}); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if _, err := f.s.SetAccessWindows(f.ctx, f.a, viewer, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.CreateAccessTemplate(f.ctx, f.a, domain.AccessTemplateInput{Name: "Adults", LibraryIDs: []string{}, ContentAccess: domain.ContentAccess{BlockedTags: []string{}}}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
			t.Fatal("configured access controls downgraded")
		}
		version, dirty, err := Migrate(f.ctx, dsn, "status")
		if err != nil || version >= want || !dirty {
			t.Fatal("refused downgrade state", version, dirty, err)
		}
		if _, err = f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
			t.Fatal(err)
		}
		clear()
	}
	if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
		t.Fatal("a template did not block the downgrade")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM access_templates`); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('user_blocked_keywords','user_access_windows','access_templates','access_template_libraries')`) != 0 {
		t.Fatal("downgrade left the access control tables")
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event IN ('user.content_access_changed','user.access_windows_changed','access.template_created')`) != 5 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if _, err := f.s.SetContentAccess(f.ctx, f.a, viewer, domain.ContentAccess{BlockedKeywords: []string{"movie-g"}}); err != nil {
		t.Fatal(err)
	}
	f.observe(t, "viewer after round trip", f.viewer, without(f.all(false), "movie-g", "movie-genre"))
}
