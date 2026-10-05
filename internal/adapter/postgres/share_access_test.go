package postgres

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G48.5 / G48.6 / G48.10: the permission matrix extended by the request
// scope (client address, LAN, session kind, client control library set)
// and by share guests, read through every store surface.

// scoped returns ctx carrying u's principal with the given request scope,
// as the HTTP layer attaches it.
func scoped(ctx context.Context, u contentAccessUser, address string, kind access.ClientKind, libraries []string) context.Context {
	p := u.principal
	scope := &access.RequestScope{Kind: kind, Libraries: libraries}
	if address != "" {
		scope.IP = netip.MustParseAddr(address)
	}
	p.Request = scope
	return access.WithPrincipal(ctx, p)
}

func (f contentAccessFixture) networkRule(t *testing.T, in domain.NetworkRuleInput) domain.NetworkRule {
	t.Helper()
	if in.CIDRs == nil {
		in.CIDRs = []string{}
	}
	if in.ClientKinds == nil {
		in.ClientKinds = []string{}
	}
	r, err := f.s.CreateNetworkRule(f.ctx, f.a, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	lanAddress   = "192.168.10.20"
	wanAddress   = "203.0.113.9"
	otherAddress = "198.51.100.7"
)

func TestNetworkAccessMatrixPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	lib, other := f.registration.Library.ID, f.other.Library.ID
	granted, everything := f.all(false), f.all(true)
	onlyOther := []string{"movie-other"}
	reset := func(t *testing.T) {
		t.Helper()
		imageRepositoryExec(t, f.jobFixture, `DELETE FROM library_network_rules`)
		imageRepositoryExec(t, f.jobFixture, `DELETE FROM user_item_access_rules`)
	}
	type view struct {
		label     string
		u         contentAccessUser
		address   string
		kind      access.ClientKind
		libraries []string
		want      []string
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
		views []view
	}{
		{"no_rules", func(*testing.T) {}, []view{
			{"viewer wan", f.viewer, wanAddress, access.ClientNative, nil, granted},
			{"viewer without scope", f.viewer, "", "", nil, granted},
			{"admin wan", f.admin, wanAddress, access.ClientWeb, nil, everything},
		}},
		{"lan_only", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: true})
		}, []view{
			{"viewer lan", f.viewer, lanAddress, access.ClientNative, nil, granted},
			{"viewer ula", f.viewer, "fd12:3456::1", access.ClientNative, nil, granted},
			{"viewer loopback", f.viewer, "127.0.0.1", access.ClientWeb, nil, granted},
			{"viewer mapped lan", f.viewer, "::ffff:10.1.2.3", access.ClientWeb, nil, granted},
			{"viewer wan", f.viewer, wanAddress, access.ClientNative, nil, nil},
			{"viewer link local", f.viewer, "169.254.1.1", access.ClientNative, nil, nil},
			// Outside a request every condition is unknown: fail closed.
			{"viewer without address", f.viewer, "", access.ClientNative, nil, nil},
			{"peer wan", f.peer, wanAddress, access.ClientWeb, nil, nil},
			// Administrators answer only to rules that include them.
			{"admin wan", f.admin, wanAddress, access.ClientWeb, nil, everything},
		}},
		{"wan_only", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "wan", Enabled: true})
		}, []view{
			{"viewer lan", f.viewer, lanAddress, access.ClientNative, nil, nil},
			{"viewer wan", f.viewer, wanAddress, access.ClientNative, nil, granted},
		}},
		{"cidr", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "any", CIDRs: []string{"203.0.113.0/24", "2001:db8::/32"}, Enabled: true})
		}, []view{
			{"viewer inside", f.viewer, wanAddress, access.ClientWeb, nil, granted},
			{"viewer inside v6", f.viewer, "2001:db8::5", access.ClientWeb, nil, granted},
			{"viewer outside", f.viewer, otherAddress, access.ClientWeb, nil, nil},
			{"viewer lan outside", f.viewer, lanAddress, access.ClientWeb, nil, nil},
		}},
		{"client_kind", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "any", ClientKinds: []string{"native"}, Enabled: true})
		}, []view{
			{"viewer native", f.viewer, wanAddress, access.ClientNative, nil, granted},
			{"viewer web", f.viewer, wanAddress, access.ClientWeb, nil, nil},
		}},
		{"conditions_and_rules_or", func(t *testing.T) {
			// LAN web, or any native session from one address.
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", ClientKinds: []string{"web"}, Enabled: true})
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "any", CIDRs: []string{otherAddress}, ClientKinds: []string{"native"}, Enabled: true})
		}, []view{
			{"lan web", f.viewer, lanAddress, access.ClientWeb, nil, granted},
			{"lan native", f.viewer, lanAddress, access.ClientNative, nil, nil},
			{"address native", f.viewer, otherAddress, access.ClientNative, nil, granted},
			{"address web", f.viewer, otherAddress, access.ClientWeb, nil, nil},
		}},
		{"disabled_rule", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: false})
		}, []view{{"viewer wan", f.viewer, wanAddress, access.ClientNative, nil, granted}}},
		{"include_admins", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", IncludeAdmins: true, Enabled: true})
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: other, Network: "lan", Enabled: true})
		}, []view{
			{"admin wan", f.admin, wanAddress, access.ClientWeb, nil, onlyOther},
			{"admin lan", f.admin, lanAddress, access.ClientWeb, nil, everything},
			{"viewer wan", f.viewer, wanAddress, access.ClientWeb, nil, nil},
		}},
		{"allow_rule_never_widens_network", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: true})
			if _, err := f.s.SetItemAccessRule(f.ctx, f.a, f.viewer.principal.UserID, f.items["movie-g"], domain.ItemAccessAllow); err != nil {
				t.Fatal(err)
			}
		}, []view{{"viewer wan", f.viewer, wanAddress, access.ClientNative, nil, nil}}},
		// The client control restrict_libraries set (G47) narrows the
		// request like a network rule and applies to administrators too:
		// their exemption is decided by the gate.
		{"client_control_libraries", func(*testing.T) {}, []view{
			{"viewer other only", f.viewer, wanAddress, access.ClientNative, []string{other}, nil},
			{"viewer granted", f.viewer, wanAddress, access.ClientNative, []string{lib, other}, granted},
			{"viewer none", f.viewer, wanAddress, access.ClientNative, []string{}, nil},
			{"admin other only", f.admin, wanAddress, access.ClientNative, []string{other}, onlyOther},
		}},
		{"client_control_and_network", func(t *testing.T) {
			f.networkRule(t, domain.NetworkRuleInput{LibraryID: other, Network: "lan", IncludeAdmins: true, Enabled: true})
		}, []view{
			{"admin wan both", f.admin, wanAddress, access.ClientNative, []string{lib, other}, granted},
			{"admin lan other", f.admin, lanAddress, access.ClientNative, []string{other}, onlyOther},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			tc.setup(t)
			for _, v := range tc.views {
				f.observeIn(scoped(f.ctx, v.u, v.address, v.kind, v.libraries), t, v.label, v.u, v.want)
			}
		})
	}
}

