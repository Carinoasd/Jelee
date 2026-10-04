package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Content access administration (G48.1, G48.4). Every operation is
// administrator-only and audited by the repository; the rules take effect
// in the unified storage filter on the next request.

// ContentAccess reads a user's content restrictions and item rules.
func (a *Accounts) ContentAccess(ctx context.Context, actor domain.Actor, id string) (domain.ContentAccessView, error) {
	if !validTarget(actor, id) {
		return domain.ContentAccessView{}, domain.ErrNotFound
	}
	return a.repository.GetContentAccess(ctx, actor, id)
}

// SetContentAccess replaces a user's rating ceiling, unrated override and
// blocked tags.
func (a *Accounts) SetContentAccess(ctx context.Context, actor domain.Actor, id string, in domain.ContentAccess) (domain.ContentAccessView, error) {
	if !validTarget(actor, id) {
		return domain.ContentAccessView{}, domain.ErrNotFound
	}
	if !in.Valid() {
		return domain.ContentAccessView{}, domain.ErrInvalid
	}
	return a.repository.SetContentAccess(ctx, actor, id, in)
}

// SetItemAccessRule creates or replaces a user's explicit rule on an item
// and its descendants.
func (a *Accounts) SetItemAccessRule(ctx context.Context, actor domain.Actor, id, itemID string, effect domain.ItemAccessEffect) (domain.ItemAccessRule, error) {
	if !validTarget(actor, id) || !domain.ValidID(itemID) {
		return domain.ItemAccessRule{}, domain.ErrNotFound
	}
	if !effect.Valid() {
		return domain.ItemAccessRule{}, domain.ErrInvalid
	}
	return a.repository.SetItemAccessRule(ctx, actor, id, itemID, effect)
}

// DeleteItemAccessRule removes a user's explicit rule on an item.
func (a *Accounts) DeleteItemAccessRule(ctx context.Context, actor domain.Actor, id, itemID string) error {
	if !validTarget(actor, id) || !domain.ValidID(itemID) {
		return domain.ErrNotFound
	}
	return a.repository.DeleteItemAccessRule(ctx, actor, id, itemID)
}

// AccessPolicy reads the server-wide content access policy.
func (a *Accounts) AccessPolicy(ctx context.Context, actor domain.Actor) (domain.AccessPolicy, error) {
	if !validActor(actor) {
		return domain.AccessPolicy{}, domain.ErrUnauthenticated
	}
	return a.repository.GetAccessPolicy(ctx, actor)
}

// SetAccessPolicy replaces the server-wide content access policy.
func (a *Accounts) SetAccessPolicy(ctx context.Context, actor domain.Actor, policy domain.AccessPolicy) (domain.AccessPolicy, error) {
	if !validActor(actor) {
		return domain.AccessPolicy{}, domain.ErrUnauthenticated
	}
	return a.repository.SetAccessPolicy(ctx, actor, policy)
}

// ParentalRatings lists the recognized rating codes and their levels.
func (a *Accounts) ParentalRatings(ctx context.Context, actor domain.Actor) ([]domain.ParentalRating, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	return a.repository.ListParentalRatings(ctx, actor)
}
