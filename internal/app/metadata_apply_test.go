package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
	"time"
)

type applyRepositoryStub struct {
	ItemMetadataRepository
	before domain.ItemMetadata
	calls  int
	update domain.TMDBMetadataUpdate
}

func (r *applyRepositoryStub) ItemMetadata(context.Context, domain.Actor, string) (domain.ItemMetadata, error) {
	return r.before, nil
}
func (r *applyRepositoryStub) ApplyTMDBMetadata(_ context.Context, _ domain.Actor, _ string, _ int64, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	r.calls++
	r.update = update
	return domain.MetadataApplyResult{Metadata: r.before, Applied: []string{"title"}}, nil
}

type applyProviderStub struct {
	MetadataProvider
	calls     int
	badSource bool
}

func (p *applyProviderStub) Movie(_ context.Context, id int32, language string) (domain.MovieCandidate, error) {
	p.calls++
	source := domain.TMDBSourceURL("movie", id)
	if p.badSource {
		source = "https://example.com"
	}
	return domain.MovieCandidate{ProviderID: id, Source: "TMDB", SourceURL: source, Language: language, FetchedAt: time.Now().UTC(), Title: "Provider title", Overview: "Overview", ReleaseDate: "2024-02-29"}, nil
}

func TestTMDBMetadataApplicationConfirmationAndSourceValidation(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	provider := &applyProviderStub{}
	repo := &applyRepositoryStub{before: domain.ItemMetadata{ItemID: id, LibraryID: id, Kind: "Movie", Revision: 1, NFOMode: domain.NFOModeOff}}
	service, _ := NewMetadata(provider)
	service, _ = service.WithItemMetadata(repo)
	actor := domain.Actor{UserID: id, SessionID: id}
	input := domain.TMDBMetadataApplyInput{Resource: "movie", ProviderID: 12, ExpectedRevision: 1, Language: "en-US"}
	if _, err := service.ApplyTMDB(context.Background(), actor, id, input); err != domain.ErrInvalid || provider.calls != 0 {
		t.Fatal("unconfirmed candidate fetched", err)
	}
	input.Confirmed = true
	input.ExpectedRevision = 2
	if _, err := service.ApplyTMDB(context.Background(), actor, id, input); err != domain.ErrConflict || provider.calls != 0 {
		t.Fatal("stale input fetched", err)
	}
	input.ExpectedRevision = 1
	provider.badSource = true
	if _, err := service.ApplyTMDB(context.Background(), actor, id, input); err != domain.ErrMetadataUnavailable || repo.calls != 0 {
		t.Fatal("unsafe source persisted", err)
	}
	provider.badSource = false
	if _, err := service.ApplyTMDB(context.Background(), actor, id, input); err != nil || repo.calls != 1 || len(repo.update.Fields) != 3 || repo.update.Fields[1].Origin.RequestedLanguage != "en-US" {
		t.Fatal("validated provider fields not wired", err)
	}
}
