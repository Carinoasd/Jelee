package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Access administration beyond single rules (G48.4, G48.7): restricted time
// windows, the rating code table, the user × library grant matrix, bulk
// grant changes, templates and change previews. Every operation is
// administrator-only; the repository audits applied changes, and a preview
// writes nothing.

// SetAccessWindows replaces a user's restricted time windows.
func (a *Accounts) SetAccessWindows(ctx context.Context, actor domain.Actor, id string, windows []domain.AccessWindow) (domain.ContentAccessView, error) {
	if !validTarget(actor, id) {
		return domain.ContentAccessView{}, domain.ErrNotFound
	}
	if !domain.ValidAccessWindows(windows) {
		return domain.ContentAccessView{}, domain.ErrInvalid
	}
	return a.repository.SetAccessWindows(ctx, actor, id, windows)
}

// ReplaceParentalRatings replaces the recognized rating codes.
func (a *Accounts) ReplaceParentalRatings(ctx context.Context, actor domain.Actor, ratings []domain.ParentalRating) ([]domain.ParentalRating, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	if !domain.ValidParentalRatings(ratings) {
		return nil, domain.ErrInvalid
	}
	return a.repository.ReplaceParentalRatings(ctx, actor, ratings)
}

// AccessGrantMatrix lists users with their library grants and every
// library.
func (a *Accounts) AccessGrantMatrix(ctx context.Context, actor domain.Actor) (domain.AccessGrantMatrix, error) {
	if !validActor(actor) {
		return domain.AccessGrantMatrix{}, domain.ErrUnauthenticated
	}
	return a.repository.GetAccessGrantMatrix(ctx, actor)
}

// ApplyAccessGrants adds and removes library grants of many users, or with
// preview only reports what that would change.
func (a *Accounts) ApplyAccessGrants(ctx context.Context, actor domain.Actor, ops []domain.AccessGrantOperation, preview bool) (domain.AccessChangePreview, error) {
	if !validActor(actor) {
		return domain.AccessChangePreview{}, domain.ErrUnauthenticated
	}
	if !domain.ValidAccessGrantOperations(ops) {
		return domain.AccessChangePreview{}, domain.ErrInvalid
	}
	return a.repository.ApplyAccessGrants(ctx, actor, ops, preview)
}

// AccessTemplates lists the access templates.
func (a *Accounts) AccessTemplates(ctx context.Context, actor domain.Actor) ([]domain.AccessTemplate, error) {
	if !validActor(actor) {
		return nil, domain.ErrUnauthenticated
	}
	return a.repository.ListAccessTemplates(ctx, actor)
}

// CreateAccessTemplate stores a new template.
func (a *Accounts) CreateAccessTemplate(ctx context.Context, actor domain.Actor, in domain.AccessTemplateInput) (domain.AccessTemplate, error) {
	if !validActor(actor) {
		return domain.AccessTemplate{}, domain.ErrUnauthenticated
	}
	if !in.Valid() {
		return domain.AccessTemplate{}, domain.ErrInvalid
	}
	return a.repository.CreateAccessTemplate(ctx, actor, in)
}

// UpdateAccessTemplate replaces a template.
func (a *Accounts) UpdateAccessTemplate(ctx context.Context, actor domain.Actor, id string, in domain.AccessTemplateInput) (domain.AccessTemplate, error) {
	if !validActor(actor) {
		return domain.AccessTemplate{}, domain.ErrUnauthenticated
	}
	if !domain.ValidID(id) {
		return domain.AccessTemplate{}, domain.ErrNotFound
	}
	if !in.Valid() {
		return domain.AccessTemplate{}, domain.ErrInvalid
	}
	return a.repository.UpdateAccessTemplate(ctx, actor, id, in)
}

// DeleteAccessTemplate removes a template.
func (a *Accounts) DeleteAccessTemplate(ctx context.Context, actor domain.Actor, id string) error {
	if !validActor(actor) {
		return domain.ErrUnauthenticated
	}
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	return a.repository.DeleteAccessTemplate(ctx, actor, id)
}

// ApplyAccessTemplate gives users a template's libraries and restrictions,
// or with preview only reports what that would change.
func (a *Accounts) ApplyAccessTemplate(ctx context.Context, actor domain.Actor, id string, userIDs []string, preview bool) (domain.AccessChangePreview, error) {
	if !validActor(actor) {
		return domain.AccessChangePreview{}, domain.ErrUnauthenticated
	}
	if !domain.ValidID(id) {
		return domain.AccessChangePreview{}, domain.ErrNotFound
	}
	if !domain.ValidAccessUserIDs(userIDs) {
		return domain.AccessChangePreview{}, domain.ErrInvalid
	}
	return a.repository.ApplyAccessTemplate(ctx, actor, id, userIDs, preview)
}
