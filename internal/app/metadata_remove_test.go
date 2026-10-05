package app

import (
	"context"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type metadataRemoveRepositoryStub struct {
	itemMetadataRepositoryStub
	removeCalls int
	expected    int64
	result      domain.MetadataRemoveResult
	err         error
}

func (r *metadataRemoveRepositoryStub) RemoveExternalMetadata(_ context.Context, _ domain.Actor, _ string, expected int64) (domain.MetadataRemoveResult, error) {
	r.removeCalls++
	r.expected = expected
	return r.result, r.err
}

func TestMetadataRemoveExternalApplication(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	a := domain.Actor{UserID: id, SessionID: id}
	repo := &metadataRemoveRepositoryStub{result: domain.MetadataRemoveResult{Removed: []string{"overview"}, Skipped: []domain.MetadataFieldSkip{}, Metadata: domain.ItemMetadata{ItemID: id, Revision: 3, Fields: []domain.ItemMetadataField{{Field: "title", Value: "film", Source: "existing"}}}}}
	service, err := NewLocalMetadata(repo)
	if err != nil {
		t.Fatal(err)
	}
	// No provider is configured: removal never needs TMDB credentials.
	value, err := service.RemoveExternal(context.Background(), a, id, 2)
	if err != nil || repo.expected != 2 || value.Removed[0] != "overview" {
		t.Fatal("removal not delegated", err, value)
	}
	value.Removed[0], value.Metadata.Fields[0].Value = "caller", "caller"
	if repo.result.Removed[0] != "overview" || repo.result.Metadata.Fields[0].Value != "film" {
		t.Fatal("response shared repository state")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx      context.Context
		actor    domain.Actor
		item     string
		expected int64
		want     error
	}{
		{cancelled, a, id, 2, context.Canceled},
		{context.Background(), domain.Actor{}, id, 2, domain.ErrUnauthenticated},
		{context.Background(), a, "bad", 2, domain.ErrInvalid},
		{context.Background(), a, id, 0, domain.ErrInvalid},
		{context.Background(), a, id, domain.ItemMetadataRevisionMax, domain.ErrInvalid},
	} {
		if _, err := service.RemoveExternal(tc.ctx, tc.actor, tc.item, tc.expected); err != tc.want {
			t.Fatal("invalid removal accepted", err, tc.want)
		}
	}
	repo.err = domain.ErrConflict
	if _, err := service.RemoveExternal(context.Background(), a, id, 2); err != domain.ErrConflict {
		t.Fatal("conflict not propagated", err)
	}
	if repo.removeCalls != 2 {
		t.Fatal("invalid input reached repository", repo.removeCalls)
	}
	plain, err := NewLocalMetadata(&itemMetadataRepositoryStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.RemoveExternal(context.Background(), a, id, 2); err != domain.ErrMetadataUnavailable {
		t.Fatal("missing removal repository", err)
	}
	var missing *Metadata
	if _, err := missing.RemoveExternal(context.Background(), a, id, 2); err != domain.ErrMetadataUnavailable {
		t.Fatal("nil metadata service", err)
	}
}
