package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// SidecarTrackRepository is the read side of external subtitle and audio
// files (G10.9, G15.2, G16.2). Both reads recheck the live web or native
// session and the library grant in the same statement as the rows; a missing
// and an invisible source or track are the same ErrNotFound. Writes happen
// only inside a scan transaction and are not part of this port.
type SidecarTrackRepository interface {
	// ListSidecarTracks returns every sidecar row of a visible media source
	// ordered by kind and relative path. A visible source without sidecars
	// yields an empty slice.
	ListSidecarTracks(ctx context.Context, actor domain.Actor, sourceID string) ([]domain.SidecarTrackRecord, error)
	// ResolveSidecarTrack binds one visible track to its file for direct,
	// unmodified delivery.
	ResolveSidecarTrack(ctx context.Context, actor domain.Actor, trackID string) (domain.SidecarTrackLocation, error)
}
