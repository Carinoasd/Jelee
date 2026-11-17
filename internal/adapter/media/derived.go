package media

import (
	"context"
	"errors"
	"net/http"
	"path"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// ErrNotDerived reports that an authorized external text subtitle has no
// UTF-8 copy (G15.4): its charset is unknown or already UTF-8, the format
// declares its own encoding, or converting proved the recorded charset
// wrong. The original stays deliverable through ServeTrack.
var ErrNotDerived = errors.New("subtitle_not_derived")

// UTF8Resolver is implemented outside the delivery packages. It applies
// the same authorization as Resolver.ResolveTrack to the track first, then
// makes sure the rebuildable cache holds a UTF-8 copy of the external text
// subtitle and returns that copy as a Source rooted at the cache. The copy
// is a character set conversion of subtitle text into a separate file; the
// original is never opened for writing, and no media is transcoded.
type UTF8Resolver interface {
	ResolveUTF8Track(ctx context.Context, principal access.Principal, sourceID, trackID string) (Source, error)
}

// ServeUTF8Track delivers the UTF-8 copy of one external text subtitle
// through the same production guard, native-only rule, ACL, limits,
// revocation and copy path as ServeTrack. Without a copy (ErrNotDerived)
// it answers like a missing track, or, with fallback, delivers the
// original as ServeTrack would: the compatibility layer always answers
// with the subtitle, the native route only with the copy it names.
func (h *Handler) ServeUTF8Track(w http.ResponseWriter, r *http.Request, resolver UTF8Resolver, sourceID, trackID string, fallback bool) {
	if resolver == nil || trackID == "" {
		sourceID = ""
	}
	timeout := h.options.ExtractTimeout
	if timeout <= 0 {
		timeout = DefaultExtractTimeout
	}
	h.serveWithin(w, r, sourceID, timeout, func(ctx context.Context, principal access.Principal) (Source, error) {
		source, err := resolver.ResolveUTF8Track(ctx, principal, sourceID, trackID)
		switch {
		case err == nil:
			source.ETag, source.Charset = "", "UTF-8"
			source.ContentType = TrackContentType(TrackSubtitle, path.Ext(source.RelativePath), source.Charset, source.SubtitleKind)
			return source, nil
		case errors.Is(err, ErrNotDerived) && fallback:
			source, err = h.resolver.ResolveTrack(ctx, principal, sourceID, TrackSubtitle, trackID)
			if err != nil {
				return Source{}, err
			}
			source.ContentType = TrackContentType(TrackSubtitle, path.Ext(source.RelativePath), source.Charset, source.SubtitleKind)
			return source, nil
		case errors.Is(err, ErrNotDerived):
			return Source{}, ErrNotFound
		}
		return Source{}, err
	})
}
