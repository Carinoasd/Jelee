package postgres

import (
	"context"
	"encoding/json"
	"path"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	_ app.PlaybackRepository       = (*Store)(nil)
	_ app.CatalogDetailsRepository = (*Store)(nil)
)

// listSourcesSQL binds the live session (narrowed by sessionFilter), the
// library grant and the item in one statement with the source rows, exactly
// as Resolve does before a stream is opened (G48.2/G48.8). An invisible item and a
// missing item both produce no visible row.
//
// A probe result is used only while it is ready, unexpired and, for a
// source the catalog scan registered, still stamped with the size and
// modification time the scan last observed. Anything else is reported as
// unprobed instead of describing a file that may have changed.
//
// External tracks are aggregated per source so the bounded probe document
// is read once per source rather than once per sidecar row. Every part of a
// multi-part version is listed with its version and number (G21.1); the
// application folds them. A container layout (G10.6) is only reported while
// it was read against the scan's current stamp of the file.
func listSourcesSQL(sessionFilter string) string {
	return `SELECT m.id IS NOT NULL,COALESCE(m.id::text,''),COALESCE(m.content_type,''),COALESCE(m.relative_path,''),
 CASE WHEN p.root_id IS NOT NULL AND (c.source_id IS NULL OR (c.size=p.size AND c.modified_unix_nano=p.modified_unix_nano)) THEN p.metadata END,
 c.size,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',t.id::text,'kind',t.kind,'format',t.format,'language',t.language,'languages',t.languages,
  'title',t.title,'forced',t.forced,'sdh',t.sdh,'default',t.is_default,'commentary',t.commentary,'charset',t.charset,'size',t.size,
  'probeState',t.probe_state,'codec',t.codec,'profile',t.profile,'channels',t.channels,'channelLayout',t.channel_layout,
  'sampleRate',t.sample_rate,'bitRate',t.bit_rate,'durationMicros',t.duration_micros,'subtitleKind',t.subtitle_kind,'pairedTrackId',t.paired_track_id::text)
  ORDER BY t.kind DESC,t.root_id,t.relative_path)
  FROM media_sidecar_tracks t WHERE t.source_id=m.id AND t.library_id=m.library_id),'[]'::jsonb),
 EXISTS(SELECT 1 FROM item_primary_versions pv WHERE pv.item_id=i.id AND pv.source_id=m.id),
 COALESCE(pp.version_source_id::text,''),COALESCE(pp.part_number,0),
 ml.layout,COALESCE(ml.index_offset,0),COALESCE(ml.index_length,0)
 FROM items i
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id` + sessionFilter + ` AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 LEFT JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
 LEFT JOIN probe_cache p ON p.root_id=m.root_id AND p.relative_path=m.relative_path AND p.library_id=m.library_id
  AND p.state='ready' AND p.expires_at>clock_timestamp()
 LEFT JOIN catalog_scan_sources c ON c.source_id=m.id AND c.library_id=m.library_id
 LEFT JOIN media_source_parts pp ON pp.source_id=m.id
 LEFT JOIN media_source_layouts ml ON ml.source_id=m.id AND ml.size=c.size AND ml.modified_unix_nano=c.modified_unix_nano
 WHERE i.id=$3::uuid AND ` + itemVisibleSQL("$4", "i.library_id", "i.id") + `
 ORDER BY m.id`
}

// listPlaybackSourcesSQL is the playback read: native sessions only, so a
// web session sees no row.
var listPlaybackSourcesSQL = listSourcesSQL(` AND s.client_kind='native'`)

// listItemSourcesSQL is the file information read (G34.3): any live session
// of the user. It returns the same rows and never authorizes delivery.
var listItemSourcesSQL = listSourcesSQL(``)

