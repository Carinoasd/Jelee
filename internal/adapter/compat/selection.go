package compat

import (
	"context"
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Default version choice for PlaybackInfo (G20.2). The catalog lists the
// sources in the user's default version order; the request's declaration
// (EnableDirectPlay, MaxStreamingBitrate, the DeviceProfile's direct play
// profiles, a named MediaSourceId) only removes sources, so the first
// listed MediaSource is the default version. A declaration never makes a
// conversion acceptable: a source the client cannot play as it is is left
// out, and with none left the answer stays NoCompatibleStream.

// Exclusion codes of PlaybackInfo. They only appear in the debug log.
const (
	exclusionDirectPlayOff = "direct_play_disabled"
	exclusionNotRequested  = "media_source_not_requested"
	exclusionProfile       = "device_profile_unsupported"
)

// playbackChooser is implemented by the server's catalog; a catalog without
// it (tests) lists sources without a selection.
type playbackChooser interface {
	PlaybackChoice(ctx context.Context, actor domain.Actor, itemID string, exclude func(domain.PlaybackSource) []string) ([]domain.PlaybackSource, domain.VersionSelection, error)
}

// playbackChoice lists the item's sources in default version order and logs
// why the default was chosen.
func (rt *router) playbackChoice(r *http.Request, principal access.Principal, itemID string, req playbackRequest) ([]domain.PlaybackSource, error) {
	catalog := rt.opts.Library.Catalog
	chooser, ok := catalog.(playbackChooser)
	if !ok {
		return catalog.PlaybackSources(r.Context(), rt.playbackActor(r, principal), itemID)
	}
	sources, selection, err := chooser.PlaybackChoice(r.Context(), rt.playbackActor(r, principal), itemID, func(s domain.PlaybackSource) []string {
		return rt.playbackExclusion(req, s)
	})
	if err != nil {
		return nil, err
	}
	if logger := rt.opts.Library.Logger; logger != nil {
		// Only source IDs and fixed codes, never names or paths.
		logger.DebugContext(r.Context(), "default version chosen", "component", "compat", "sourceId", selection.DefaultSourceID,
			"reason", selection.Reason, "preferences", selection.Preferences, "excluded", len(selection.Excluded))
	}
	return sources, nil
}

// playbackExclusion returns why the request cannot use source as it is; nil
// when it can (directPlayable).
func (rt *router) playbackExclusion(req playbackRequest, source domain.PlaybackSource) []string {
	switch {
	case req.mediaSourceID != "" && source.ID != req.mediaSourceID && !hasPart(source, req.mediaSourceID):
		return []string{exclusionNotRequested}
	case !rt.opts.Library.DirectPlay || !req.directPlay:
		return []string{exclusionDirectPlayOff}
	case rt.directPlayable(req, source):
		return nil
	case req.maxBitrate > 0 && source.BitRate != nil && *source.BitRate > req.maxBitrate:
		return []string{domain.PlaybackReasonBitrate}
	}
	return []string{exclusionProfile}
}

// hasPart reports a multi-part version one of whose parts is id (G21.1).
func hasPart(source domain.PlaybackSource, id string) bool {
	for _, part := range source.Parts {
		if part.ID == id {
			return true
		}
	}
	return false
}
