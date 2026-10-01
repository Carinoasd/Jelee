package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Both operations recheck the live administrator session in the transaction.
type MetadataPreferencesRepository interface {
	MetadataPreferences(context.Context, domain.Actor, string) (domain.MetadataPreferences, error)
	UpdateMetadataPreferences(context.Context, domain.Actor, string, string, int64) (domain.MetadataPreferences, error)
}

// WithLibraryPreferences returns a new service sharing the owned provider.
// Runtime constructs it before accepting requests; no mutable setter is used.
func (m *Metadata) WithLibraryPreferences(repository MetadataPreferencesRepository) (*Metadata, error) {
	if m == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	return &Metadata{provider: m.provider, preferences: repository}, nil
}

func (m *Metadata) LibraryPreferences(ctx context.Context, actor domain.Actor, library string) (domain.MetadataPreferences, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataPreferences{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.MetadataPreferences{}, domain.ErrUnauthenticated
	}
	if !domain.ValidID(library) {
		return domain.MetadataPreferences{}, domain.ErrInvalid
	}
	if m.preferences == nil {
		return domain.MetadataPreferences{}, domain.ErrMetadataUnavailable
	}
	return m.preferences.MetadataPreferences(ctx, actor, library)
}

func (m *Metadata) UpdateLibraryPreferences(ctx context.Context, actor domain.Actor, library, language string, expected int64) (domain.MetadataPreferences, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataPreferences{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.MetadataPreferences{}, domain.ErrUnauthenticated
	}
	if !domain.ValidMetadataPreferenceUpdate(library, language, expected) {
		return domain.MetadataPreferences{}, domain.ErrInvalid
	}
	if m.preferences == nil {
		return domain.MetadataPreferences{}, domain.ErrMetadataUnavailable
	}
	return m.preferences.UpdateMetadataPreferences(ctx, actor, library, language, expected)
}
