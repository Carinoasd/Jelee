package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogIdentityRepository corrects and locks item identification (G21.5)
// and decides pending files. Every call rechecks the live administrator.
type CatalogIdentityRepository interface {
	ItemIdentity(context.Context, domain.Actor, string) (domain.ItemIdentity, error)
	UpdateItemIdentity(context.Context, domain.Actor, string, int64, []domain.ItemIdentityPatch) (domain.ItemIdentity, error)
	AcceptCatalogPending(context.Context, domain.Actor, string, string, domain.CatalogPendingDecision) (domain.CatalogPendingAcceptance, error)
	RejectCatalogPending(context.Context, domain.Actor, string, string) (domain.CatalogPendingEntry, error)
}

func (j *Jobs) catalogIdentity() (CatalogIdentityRepository, error) {
	if j == nil {
		return nil, domain.ErrMetadataUnavailable
	}
	repository, ok := j.repository.(CatalogIdentityRepository)
	if !ok {
		return nil, domain.ErrMetadataUnavailable
	}
	return repository, nil
}

// ItemIdentity reads an item's effective identification and its locks.
func (j *Jobs) ItemIdentity(ctx context.Context, actor domain.Actor, item string) (domain.ItemIdentity, error) {
	if ctx == nil || !validTarget(actor, item) {
		return domain.ItemIdentity{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.ItemIdentity{}, err
	}
	repository, err := j.catalogIdentity()
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	value, err := repository.ItemIdentity(ctx, actor, item)
	return domain.CloneItemIdentity(value), err
}

// UpdateItemIdentity applies one revision-checked correction. The caller's
// slice and pointers are copied before they reach the repository.
func (j *Jobs) UpdateItemIdentity(ctx context.Context, actor domain.Actor, item string, expected int64, patches []domain.ItemIdentityPatch) (domain.ItemIdentity, error) {
	if ctx == nil || !validTarget(actor, item) || !domain.ValidItemIdentityPatches(item, expected, patches) {
		return domain.ItemIdentity{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.ItemIdentity{}, err
	}
	repository, err := j.catalogIdentity()
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	owned := append([]domain.ItemIdentityPatch(nil), patches...)
	for i := range owned {
		if owned[i].Locked != nil {
			v := *owned[i].Locked
			owned[i].Locked = &v
		}
	}
	value, err := repository.UpdateItemIdentity(ctx, actor, item, expected, owned)
	return domain.CloneItemIdentity(value), err
}

// AcceptCatalogPending registers a pending file with the administrator's
// values; the decision is copied before it reaches the repository.
func (j *Jobs) AcceptCatalogPending(ctx context.Context, actor domain.Actor, library, entry string, d domain.CatalogPendingDecision) (domain.CatalogPendingAcceptance, error) {
	if ctx == nil || !validTarget(actor, library) || !domain.ValidID(entry) || !domain.ValidCatalogPendingDecision(d) {
		return domain.CatalogPendingAcceptance{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	repository, err := j.catalogIdentity()
	if err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	return repository.AcceptCatalogPending(ctx, actor, library, entry, cloneDecision(d))
}

// RejectCatalogPending keeps a pending file out of the catalog until it changes.
func (j *Jobs) RejectCatalogPending(ctx context.Context, actor domain.Actor, library, entry string) (domain.CatalogPendingEntry, error) {
	if ctx == nil || !validTarget(actor, library) || !domain.ValidID(entry) {
		return domain.CatalogPendingEntry{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.CatalogPendingEntry{}, err
	}
	repository, err := j.catalogIdentity()
	if err != nil {
		return domain.CatalogPendingEntry{}, err
	}
	return repository.RejectCatalogPending(ctx, actor, library, entry)
}

func cloneDecision(d domain.CatalogPendingDecision) domain.CatalogPendingDecision {
	text := func(v *string) *string {
		if v == nil {
			return nil
		}
		c := *v
		return &c
	}
	number := func(v *int) *int {
		if v == nil {
			return nil
		}
		c := *v
		return &c
	}
	d.Title, d.SeriesTitle = text(d.Title), text(d.SeriesTitle)
	d.Year, d.Season, d.Episode, d.EpisodeEnd = number(d.Year), number(d.Season), number(d.Episode), number(d.EpisodeEnd)
	return d
}
