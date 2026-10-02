package runtime

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type runtimeNFOItemRepository struct {
	runtimeMetadataRepository
	scope    domain.NFOItemScope
	observed domain.NFOItemFields
}

func (r *runtimeNFOItemRepository) ResolveItemNFO(context.Context, domain.Actor, string, int64) (domain.NFOItemScope, error) {
	return r.scope, nil
}
func (r *runtimeNFOItemRepository) ApplyItemNFO(_ context.Context, _ domain.Actor, _ domain.NFOItemScope, fields domain.NFOItemFields) (domain.MetadataApplyResult, error) {
	r.observed = fields
	return domain.MetadataApplyResult{Metadata: domain.ItemMetadata{Revision: 2}}, nil
}

func TestNFOItemProductionReaderBindingWithAndWithoutTMDB(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, key := range []string{"", strings.Repeat("a", 32)} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "film.mkv"), []byte("original media fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "film.nfo"), []byte(`<movie><title>Bound NFO</title></movie>`), 0600); err != nil {
			t.Fatal(err)
		}
		l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
		original, err := prepareMetadata(key, l)
		if err != nil {
			t.Fatal(err)
		}
		repo := &runtimeNFOItemRepository{scope: domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "film.mkv", Source: domain.NFOSource{RootPath: root, RelativePath: "film.nfo"}}}
		service, err := bindMetadata(original, repo)
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ApplyNFO(context.Background(), domain.Actor{UserID: id, SessionID: id}, id, 1, true)
		if err != nil || result.Metadata.Revision != 2 || !domain.ValidNFOItemFields(repo.observed) || repo.observed.Fields[0].Value != "Bound NFO" {
			t.Fatal("production NFO reader missing", err)
		}
		l.closePool()
	}
}
