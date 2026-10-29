package app

import (
	"context"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// PlaybackRepository lists the original resources of one item for playback
// information (G10.4, G10.5). The read binds a live native session and the
// library grant in the same statement as the source rows, so a missing item,
// an invisible item and a web session are the same ErrNotFound.
type PlaybackRepository interface {
	// ListPlaybackSources returns every media source of a visible item with
	// its current probe metadata, if any, and its external tracks. A visible
	// item without sources yields an empty slice.
	ListPlaybackSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error)
}

// WithPlayback enables playback information on a catalog.
func (c *Catalog) WithPlayback(repository PlaybackRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.playback = repository
	return &next, nil
}

// PlaybackSources describes every original resource of an item in the
// caller's default version order (G20.2: the user's version preference,
// then the main version, the quality score and the source ID), with the
// default tracks the caller's preferences and session choices pick.
func (c *Catalog) PlaybackSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSource, error) {
	sources, _, err := c.PlaybackChoice(ctx, actor, itemID, nil)
	return sources, err
}

// PlaybackChoice is PlaybackSources with the default version: exclude
// returns the direct play reason codes of a version the client declared it
// cannot play as it is (nil keeps every version). Excluded versions stay in
// the list in their place; the selection names the default and the rule
// that chose it. A declaration only chooses among originals.
func (c *Catalog) PlaybackChoice(ctx context.Context, actor domain.Actor, itemID string, exclude func(domain.PlaybackSource) []string) ([]domain.PlaybackSource, domain.VersionSelection, error) {
	if c == nil || ctx == nil {
		return nil, domain.VersionSelection{}, domain.ErrInvalid
	}
	if c.playback == nil {
		// Not wired: refuse instead of pretending the item has no sources.
		return nil, domain.VersionSelection{}, domain.ErrDatabase
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(itemID) {
		return nil, domain.VersionSelection{}, domain.ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, domain.VersionSelection{}, err
	}
	records, err := c.playback.ListPlaybackSources(ctx, actor, itemID)
	if err != nil {
		return nil, domain.VersionSelection{}, err
	}
	sources := describeSources(records)
	preference, err := c.applyDefaultTracks(ctx, actor, itemID, sources)
	if err != nil {
		return nil, domain.VersionSelection{}, err
	}
	selection := domain.SelectVersion(sources, preference, exclude)
	return sources, selection, nil
}

// describeSources projects storage records into versions, with the later
// parts of a multi-part version folded into its first part (G21.1): the
// administrator's main version first, then the best version.
func describeSources(records []domain.PlaybackSourceRecord) []domain.PlaybackSource {
	sources := domain.BuildPlaybackSources(records)
	slices.SortStableFunc(sources, domain.CompareVersions)
	return sources
}

// CheckPlayback decides direct play for every source of an item against a
// client's declared capabilities. An undecodable source is reported with
// reasons; nothing is converted or offered as a conversion.
func (c *Catalog) CheckPlayback(ctx context.Context, actor domain.Actor, itemID string, caps domain.ClientCapabilities) ([]domain.PlaybackDecision, error) {
	decisions, _, err := c.CheckPlaybackChoice(ctx, actor, itemID, caps)
	return decisions, err
}

// CheckPlaybackChoice is CheckPlayback with the default version among the
// directly playable ones (G20.2).
func (c *Catalog) CheckPlaybackChoice(ctx context.Context, actor domain.Actor, itemID string, caps domain.ClientCapabilities) ([]domain.PlaybackDecision, domain.VersionSelection, error) {
	normalized, err := domain.NormalizeClientCapabilities(caps)
	if err != nil {
		return nil, domain.VersionSelection{}, err
	}
	sources, selection, err := c.PlaybackChoice(ctx, actor, itemID, domain.DirectPlayExclusion(normalized))
	if err != nil {
		return nil, domain.VersionSelection{}, err
	}
	decisions := make([]domain.PlaybackDecision, 0, len(sources))
	for _, source := range sources {
		decisions = append(decisions, domain.CheckPlayback(normalized, source))
	}
	return decisions, selection, nil
}
