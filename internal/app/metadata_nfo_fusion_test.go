package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fusionRepositoryStub struct {
	applyRepositoryStub
	scope       domain.NFOItemScope
	fusionCalls int
	fail        bool
}

func (r *fusionRepositoryStub) ResolveItemNFO(context.Context, domain.Actor, string, int64) (domain.NFOItemScope, error) {
	return r.scope, nil
}
func (r *fusionRepositoryStub) ApplyTMDBWithNFO(_ context.Context, _ domain.Actor, scope domain.NFOItemScope, nfo domain.NFOItemFields, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	r.fusionCalls++
	if r.fail {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	if scope != r.scope || !domain.ValidNFOItemFields(nfo) || !domain.ValidTMDBMetadataUpdate(update) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	return domain.MetadataApplyResult{Metadata: r.before}, nil
}

type orderedFusionReader struct {
	nfoApplyTestReader
	provider *applyProviderStub
}

func (r *orderedFusionReader) ReadItemFields(ctx context.Context, path domain.NFOSource, kind string) (domain.NFOItemFields, error) {
	if r.calls == 0 && r.provider.calls != 0 || r.calls == 1 && r.provider.calls != 1 {
		return domain.NFOItemFields{}, domain.ErrInvalid
	}
	return r.nfoApplyTestReader.ReadItemFields(ctx, path, kind)
}

func TestNFOFusionRereadsAfterProviderAndPropagatesFinalConflict(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"success", "changed-source", "database-conflict"} {
		t.Run(mode, func(t *testing.T) {
			provider := &applyProviderStub{}
			repo := &fusionRepositoryStub{applyRepositoryStub: applyRepositoryStub{before: domain.ItemMetadata{ItemID: id, LibraryID: id, Kind: "Movie", Revision: 1, NFOMode: domain.NFOModeReadOnly}}, scope: domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "film.mkv", Source: domain.NFOSource{RootPath: "/trusted", RelativePath: "film.nfo"}}, fail: mode == "database-conflict"}
			reader := &orderedFusionReader{provider: provider, nfoApplyTestReader: nfoApplyTestReader{value: domain.NFOItemFields{Version: domain.NFOItemFieldsVersion, Kind: "Movie", Identity: domain.DefaultNFOIdentity(), Stamp: domain.NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: domain.NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []domain.NFOTextField{{Field: "title", Value: "Local"}}}}}
			if mode == "changed-source" {
				reader.change = "hash"
			}
			service, _ := NewMetadata(provider)
			service, _ = service.WithItemMetadata(repo)
			service, _ = service.WithNFOItemFields(reader)
			_, err := service.ApplyTMDB(context.Background(), domain.Actor{UserID: id, SessionID: id}, id, domain.TMDBMetadataApplyInput{Resource: "movie", ProviderID: 12, ExpectedRevision: 1, Confirmed: true, Language: "en-US"})
			if provider.calls != 1 || reader.calls != 2 || repo.calls != 0 {
				t.Fatal("provider/NFO ordering or separate write violated")
			}
			if mode == "success" && (err != nil || repo.fusionCalls != 1) {
				t.Fatal("fusion missing", err)
			}
			if mode != "success" && err != domain.ErrConflict {
				t.Fatal("final conflict lost", err)
			}
			if mode == "changed-source" && repo.fusionCalls != 0 {
				t.Fatal("changed source reached write")
			}
		})
	}
}
