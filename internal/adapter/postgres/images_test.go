package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func imageRepositoryFixture(t *testing.T) (jobFixture, string, string) {
	t.Helper()
	f := newJobFixture(t)
	item := metadataItem(t, f)
	var source string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type)
 VALUES($1::uuid,$2::uuid,$3::uuid,'movie/Film.mkv','video/x-matroska') RETURNING id::text`, item, f.registration.Library.ID, f.registration.RootID).Scan(&source); err != nil {
		t.Fatal("create image repository source")
	}
	return f, item, source
}

func imageRepositoryActor(t *testing.T, f jobFixture, name string, kind access.ClientKind) domain.Actor {
	t.Helper()
	token, err := f.s.Provision(f.ctx, name, kind, false)
	if err != nil {
		t.Fatal("provision image reader")
	}
	principal, err := f.s.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatal("authenticate image reader")
	}
	return domain.Actor{UserID: principal.UserID, SessionID: principal.SessionID}
}

func imageRepositoryExec(t *testing.T, f jobFixture, query string, arguments ...any) {
	t.Helper()
	if _, err := f.s.Pool.Exec(f.ctx, query, arguments...); err != nil {
		t.Fatal("update owned image repository fixture")
	}
}

func TestImageSourceRepositoryWebNativeAccessAndPrivateBinding(t *testing.T) {
	f, item, source := imageRepositoryFixture(t)
	value, err := f.s.ResolveImageSource(f.ctx, f.a, item)
	if err != nil || value.ItemID != item || value.SourceID != source || value.LibraryID != f.registration.Library.ID || value.RootID != f.registration.RootID || value.MediaPath != "movie/Film.mkv" || value.RootPath == "" {
		t.Fatal("administrator source binding differs", err)
	}
	if text := fmt.Sprintf("%v %#v", value, value); strings.Contains(text, value.RootPath) || strings.Contains(text, "Film") {
		t.Fatal("private source diagnostics expose paths")
	}
	for _, kind := range []access.ClientKind{access.ClientWeb, access.ClientNative} {
		actor := imageRepositoryActor(t, f, "image-"+string(kind), kind)
		if v, err := f.s.ResolveImageSource(f.ctx, actor, item); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
			t.Fatal("reader without library access resolved image")
		}
		imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, actor.UserID, value.LibraryID)
		if v, err := f.s.ResolveImageSource(f.ctx, actor, item); err != nil || v != value {
			t.Fatal("authorized web/native reader rejected", err)
		}
		imageRepositoryExec(t, f, `DELETE FROM library_acl WHERE user_id=$1::uuid AND library_id=$2::uuid`, actor.UserID, value.LibraryID)
		if v, err := f.s.ResolveImageSource(f.ctx, actor, item); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
			t.Fatal("revoked library access still resolved image")
		}
	}
	imageRepositoryExec(t, f, `UPDATE media_sources SET relative_path='movie/New.mkv' WHERE id=$1::uuid`, source)
	updated, err := f.s.ResolveImageSource(f.ctx, f.a, item)
	if err != nil || updated == value || updated.MediaPath != "movie/New.mkv" {
		t.Fatal("source resolver reused stale item binding")
	}
}

func TestImageSourceRepositoryRechecksSessionAndUser(t *testing.T) {
	f, item, _ := imageRepositoryFixture(t)
	actor := imageRepositoryActor(t, f, "image-live-session", access.ClientWeb)
	imageRepositoryExec(t, f, `INSERT INTO library_acl(user_id,library_id) VALUES($1::uuid,$2::uuid)`, actor.UserID, f.registration.Library.ID)
	if _, err := f.s.ResolveImageSource(f.ctx, actor, item); err != nil {
		t.Fatal("initial image source unavailable", err)
	}
	for _, check := range []struct {
		name, mutate, restore string
		id                    string
	}{
		{"disabled", `UPDATE users SET disabled=true WHERE id=$1::uuid`, `UPDATE users SET disabled=false WHERE id=$1::uuid`, actor.UserID},
		{"deleted", `UPDATE users SET deleted_at=clock_timestamp() WHERE id=$1::uuid`, `UPDATE users SET deleted_at=NULL WHERE id=$1::uuid`, actor.UserID},
		{"revoked", `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, `UPDATE sessions SET revoked_at=NULL WHERE id=$1::uuid`, actor.SessionID},
		{"expired", `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1::uuid`, actor.SessionID},
	} {
		t.Run(check.name, func(t *testing.T) {
			imageRepositoryExec(t, f, check.mutate, check.id)
			defer imageRepositoryExec(t, f, check.restore, check.id)
			if v, err := f.s.ResolveImageSource(f.ctx, actor, item); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
				t.Fatal("invalid live identity resolved image")
			}
		})
	}
	other := imageRepositoryActor(t, f, "image-other-session", access.ClientNative)
	for _, invalid := range []domain.Actor{{}, {UserID: actor.UserID, SessionID: other.SessionID}, {UserID: other.UserID, SessionID: actor.SessionID}} {
		if v, err := f.s.ResolveImageSource(f.ctx, invalid, item); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
			t.Fatal("mismatched or missing identity resolved image")
		}
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if v, err := f.s.ResolveImageSource(cancelled, actor, item); v != (domain.LocalImageSource{}) || err != context.Canceled {
		t.Fatal("image resolver ignored cancellation")
	}
	if v, err := f.s.ResolveImageSource(nil, actor, item); v != (domain.LocalImageSource{}) || err != domain.ErrInvalid {
		t.Fatal("image resolver accepted nil context")
	}
}

func TestImageSourceRepositoryRejectsMissingAndAmbiguousSources(t *testing.T) {
	f, item, source := imageRepositoryFixture(t)
	missing := metadataItem(t, f)
	for _, id := range []string{"invalid", "10000000-0000-4000-8000-000000000099", missing} {
		if v, err := f.s.ResolveImageSource(f.ctx, f.a, id); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
			t.Fatal("missing item/source returned private data")
		}
	}
	imageRepositoryExec(t, f, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type)
 VALUES($1::uuid,$2::uuid,$3::uuid,'movie/Other.mkv','video/x-matroska')`, item, f.registration.Library.ID, f.registration.RootID)
	if v, err := f.s.ResolveImageSource(f.ctx, f.a, item); v != (domain.LocalImageSource{}) || err != domain.ErrNotFound {
		t.Fatal("ambiguous media source selected arbitrarily")
	}
	imageRepositoryExec(t, f, `DELETE FROM media_sources WHERE item_id=$1::uuid AND id<>$2::uuid`, item, source)
	if _, err := f.s.ResolveImageSource(f.ctx, f.a, item); err != nil {
		t.Fatal("unique source not recovered", err)
	}
}
