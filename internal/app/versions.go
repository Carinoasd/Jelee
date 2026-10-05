package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// VersionRepository stores manual version decisions (G20.3, G20.5) and
// track preferences (G16.5, G20.4). Every method binds the caller's live
// session and the unified visibility filter in storage: an item the caller
// may not see is answered like a missing one. Decisions additionally need a
// live administrator.
type VersionRepository interface {
	// SplitVersion moves one version of itemID into a new item; the
	// operation's OtherItemID names it.
	SplitVersion(ctx context.Context, actor domain.Actor, itemID string, in domain.SplitVersionInput) (domain.VersionOperation, error)
	// MergeItems moves every version, the playback history, the user data
	// and the access rules of sourceItemID into targetID and removes the
	// source item.
	MergeItems(ctx context.Context, actor domain.Actor, targetID, sourceItemID string) (domain.VersionOperation, error)
	// SetPrimaryVersion chooses the main version; an empty sourceID clears it.
	SetPrimaryVersion(ctx context.Context, actor domain.Actor, itemID, sourceID string) (domain.VersionOperation, error)
	// RemoveVersionExclusion lets synchronisation group the file into the
	// item again.
	RemoveVersionExclusion(ctx context.Context, actor domain.Actor, itemID, exclusionID string) (domain.VersionOperation, error)
	// UndoVersionOperation reverses one operation.
	UndoVersionOperation(ctx context.Context, actor domain.Actor, operationID string) (domain.VersionOperation, error)
	// VersionOverview lists the main version, exclusions and operations.
	VersionOverview(ctx context.Context, actor domain.Actor, itemID string) (domain.VersionOverview, error)

	// TrackPreferences returns what the user stored for one item.
	TrackPreferences(ctx context.Context, actor domain.Actor, itemID string) (domain.TrackPreferenceView, error)
	// SetTrackPreference replaces the item level (sourceID empty) or one
	// version level; an empty preference removes it.
	SetTrackPreference(ctx context.Context, actor domain.Actor, itemID, sourceID string, p domain.TrackPreference) (domain.TrackPreferenceView, error)
	// UserTrackPreference returns the user's own defaults, nil when unset.
	UserTrackPreference(ctx context.Context, actor domain.Actor) (*domain.TrackPreference, error)
	// SetUserTrackPreference replaces the user's defaults.
	SetUserTrackPreference(ctx context.Context, actor domain.Actor, p domain.TrackPreference) (*domain.TrackPreference, error)
	// EffectiveTrackPreferences reads every level for playback information.
	EffectiveTrackPreferences(ctx context.Context, actor domain.Actor, itemID string) (domain.TrackPreferenceSet, error)
}

// WithVersions enables version decisions and track preferences.
func (c *Catalog) WithVersions(repository VersionRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.versions = repository
	return &next, nil
}

func (c *Catalog) versionsReady(ctx context.Context, actor domain.Actor) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.versions == nil {
		return domain.ErrDatabase
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrUnauthenticated
	}
	return ctx.Err()
}

// SplitVersion validates and applies a split.
func (c *Catalog) SplitVersion(ctx context.Context, actor domain.Actor, itemID string, in domain.SplitVersionInput) (domain.VersionOperation, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOperation{}, err
	}
	if !domain.ValidID(itemID) {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	if !in.Valid() {
		return domain.VersionOperation{}, domain.ErrInvalid
	}
	return c.versions.SplitVersion(ctx, actor, itemID, in)
}

// MergeItems validates and applies a merge. There is no override for the
// G20.5 boundaries: a conflicting identity is fixed in the metadata first.
func (c *Catalog) MergeItems(ctx context.Context, actor domain.Actor, targetID, sourceItemID string) (domain.VersionOperation, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOperation{}, err
	}
	if !domain.ValidID(targetID) {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	if !domain.ValidID(sourceItemID) || sourceItemID == targetID {
		return domain.VersionOperation{}, domain.ErrInvalid
	}
	return c.versions.MergeItems(ctx, actor, targetID, sourceItemID)
}

