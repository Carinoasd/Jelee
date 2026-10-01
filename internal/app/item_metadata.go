package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type ItemMetadataRepository interface {
	ItemMetadata(context.Context, domain.Actor, string) (domain.ItemMetadata, error)
	UpdateItemMetadata(context.Context, domain.Actor, string, int64, []domain.ItemMetadataPatch) (domain.ItemMetadata, error)
}

func NewLocalMetadata(repository ItemMetadataRepository) (*Metadata, error) {
	if repository == nil {
		return nil, domain.ErrInvalid
	}
	return &Metadata{items: repository}, nil
}

func (m *Metadata) WithItemMetadata(repository ItemMetadataRepository) (*Metadata, error) {
	if m == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	copy := *m
	copy.items = repository
	return &copy, nil
}

func (m *Metadata) HasItemMetadata() bool { return m != nil && m.items != nil }

func (m *Metadata) HasProvider() bool { return m != nil && m.provider != nil }

func (m *Metadata) ItemFields(ctx context.Context, actor domain.Actor, item string) (domain.ItemMetadata, error) {
	if err := ctx.Err(); err != nil {
		return domain.ItemMetadata{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ItemMetadata{}, domain.ErrUnauthenticated
	}
	if !domain.ValidID(item) {
		return domain.ItemMetadata{}, domain.ErrInvalid
	}
	if !m.HasItemMetadata() {
		return domain.ItemMetadata{}, domain.ErrMetadataUnavailable
	}
	value, err := m.items.ItemMetadata(ctx, actor, item)
	return domain.CloneItemMetadata(value), err
}

func (m *Metadata) UpdateItemFields(ctx context.Context, actor domain.Actor, item string, expected int64, patches []domain.ItemMetadataPatch) (domain.ItemMetadata, error) {
	if err := ctx.Err(); err != nil {
		return domain.ItemMetadata{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ItemMetadata{}, domain.ErrUnauthenticated
	}
	if !domain.ValidItemMetadataPatches(item, expected, patches) {
		return domain.ItemMetadata{}, domain.ErrInvalid
	}
	if !m.HasItemMetadata() {
		return domain.ItemMetadata{}, domain.ErrMetadataUnavailable
	}
	owned := append([]domain.ItemMetadataPatch(nil), patches...)
	for i := range owned {
		if owned[i].Value != nil {
			v := *owned[i].Value
			owned[i].Value = &v
		}
		if owned[i].Locked != nil {
			v := *owned[i].Locked
			owned[i].Locked = &v
		}
	}
	value, err := m.items.UpdateItemMetadata(ctx, actor, item, expected, owned)
	return domain.CloneItemMetadata(value), err
}
