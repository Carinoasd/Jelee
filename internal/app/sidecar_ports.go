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

// SidecarInspectionRepository pages, under a catalog sync lease, the tracks
// whose charset and edge fingerprint are still unknown, and records what a
// bounded read found. A result applies only while the row's size and
// modification time still equal the ones that were read.
type SidecarInspectionRepository interface {
	NextSidecarInspections(ctx context.Context, lease domain.JobLease, after string, limit int) ([]domain.SidecarInspectionTarget, error)
	RecordSidecarInspections(ctx context.Context, lease domain.JobLease, results []domain.SidecarInspection) (int, error)
}

// SidecarInspector reads a bounded part of one sidecar file without
// changing it: an edge fingerprint and, for a text subtitle, the detected
// charset. A file that is gone, replaced or changed since the scan fails.
type SidecarInspector interface {
	InspectSidecar(ctx context.Context, target domain.SidecarInspectionTarget) (domain.SidecarInspection, error)
}
