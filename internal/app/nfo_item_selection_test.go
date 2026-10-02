package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type selectingFusionReader struct {
	nfoApplyTestReader
	mode string
}

func (r *selectingFusionReader) SelectItemNFO(ctx context.Context, scope domain.NFOItemScope) (domain.NFOItemSelection, error) {
	fields, err := r.ReadItemFields(ctx, scope.Source, scope.Kind)
	selected := domain.NFOItemSelection{RelativePath: scope.Source.RelativePath, CandidateDigest: domain.NFOCandidateDigest([]string{scope.Source.RelativePath}), Fields: fields}
	if r.mode == "outside" {
		selected.RelativePath = "elsewhere/film.nfo"
	}
	if r.calls == 2 {
		switch r.mode {
		case "names":
			selected.CandidateDigest = domain.NFOCandidateDigest([]string{scope.Source.RelativePath, "movie.nfo"})
		case "selected":
			selected.RelativePath = "movie.nfo"
		case "changed":
			return domain.NFOItemSelection{}, domain.ErrNFOSourceChanged
		}
	}
	return selected, err
}

func TestNFOSelectionChangesPreventProviderCommit(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"names", "selected", "outside", "changed"} {
		t.Run(mode, func(t *testing.T) {
			provider := &applyProviderStub{}
			repo := &fusionRepositoryStub{applyRepositoryStub: applyRepositoryStub{before: domain.ItemMetadata{ItemID: id, LibraryID: id, Kind: "Movie", Revision: 1, NFOMode: domain.NFOModeReadOnly}}, scope: domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "film.mkv", Source: domain.NFOSource{RootPath: "/trusted", RelativePath: "film.nfo"}}}
			reader := &selectingFusionReader{mode: mode, nfoApplyTestReader: nfoApplyTestReader{value: domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: "Movie", Identity: domain.DefaultNFOIdentity(), Stamp: domain.NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{{Field: "title", Value: "Local"}}}}}
			service, _ := NewMetadata(provider)
			service, _ = service.WithItemMetadata(repo)
			service, _ = service.WithNFOItemFields(reader)
			_, err := service.ApplyTMDB(context.Background(), domain.Actor{UserID: id, SessionID: id}, id, domain.TMDBMetadataApplyInput{Resource: "movie", ProviderID: 12, ExpectedRevision: 1, Confirmed: true, Language: "en-US"})
			want, calls := domain.ErrConflict, 1
			if mode == "outside" {
				want, calls = domain.ErrMetadataUnavailable, 0
			}
			if err != want || repo.fusionCalls != 0 || provider.calls != calls {
				t.Fatal("selection change or forged scope reached commit", err, provider.calls, repo.fusionCalls)
			}
		})
	}
}
