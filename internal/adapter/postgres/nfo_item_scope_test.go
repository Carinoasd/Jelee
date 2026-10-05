package postgres

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOItemScopeAuthorizationAndSourceOwnership(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
	library := f.registration.Library.ID
	var root, source string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, library).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'dir/Film.mkv','video/x-matroska') RETURNING id::text`, item, library, root).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1); err != domain.ErrMetadataUnavailable {
		t.Fatal("NFO off was executable", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_mode='read-only',nfo_generation=2 WHERE id=$1::uuid`, library); err != nil {
		t.Fatal(err)
	}
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil || !domain.ValidNFOItemScope(scope) || scope.SourceID != source || scope.RootID != root || scope.LibraryID != library || scope.Source.RelativePath != "dir/Film.nfo" || scope.Generation != 2 {
		t.Fatal("source ownership differs", err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", scope, scope), "Film") || strings.Contains(fmt.Sprint(scope), scope.Source.RootPath) {
		t.Fatal("private paths exposed")
	}
	if _, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 2); err != domain.ErrConflict {
		t.Fatal("stale revision resolved", err)
	}
	if _, err := f.s.ResolveItemNFO(f.ctx, domain.Actor{}, item, 1); err != domain.ErrUnauthenticated {
		t.Fatal("anonymous resolver accepted", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'dir/Other.mkv','video/x-matroska')`, item, library, root); err != nil {
		t.Fatal(err)
	}
	if value, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1); err != domain.ErrMetadataUnavailable || value.Source.RootPath != "" {
		t.Fatal("ambiguous source selected", err)
	}
}
