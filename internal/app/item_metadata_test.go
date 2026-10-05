package app

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type itemMetadataRepositoryStub struct {
	calls int
	value domain.ItemMetadata
}

func (r *itemMetadataRepositoryStub) ItemMetadata(_ context.Context, _ domain.Actor, _ string) (domain.ItemMetadata, error) {
	r.calls++
	return r.value, nil
}
func (r *itemMetadataRepositoryStub) UpdateItemMetadata(_ context.Context, _ domain.Actor, _ string, _ int64, patches []domain.ItemMetadataPatch) (domain.ItemMetadata, error) {
	r.calls++
	*patches[0].Value = "repository-owned"
	*patches[0].Locked = false
	return r.value, nil
}

func TestItemMetadataApplicationOwnershipAndValidation(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	now := time.Now().UTC()
	repo := &itemMetadataRepositoryStub{value: domain.ItemMetadata{ItemID: id, Revision: 2, Fields: []domain.ItemMetadataField{{Field: "title", Value: "Title", UpdatedAt: &now}}}}
	service, err := NewLocalMetadata(repo)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.Actor{UserID: id, SessionID: id}
	title := "Caller title"
	lock := true
	value, err := service.UpdateItemFields(context.Background(), a, id, 1, []domain.ItemMetadataPatch{{Field: "title", Value: &title, Locked: &lock}})
	if err != nil || title != "Caller title" || !lock {
		t.Fatal("repository mutated caller pointers", err)
	}
	value.Fields[0].Value = "changed"
	*value.Fields[0].UpdatedAt = time.Time{}
	if repo.value.Fields[0].Value != "Title" || repo.value.Fields[0].UpdatedAt.IsZero() {
		t.Fatal("response shared repository state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = service.ItemFields(ctx, a, id); err != context.Canceled {
		t.Fatal("cancelled read", err)
	}
	if _, err = service.ItemFields(context.Background(), domain.Actor{}, id); err != domain.ErrUnauthenticated {
		t.Fatal("invalid actor", err)
	}
	if _, err = service.UpdateItemFields(context.Background(), a, id, 1, nil); err != domain.ErrInvalid {
		t.Fatal("empty patches", err)
	}
	if repo.calls != 1 {
		t.Fatal("invalid call reached repository")
	}
}