type playbackSidecar struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Format     string   `json:"format"`
	Language   *string  `json:"language"`
	Languages  []string `json:"languages"`
	Title      *string  `json:"title"`
	Forced     bool     `json:"forced"`
	SDH        bool     `json:"sdh"`
	Default    bool     `json:"default"`
	Commentary bool     `json:"commentary"`
	Charset    *string  `json:"charset"`
	Size       int64    `json:"size"`
	ProbeState string   `json:"probeState"`
	Codec      *string  `json:"codec"`
	Profile    *string  `json:"profile"`
	Channels   *int     `json:"channels"`
	Layout     *string  `json:"channelLayout"`
	SampleRate *int     `json:"sampleRate"`
	BitRate    *int64   `json:"bitRate"`
	Duration   *int64   `json:"durationMicros"`
	SubKind    *string  `json:"subtitleKind"`
	Paired     *string  `json:"pairedTrackId"`
}

// ListPlaybackSources returns every media source of a visible item for a
// native session. Only the file name of each source leaves this adapter;
// roots and directories do not.
func (s *Store) ListPlaybackSources(parent context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	return s.listSources(parent, listPlaybackSourcesSQL, actor, itemID)
}

// ListItemSources returns the same records as ListPlaybackSources for any
// live session kind.
func (s *Store) ListItemSources(parent context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	return s.listSources(parent, listItemSourcesSQL, actor, itemID)
}

func (s *Store) listSources(parent context.Context, query string, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	ctx, cancel, err := readerContext(parent, actor, itemID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.Pool.Query(ctx, query, actor.UserID, actor.SessionID, itemID, requestScopeArg(ctx))
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	visible := false
	result := make([]domain.PlaybackSourceRecord, 0)
	for rows.Next() {
		visible = true
		var present bool
		var id, contentType, relativePath string
		var metadata, sidecars []byte
		var scanSize *int64
		var primary bool
		var partOf string
		var partNumber int
		var layout *string
		var indexOffset, indexLength int64
		if err := rows.Scan(&present, &id, &contentType, &relativePath, &metadata, &scanSize, &sidecars, &primary, &partOf, &partNumber, &layout, &indexOffset, &indexLength); err != nil {
			return nil, storageError(err)
		}
		if !present {
			continue
		}
		record := domain.PlaybackSourceRecord{ID: id, ContentType: contentType, FileName: path.Base(relativePath), ScanSize: scanSize, Primary: primary,
			PartOf: partOf, PartNumber: partNumber}
		if layout != nil {
			record.Layout = &domain.SourceLayout{Layout: domain.ContainerLayout(*layout), IndexOffset: indexOffset, IndexLength: indexLength}
		}
		if len(metadata) > 0 {
			var meta domain.MediaMetadata
			// The cache only stores validated documents; one that no longer
			// passes the whitelist is treated as unprobed, not served.
			if json.Unmarshal(metadata, &meta) == nil {
				if _, err := domain.MarshalProbeMetadata(meta); err == nil {
					record.Metadata = &meta
				}
			}
		}
		var tracks []playbackSidecar
		if err := json.Unmarshal(sidecars, &tracks); err != nil {
			return nil, domain.ErrDatabase
		}
		for _, t := range tracks {
			track := domain.SidecarTrack{Kind: t.Kind, Format: t.Format, Language: sidecarText(t.Language), Title: sidecarText(t.Title),
				Forced: t.Forced, SDH: t.SDH, Default: t.Default, Commentary: t.Commentary}
			if len(t.Languages) > 0 {
				track.Languages = t.Languages
			}
			probe := sidecarProbeRecord(t.ProbeState, t.Codec, t.Profile, t.Channels, t.Layout, t.SampleRate, t.BitRate, t.Duration)
			record.Sidecars = append(record.Sidecars, domain.SidecarTrackRecord{ID: t.ID, SourceID: id, Track: track, Charset: sidecarText(t.Charset), Size: t.Size,
				Probe: probe, SubtitleKind: sidecarText(t.SubKind), PairedTrackID: sidecarText(t.Paired)})
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !visible {
		return nil, domain.ErrNotFound
	}
	return result, nil
}