// guestOf redeems a share token into a guest user the matrix observes,
// seeded with the same resume points and statistics as the fixture users.
func (f contentAccessFixture) guestOf(t *testing.T, token string, native bool) contentAccessUser {
	t.Helper()
	in := domain.ShareRedemption{Token: token, DeviceName: "guest browser", IP: "198.51.100.1", MaxSessions: 4, SessionTTL: time.Hour}
	if native {
		in.Native, in.DeviceName = true, "guest tv"
		in.Client = domain.NativeClient{Name: "Guest Player", DeviceID: "guest-device", Device: "guest tv"}
	}
	grant, err := f.s.RedeemShare(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Authenticate(f.ctx, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO user_item_data(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at)
 SELECT $1::uuid,i.id,600000000,false,0,now(),now() FROM items i ON CONFLICT DO NOTHING`, p.UserID)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays)
 SELECT $1::uuid,(now() AT TIME ZONE 'UTC')::date,i.id,i.library_id,60000,1,1,1 FROM items i ON CONFLICT DO NOTHING`, p.UserID)
	return contentAccessUser{principal: p, actor: domain.Actor{UserID: p.UserID, SessionID: p.SessionID, IP: "198.51.100.1"}, token: grant.Token}
}

func (f contentAccessFixture) share(t *testing.T, in domain.ShareInput) domain.ShareGrant {
	t.Helper()
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = time.Now().Add(time.Hour)
	}
	if in.MaxStreams == 0 {
		in.MaxStreams = 1
	}
	g, err := f.s.CreateShare(f.ctx, f.a, in)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestShareGuestMatrixPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	lib := f.registration.Library.ID
	granted := f.all(false)
	in := func(u contentAccessUser) context.Context { return scoped(f.ctx, u, wanAddress, u.principal.Kind, nil) }

	// Native guests are observed so direct delivery is part of the matrix;
	// a web guest never resolves a source.
	library := f.share(t, domain.ShareInput{LibraryID: lib, ReadOnly: true, AllowPlayback: true, Note: "whole library"})
	libraryGuest := f.guestOf(t, library.Token, true)
	if libraryGuest.principal.ShareID != library.Share.ID || !libraryGuest.principal.ShareReadOnly || libraryGuest.principal.Kind != access.ClientNative || libraryGuest.principal.Admin {
		t.Fatalf("guest principal %+v", libraryGuest.principal)
	}
	// A whole-library share shows the library and nothing of another.
	f.observeIn(in(libraryGuest), t, "library guest", libraryGuest, granted)
	views, err := f.s.ListLibraryViews(in(libraryGuest), libraryGuest.principal.UserID)
	if err != nil || len(views) != 1 || views[0].ID != lib {
		t.Fatalf("library guest views %+v %v", views, err)
	}

	// An item share shows the item and its descendants only, and no
	// library of its own.
	series := f.share(t, domain.ShareInput{ItemID: f.items["series-pg"], AllowPlayback: true, MaxStreams: 2})
	seriesGuest := f.guestOf(t, series.Token, true)
	pgSeries := []string{"episode-pg", "episode-pg-tagged", "season-pg", "series-pg"}
	f.observeIn(in(seriesGuest), t, "series guest", seriesGuest, pgSeries)
	if views, err = f.s.ListLibraryViews(in(seriesGuest), seriesGuest.principal.UserID); err != nil || len(views) != 0 {
		t.Fatalf("item share lists libraries %+v %v", views, err)
	}
	season := f.share(t, domain.ShareInput{ItemID: f.items["season-ma"]})
	seasonGuest := f.guestOf(t, season.Token, false)
	f.observeIn(in(seasonGuest), t, "season guest", seasonGuest, []string{"episode-ma", "season-ma"})
	movie := f.share(t, domain.ShareInput{ItemID: f.items["movie-other"], AllowPlayback: true})
	movieGuest := f.guestOf(t, movie.Token, true)
	f.observeIn(in(movieGuest), t, "other library item guest", movieGuest, []string{"movie-other"})
	webGuest := f.guestOf(t, movie.Token, false)
	if _, err = f.s.Resolve(in(webGuest), webGuest.principal, f.sources["movie-other"]); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("web guest resolved a source: %v", err)
	}
	if items, err := f.s.ListItems(in(webGuest), webGuest.principal.UserID, "", 10); err != nil || len(items) != 1 || items[0].ID != f.items["movie-other"] {
		t.Fatalf("web guest items %+v %v", items, err)
	}

	// Content rules on the guest account still narrow the share, and a
	// network rule hides the share's library from a guest outside it.
	if _, err = f.s.SetContentAccess(f.ctx, f.a, seriesGuest.principal.UserID, domain.ContentAccess{BlockedTags: []string{"horror"}}); err != nil {
		t.Fatal(err)
	}
	f.observeIn(in(seriesGuest), t, "series guest blocked tag", seriesGuest, without(pgSeries, "episode-pg-tagged"))
	rule := f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: true})
	f.observeIn(in(libraryGuest), t, "library guest wan", libraryGuest, nil)
	f.observeIn(scoped(f.ctx, libraryGuest, lanAddress, access.ClientWeb, nil), t, "library guest lan", libraryGuest, granted)
	if err = f.s.DeleteNetworkRule(f.ctx, f.a, rule.ID); err != nil {
		t.Fatal(err)
	}
	// Ordinary users are untouched by shares.
	f.observeIn(in(f.viewer), t, "viewer", f.viewer, granted)
	f.observeIn(in(f.peer), t, "peer", f.peer, granted)

	// Playback: the share's stream cap rides on the delivery lookup; a web
	// guest never resolves, a share without playback never issues a
	// native session.
	source, err := f.s.Resolve(in(seriesGuest), seriesGuest.principal, f.sources["movie-g"])
	if !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("series guest resolved a movie outside the share: %+v %v", source, err)
	}
	moviePlay := f.share(t, domain.ShareInput{ItemID: f.items["movie-g"], AllowPlayback: true, MaxStreams: 3})
	player := f.guestOf(t, moviePlay.Token, true)
	if source, err = f.s.Resolve(in(player), player.principal, f.sources["movie-g"]); err != nil || source.ShareStreams != 3 {
		t.Fatalf("guest resolve %+v %v", source, err)
	}
	if source, err = f.s.Resolve(in(f.viewer), f.viewer.principal, f.sources["movie-g"]); err != nil || source.ShareStreams != 0 {
		t.Fatalf("viewer resolve %+v %v", source, err)
	}
	if _, err = f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: season.Token, Native: true, Client: domain.NativeClient{Name: "p", DeviceID: "d"}, MaxSessions: 4, SessionTTL: time.Hour}); !errors.Is(err, domain.ErrSharePlaybackDisabled) {
		t.Fatalf("native session without playback: %v", err)
	}

	// Expiry ends the guest at once, before its session expires.
	imageRepositoryExec(t, f.jobFixture, `UPDATE share_links SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 second' WHERE id=$1::uuid`, season.Share.ID)
	f.observeIn(in(seasonGuest), t, "expired guest", seasonGuest, nil)
	if _, err = f.s.Authenticate(f.ctx, seasonGuest.token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("expired share's session authenticated: %v", err)
	}
	if _, err = f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: season.Token, MaxSessions: 4, SessionTTL: time.Hour}); !errors.Is(err, domain.ErrShareUnavailable) {
		t.Fatalf("expired share redeemed: %v", err)
	}
	if active, err := f.s.SessionActive(f.ctx, seasonGuest.principal.UserID, seasonGuest.principal.SessionID); err != nil || active {
		t.Fatalf("expired guest session active=%t %v", active, err)
	}

	// Revocation revokes the guest sessions in the same transaction.
	revoked, err := f.s.RevokeShare(f.ctx, f.a, moviePlay.Share.ID)
	if err != nil || revoked.State != domain.ShareRevoked || revoked.RevokedAt == nil {
		t.Fatalf("revoke %+v %v", revoked, err)
	}
	if active, err := f.s.SessionActive(f.ctx, player.principal.UserID, player.principal.SessionID); err != nil || active {
		t.Fatalf("revoked guest session active=%t %v", active, err)
	}
	if items, err := f.s.ListItems(in(player), player.principal.UserID, "", 10); err != nil || len(items) != 0 {
		t.Fatalf("revoked guest items %+v %v", items, err)
	}
	if _, err = f.s.GetItem(in(player), player.principal.UserID, f.items["movie-g"]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoked guest item: %v", err)
	}
	if _, err = f.s.Authenticate(f.ctx, player.token); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("revoked guest authenticated: %v", err)
	}
	if again, err := f.s.RevokeShare(f.ctx, f.a, moviePlay.Share.ID); err != nil || !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Fatalf("second revoke changed the share: %+v %v", again, err)
	}
	if _, err = f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: moviePlay.Token, MaxSessions: 4, SessionTTL: time.Hour}); !errors.Is(err, domain.ErrShareUnavailable) {
		t.Fatalf("revoked share redeemed: %v", err)
	}
	if _, err = f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", MaxSessions: 4, SessionTTL: time.Hour}); !errors.Is(err, domain.ErrShareUnavailable) {
		t.Fatalf("unknown token: %v", err)
	}

	// Every use is audited against the share.
	records, _, err := f.s.ListShareAccess(f.ctx, f.a, moviePlay.Share.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, r := range records {
		events = append(events, r.Event)
		if r.Event == "share.redeem_refused" && r.Reason != "revoked" {
			t.Errorf("refusal reason %q", r.Reason)
		}
	}
	for _, want := range []string{"share.created", "share.redeemed", "share.revoked", "share.redeem_refused"} {
		if !slices.Contains(events, want) {
			t.Errorf("share audit lacks %s: %v", want, events)
		}
	}
	if err = f.s.RecordShareAccess(f.ctx, domain.ShareAccess{ShareID: library.Share.ID, Actor: libraryGuest.actor, ClientKind: "web", Route: "GET /api/v1/items/{id}"}); err != nil {
		t.Fatal(err)
	}
	if records, _, err = f.s.ListShareAccess(f.ctx, f.a, library.Share.ID, "", 1); err != nil || len(records) != 1 || records[0].Event != "share.accessed" || records[0].Route != "GET /api/v1/items/{id}" || records[0].IP != "198.51.100.1" {
		t.Fatalf("access record %+v %v", records, err)
	}

	// Guest accounts are invisible to account administration and logins.
	users, err := f.s.ListUsers(f.ctx, f.a, "", 100, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.ID == libraryGuest.principal.UserID {
			t.Fatal("guest account listed")
		}
	}
	if _, err = f.s.Credentials(f.ctx, "share:"+library.Share.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("guest credentials: %v", err)
	}
	current, err := f.s.CurrentShare(f.ctx, libraryGuest.actor)
	if err != nil || current.ID != library.Share.ID || current.LibraryID != lib || !current.ReadOnly {
		t.Fatalf("current share %+v %v", current, err)
	}
	if _, err = f.s.CurrentShare(f.ctx, f.viewer.actor); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("non-guest current share: %v", err)
	}
	shares, err := f.s.ListShares(f.ctx, f.a)
	if err != nil || len(shares) != 5 {
		t.Fatalf("shares %d %v", len(shares), err)
	}
	for _, s := range shares {
		switch s.ID {
		case library.Share.ID:
			if s.State != domain.ShareActive || s.ActiveSessions != 1 || s.LibraryName == "" || s.LastUsedAt == nil {
				t.Errorf("library share %+v", s)
			}
		case season.Share.ID:
			if s.State != domain.ShareExpired || s.ActiveSessions != 0 || s.ItemTitle != "season-ma" {
				t.Errorf("expired share %+v", s)
			}
		}
	}
	// Only administrators administer shares.
	if _, err = f.s.ListShares(f.ctx, f.viewer.actor); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer listed shares: %v", err)
	}
	if _, err = f.s.CreateShare(f.ctx, f.viewer.actor, domain.ShareInput{LibraryID: lib, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer created a share: %v", err)
	}
}

func TestShareSessionLimitsPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	g := f.share(t, domain.ShareInput{LibraryID: f.registration.Library.ID, ExpiresAt: time.Now().Add(10 * time.Minute)})
	redeem := func() (domain.SessionGrant, error) {
		return f.s.RedeemShare(f.ctx, domain.ShareRedemption{Token: g.Token, MaxSessions: 2, SessionTTL: 24 * time.Hour})
	}
	first, err := redeem()
	if err != nil {
		t.Fatal(err)
	}
	// A session never outlives its share.
	if until := time.Until(first.Session.ExpiresAt); until > 11*time.Minute || until < 8*time.Minute {
		t.Fatalf("guest session expires in %v", until)
	}
	if first.User.Name != "share:"+g.Share.ID || !first.User.Hidden || first.User.Admin {
		t.Fatalf("guest user %+v", first.User)
	}
	if _, err = redeem(); err != nil {
		t.Fatal(err)
	}
	if _, err = redeem(); !errors.Is(err, domain.ErrSessionLimit) {
		t.Fatalf("third guest session: %v", err)
	}
	// Invalid inputs.
	for _, in := range []domain.ShareInput{
		{LibraryID: f.registration.Library.ID, ItemID: f.items["movie-g"], ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1},
		{LibraryID: f.registration.Library.ID, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 0},
		{LibraryID: f.registration.Library.ID, ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 17},
	} {
		if _, err = f.s.CreateShare(f.ctx, f.a, in); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("share %+v: %v", in, err)
		}
	}
	if _, err = f.s.CreateShare(f.ctx, f.a, domain.ShareInput{ItemID: "00000000-0000-4000-8000-000000000001", ExpiresAt: time.Now().Add(time.Hour), MaxStreams: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("share of a missing item: %v", err)
	}
	// The guest account cannot be made an administrator.
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, first.User.ID); err == nil {
		t.Fatal("guest account became an administrator")
	}
}

func TestNetworkRuleAdministrationPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	lib := f.registration.Library.ID
	r, err := f.s.CreateNetworkRule(f.ctx, f.a, domain.NetworkRuleInput{LibraryID: lib, Network: "any", CIDRs: []string{"10.1.2.3", "10.1.2.0/24", "::ffff:192.168.1.7/120", "2001:db8::1/32"}, ClientKinds: []string{"native", "web"}, Enabled: true, Note: "office"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"10.1.2.0/24", "10.1.2.3/32", "192.168.1.0/24", "2001:db8::/32"}; !slices.Equal(r.CIDRs, want) || r.LibraryName == "" || !slices.Equal(r.ClientKinds, []string{"native", "web"}) {
		t.Fatalf("normalized rule %+v", r)
	}
	if same, err := f.s.UpdateNetworkRule(f.ctx, f.a, r.ID, r.NetworkRuleInput); err != nil || !same.UpdatedAt.Equal(r.UpdatedAt) {
		t.Fatalf("unchanged update %+v %v", same, err)
	}
	changed := r.NetworkRuleInput
	changed.Network, changed.CIDRs = "lan", []string{}
	if r, err = f.s.UpdateNetworkRule(f.ctx, f.a, r.ID, changed); err != nil || r.Network != "lan" || len(r.CIDRs) != 0 {
		t.Fatalf("update %+v %v", r, err)
	}
	for _, bad := range []domain.NetworkRuleInput{
		{LibraryID: lib, Network: "office", CIDRs: []string{}, ClientKinds: []string{}, Enabled: true},
		{LibraryID: lib, Network: "any", CIDRs: []string{"not-an-address"}, ClientKinds: []string{}, Enabled: true},
		{LibraryID: lib, Network: "any", CIDRs: []string{"fe80::1%eth0"}, ClientKinds: []string{}, Enabled: true},
		{LibraryID: lib, Network: "any", CIDRs: []string{}, ClientKinds: []string{"tv"}, Enabled: true},
		{LibraryID: lib, Network: "any", CIDRs: []string{}, ClientKinds: []string{"web", "web"}, Enabled: true},
	} {
		if _, err = f.s.CreateNetworkRule(f.ctx, f.a, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("rule %+v: %v", bad, err)
		}
	}
	if _, err = f.s.CreateNetworkRule(f.ctx, f.a, domain.NetworkRuleInput{LibraryID: "00000000-0000-4000-8000-000000000001", Network: "lan", CIDRs: []string{}, ClientKinds: []string{}, Enabled: true}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rule of a missing library: %v", err)
	}
	if _, err = f.s.ListNetworkRules(f.ctx, f.viewer.actor); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer listed rules: %v", err)
	}
	rules, err := f.s.ListNetworkRules(f.ctx, f.a)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules %+v %v", rules, err)
	}
	if err = f.s.DeleteNetworkRule(f.ctx, f.a, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteNetworkRule(f.ctx, f.a, r.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event LIKE 'access.network_rule_%' AND target_id=$1::uuid`, r.ID); n != 3 {
		t.Fatalf("network rule audit rows %d, want created, updated and deleted", n)
	}
}

func TestShareNetworkMigrationRoundTrip(t *testing.T) {
	f := newContentAccessFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	lib := f.registration.Library.ID
	rule := f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: true})
	live := f.share(t, domain.ShareInput{LibraryID: lib})
	guest := f.guestOf(t, live.Token, false)
	if _, err := f.s.CreateClientRule(f.ctx, f.a, domain.ClientRuleInput{Dimension: "ip", Match: "cidr", Pattern: "0.0.0.0/0", Action: "restrict_libraries", Libraries: []string{lib},
		ScopeKind: "global", ScopeValues: []string{}, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture, "share_network_access")
	refuse := func(what string) {
		t.Helper()
		if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
			t.Fatalf("downgraded with %s", what)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
			t.Fatal(err)
		}
	}
	refuse("a network rule")
	if err := f.s.DeleteNetworkRule(f.ctx, f.a, rule.ID); err != nil {
		t.Fatal(err)
	}
	refuse("a restrict_libraries rule")
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM client_rules`)
	refuse("a live share")
	if _, err := f.s.RevokeShare(f.ctx, f.a, live.Share.ID); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM users WHERE id=$1::uuid`, guest.principal.UserID) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('share_links','library_network_rules')`) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND ((table_name='users' AND column_name='share_id') OR (table_name='client_rules' AND column_name='libraries'))`) != 0 {
		t.Fatal("downgrade left share or network schema")
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event LIKE 'share.%' OR event LIKE 'access.network_rule_%'`) < 4 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.networkRule(t, domain.NetworkRuleInput{LibraryID: lib, Network: "lan", Enabled: true})
	f.observeIn(scoped(f.ctx, f.viewer, wanAddress, access.ClientWeb, nil), t, "viewer wan after round trip", f.viewer, nil)
	f.observeIn(scoped(f.ctx, f.viewer, lanAddress, access.ClientWeb, nil), t, "viewer lan after round trip", f.viewer, f.all(false))
}