// SetPrimaryVersion validates and records the main version.
func (c *Catalog) SetPrimaryVersion(ctx context.Context, actor domain.Actor, itemID, sourceID string) (domain.VersionOperation, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOperation{}, err
	}
	if !domain.ValidID(itemID) {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	if sourceID != "" && !domain.ValidID(sourceID) {
		return domain.VersionOperation{}, domain.ErrInvalid
	}
	return c.versions.SetPrimaryVersion(ctx, actor, itemID, sourceID)
}

// RemoveVersionExclusion lifts one exclusion.
func (c *Catalog) RemoveVersionExclusion(ctx context.Context, actor domain.Actor, itemID, exclusionID string) (domain.VersionOperation, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOperation{}, err
	}
	if !domain.ValidID(itemID) || !domain.ValidID(exclusionID) {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	return c.versions.RemoveVersionExclusion(ctx, actor, itemID, exclusionID)
}

// UndoVersionOperation reverses one operation.
func (c *Catalog) UndoVersionOperation(ctx context.Context, actor domain.Actor, operationID string) (domain.VersionOperation, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOperation{}, err
	}
	if !domain.ValidID(operationID) {
		return domain.VersionOperation{}, domain.ErrNotFound
	}
	return c.versions.UndoVersionOperation(ctx, actor, operationID)
}

// VersionOverview reads the administrator view of one item.
func (c *Catalog) VersionOverview(ctx context.Context, actor domain.Actor, itemID string) (domain.VersionOverview, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.VersionOverview{}, err
	}
	if !domain.ValidID(itemID) {
		return domain.VersionOverview{}, domain.ErrNotFound
	}
	return c.versions.VersionOverview(ctx, actor, itemID)
}

// TrackPreferences reads the caller's preferences for one item.
func (c *Catalog) TrackPreferences(ctx context.Context, actor domain.Actor, itemID string) (domain.TrackPreferenceView, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.TrackPreferenceView{}, err
	}
	if !domain.ValidID(itemID) {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	return c.versions.TrackPreferences(ctx, actor, itemID)
}

// SetTrackPreference validates and stores one level of one item.
func (c *Catalog) SetTrackPreference(ctx context.Context, actor domain.Actor, itemID, sourceID string, p domain.TrackPreference) (domain.TrackPreferenceView, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return domain.TrackPreferenceView{}, err
	}
	if !domain.ValidID(itemID) {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	if sourceID != "" && !domain.ValidID(sourceID) {
		return domain.TrackPreferenceView{}, domain.ErrInvalid
	}
	normalized, err := domain.NormalizeTrackPreference(p, sourceID != "")
	if err != nil {
		return domain.TrackPreferenceView{}, err
	}
	return c.versions.SetTrackPreference(ctx, actor, itemID, sourceID, normalized)
}

// UserTrackPreference reads the caller's own defaults.
func (c *Catalog) UserTrackPreference(ctx context.Context, actor domain.Actor) (*domain.TrackPreference, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return nil, err
	}
	return c.versions.UserTrackPreference(ctx, actor)
}

// SetUserTrackPreference validates and stores the caller's own defaults.
func (c *Catalog) SetUserTrackPreference(ctx context.Context, actor domain.Actor, p domain.TrackPreference) (*domain.TrackPreference, error) {
	if err := c.versionsReady(ctx, actor); err != nil {
		return nil, err
	}
	normalized, err := domain.NormalizeTrackPreference(p, false)
	if err != nil {
		return nil, err
	}
	return c.versions.SetUserTrackPreference(ctx, actor, normalized)
}

// applyDefaultTracks fills each source's default tracks from the caller's
// preferences. Without the repository the sources keep none, which leaves
// the client on the files' own defaults.
func (c *Catalog) applyDefaultTracks(ctx context.Context, actor domain.Actor, itemID string, sources []domain.PlaybackSource) error {
	if c.versions == nil || len(sources) == 0 {
		return nil
	}
	set, err := c.versions.EffectiveTrackPreferences(ctx, actor, itemID)
	if err != nil {
		return err
	}
	for i := range sources {
		p, basis := set.Effective(sources[i].ID)
		tracks := domain.SelectDefaultTracks(sources[i], p, basis)
		sources[i].DefaultTracks = &tracks
	}
	return nil
}
