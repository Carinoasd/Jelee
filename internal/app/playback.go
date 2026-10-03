package app

import (
	"cmp"
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

// PlaybackSources describes every original resource of an item, best
// version first (quality score descending, then source ID).
func (c *Catalog) PlaybackSources(ctx context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSource, error) {
	if c == nil || ctx == nil {
		return nil, domain.ErrInvalid
	}
	if c.playback == nil {
		// Not wired: refuse instead of pretending the item has no sources.
		return nil, domain.ErrDatabase
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(itemID) {
		return nil, domain.ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := c.playback.ListPlaybackSources(ctx, actor, itemID)
	if err != nil {
		return nil, err
	}
	sources := make([]domain.PlaybackSource, 0, len(records))
	for _, record := range records {
		sources = append(sources, domain.BuildPlaybackSource(record))
	}
	slices.SortStableFunc(sources, func(a, b domain.PlaybackSource) int {
		return cmp.Or(cmp.Compare(b.Version.QualityScore, a.Version.QualityScore), cmp.Compare(a.ID, b.ID))
	})
	return sources, nil
}

// CheckPlayback decides direct play for every source of an item against a
// client's declared capabilities. An undecodable source is reported with
// reasons; nothing is converted or offered as a conversion.
func (c *Catalog) CheckPlayback(ctx context.Context, actor domain.Actor, itemID string, caps domain.ClientCapabilities) ([]domain.PlaybackDecision, error) {
	normalized, err := domain.NormalizeClientCapabilities(caps)
	if err != nil {
		return nil, err
	}
	sources, err := c.PlaybackSources(ctx, actor, itemID)
	if err != nil {
		return nil, err
	}
	decisions := make([]domain.PlaybackDecision, 0, len(sources))
	for _, source := range sources {
		decisions = append(decisions, domain.CheckPlayback(normalized, source))
	}
	return decisions, nil
}
