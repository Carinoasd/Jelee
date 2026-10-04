package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func trackPrincipal(t *testing.T, f jobFixture, name string, kind access.ClientKind, admin bool) access.Principal {
	t.Helper()
	token, err := f.s.Provision(f.ctx, name, kind, admin)
	if err != nil {
		t.Fatal("provision track reader")
	}
	principal, err := f.s.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatal("authenticate track reader")
	}
	return principal
}

// G10.9 / G48.2: two users granted different libraries each reach only the
// external tracks of their own library, through the same native-only,
// live-session rules as the source stream.
func TestResolveTrackForDirectDelivery(t *testing.T) {
	f := newSidecarFixture(t)
	sub, aud := sidecarInput(f.jobFixture, "Movie.en.srt"), sidecarInput(f.jobFixture, "Movie.ja.flac")
	sub.Charset = "Shift_JIS"
	sidecarUpsert(t, f.jobFixture, f.source, sub, aud)

	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var otherItem, otherSource string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Other','Movie') RETURNING id::text`, other.Library.ID).Scan(&otherItem); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type)
 VALUES($1::uuid,$2::uuid,$3::uuid,'Other/Other.mkv','video/x-matroska') RETURNING id::text`, otherItem, other.Library.ID, other.RootID).Scan(&otherSource); err != nil {
		t.Fatal(err)
	}
	otherTrack := domain.SidecarTrackInput{Track: domain.SidecarTrack{Kind: domain.SidecarKindSubtitle, Format: "ass"}, RootID: other.RootID,
		RelativePath: "Other/Other.ass", Size: 10, ModifiedUnixNano: 1}
	sidecarUpsert(t, f.jobFixture, otherSource, otherTrack)

	ids := map[string]string{}
	rows, err := f.s.Pool.Query(f.ctx, `SELECT relative_path,id::text FROM media_sidecar_tracks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var path, id string
		if err := rows.Scan(&path, &id); err != nil {
			t.Fatal(err)
		}
		ids[path] = id
	}
	rows.Close()
	subID, audID, otherID := ids["Movie/Movie.en.srt"], ids["Movie/Movie.ja.flac"], ids["Other/Other.ass"]
	if subID == "" || audID == "" || otherID == "" {
		t.Fatal("sidecar fixture incomplete", len(ids))
	}

	alice := trackPrincipal(t, f.jobFixture, "track-alice", access.ClientNative, false)
	bob := trackPrincipal(t, f.jobFixture, "track-bob", access.ClientNative, false)
	admin := trackPrincipal(t, f.jobFixture, "track-admin", access.ClientNative, true)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid),($3::uuid,$4::uuid)`,
		alice.UserID, f.registration.Library.ID, bob.UserID, other.Library.ID)
	imageRepositoryExec(t, f.jobFixture, `UPDATE users SET max_streams=2,max_kbps=800 WHERE id=$1::uuid`, alice.UserID)
	var root, otherRoot string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, other.RootID).Scan(&otherRoot); err != nil {
		t.Fatal(err)
	}

	type lookup struct {
		source  string
		kind    media.TrackKind
		track   string
		root    string
		path    string
		charset string
	}
	aliceSub := lookup{f.source, media.TrackSubtitle, subID, root, "Movie/Movie.en.srt", "Shift_JIS"}
	aliceAud := lookup{f.source, media.TrackAudio, audID, root, "Movie/Movie.ja.flac", ""}
	bobSub := lookup{otherSource, media.TrackSubtitle, otherID, otherRoot, "Other/Other.ass", ""}
	allowed := func(p access.Principal, l lookup) media.Source {
		t.Helper()
		got, err := f.s.ResolveTrack(f.ctx, p, l.source, l.kind, l.track)
		if err != nil || got.Root != l.root || got.RelativePath != l.path || got.Charset != l.charset || got.ContentType != "" || got.ETag != "" {
			t.Fatalf("resolve %s: %v", l.path, err)
		}
		return got
	}
	denied := func(stage string, p access.Principal, l lookup) {
		t.Helper()
		got, err := f.s.ResolveTrack(f.ctx, p, l.source, l.kind, l.track)
		if !errors.Is(err, media.ErrNotFound) || got.Root != "" || got.RelativePath != "" {
			t.Fatalf("%s: resolved %v", stage, err)
		}
	}

	if got := allowed(alice, aliceSub); got.Limits.MaxStreams == nil || *got.Limits.MaxStreams != 2 || got.Limits.MaxKbps == nil || *got.Limits.MaxKbps != 800 {
		t.Fatal("track lookup lost the user's delivery limits")
	}
	allowed(alice, aliceAud)
	allowed(bob, bobSub)
	for _, l := range []lookup{aliceSub, aliceAud, bobSub} {
		allowed(admin, l)
	}
	denied("alice reads bob's track", alice, bobSub)
	denied("bob reads alice's subtitle", bob, aliceSub)
	denied("bob reads alice's audio", bob, aliceAud)
	// Mixing a visible source with a track of another source never binds.
	denied("track of a foreign source", alice, lookup{f.source, media.TrackSubtitle, otherID, "", "", ""})
	denied("foreign track through admin", admin, lookup{otherSource, media.TrackSubtitle, subID, "", "", ""})
	denied("subtitle as audio", alice, lookup{f.source, media.TrackAudio, subID, "", "", ""})
	denied("audio as subtitle", admin, lookup{f.source, media.TrackSubtitle, audID, "", "", ""})
	denied("source id as track", admin, lookup{f.source, media.TrackSubtitle, f.source, "", "", ""})
	denied("unknown kind", admin, lookup{f.source, media.TrackKind("video"), subID, "", "", ""})
	denied("malformed ids", admin, lookup{"invalid", media.TrackSubtitle, "invalid", "", "", ""})
	denied("missing track", admin, lookup{f.source, media.TrackSubtitle, "10000000-0000-4000-8000-000000000099", "", "", ""})

	// Web sessions never resolve media, even for an administrator.
	web := trackPrincipal(t, f.jobFixture, "track-web-admin", access.ClientWeb, true)
	denied("web administrator", web, aliceSub)
	// Another user's session does not borrow alice's grant.
	denied("mixed session", access.Principal{UserID: alice.UserID, SessionID: bob.SessionID, Kind: access.ClientNative}, aliceSub)
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM library_acl WHERE user_id=$1::uuid`, alice.UserID)
	denied("grant removed", alice, aliceSub)
	imageRepositoryExec(t, f.jobFixture, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, alice.UserID, f.registration.Library.ID)
	allowed(alice, aliceSub)
	imageRepositoryExec(t, f.jobFixture, `UPDATE users SET disabled=true WHERE id=$1::uuid`, alice.UserID)
	denied("disabled user", alice, aliceSub)
	imageRepositoryExec(t, f.jobFixture, `UPDATE users SET disabled=false WHERE id=$1::uuid`, alice.UserID)
	imageRepositoryExec(t, f.jobFixture, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, alice.SessionID)
	denied("revoked session", alice, aliceSub)
	// Deleting the source removes its tracks with it.
	imageRepositoryExec(t, f.jobFixture, `DELETE FROM media_sources WHERE id=$1::uuid`, otherSource)
	denied("deleted source", admin, bobSub)

	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := f.s.ResolveTrack(cancelled, admin, aliceSub.source, aliceSub.kind, aliceSub.track); err == nil || errors.Is(err, media.ErrNotFound) {
		t.Fatal("cancelled lookup was not an error", err)
	}
}
