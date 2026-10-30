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

// SidecarProbeRepository pages, under a catalog sync lease, the external
// tracks whose sandboxed probe is pending (B4) and records the outcomes. A
// result applies only while the row's size and modification time still
// equal the stamp that was probed; a changed file stays pending.
type SidecarProbeRepository interface {
	NextSidecarProbes(ctx context.Context, lease domain.JobLease, after string, limit int) ([]domain.SidecarInspectionTarget, error)
	RecordSidecarProbes(ctx context.Context, lease domain.JobLease, results []domain.SidecarProbeResult) (int, error)
}

// SidecarProber probes one external audio or subtitle file with the
// sandboxed metadata runtime, under the same process admission as media
// probing. Errors are the MetadataProber sentinels.
type SidecarProber interface {
	ProbeSidecar(ctx context.Context, source domain.ProbeSource) (domain.ProbeObservation, error)
}

// SourceLayoutRepository pages, under a catalog sync lease, the media
// sources whose container layout was not read for the scan's current stamp
// of the file (or by the current reader version), and records what a
// bounded read found (G10.6). A result applies only while the scan's stamp
// still equals the one that was read.
type SourceLayoutRepository interface {
	NextSourceLayoutInspections(ctx context.Context, lease domain.JobLease, after string, limit int) ([]domain.SourceLayoutTarget, error)
	RecordSourceLayouts(ctx context.Context, lease domain.JobLease, results []domain.SourceLayoutInspection) (int, error)
}

// SourceLayoutInspector reads a bounded part of one media source without
// changing it and classifies where it keeps its start and seek index. A
// file that is gone, replaced or changed since the scan fails.
type SourceLayoutInspector interface {
	InspectSourceLayout(ctx context.Context, target domain.SourceLayoutTarget) (domain.SourceLayoutInspection, error)
}
