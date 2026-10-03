package postgres

import (
	"context"
	"encoding/json"
	"path"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.PlaybackRepository = (*Store)(nil)

// listPlaybackSourcesSQL binds the live native session, the library grant
// and the item in one statement with the source rows, exactly as Resolve
// does before a stream is opened (G48.2/G48.8). A web session, an invisible
// item and a missing item all produce no visible row.
//
// A probe result is used only while it is ready, unexpired and, for a
// source the catalog scan registered, still stamped with the size and
// modification time the scan last observed. Anything else is reported as
// unprobed instead of describing a file that may have changed.
//
// External tracks are aggregated per source so the bounded probe document
// is read once per source rather than once per sidecar row.
const listPlaybackSourcesSQL = `SELECT m.id IS NOT NULL,COALESCE(m.id::text,''),COALESCE(m.content_type,''),COALESCE(m.relative_path,''),
 CASE WHEN p.root_id IS NOT NULL AND (c.source_id IS NULL OR (c.size=p.size AND c.modified_unix_nano=p.modified_unix_nano)) THEN p.metadata END,
 c.size,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',t.id::text,'kind',t.kind,'format',t.format,'language',t.language,'languages',t.languages,
  'title',t.title,'forced',t.forced,'sdh',t.sdh,'default',t.is_default,'commentary',t.commentary,'charset',t.charset,'size',t.size)
  ORDER BY t.kind DESC,t.root_id,t.relative_path)
  FROM media_sidecar_tracks t WHERE t.source_id=m.id AND t.library_id=m.library_id),'[]'::jsonb)
 FROM items i
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind='native' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 LEFT JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
 LEFT JOIN probe_cache p ON p.root_id=m.root_id AND p.relative_path=m.relative_path AND p.library_id=m.library_id
  AND p.state='ready' AND p.expires_at>clock_timestamp()
 LEFT JOIN catalog_scan_sources c ON c.source_id=m.id AND c.library_id=m.library_id
 WHERE i.id=$3::uuid AND (u.is_admin OR EXISTS(SELECT 1 FROM library_acl a WHERE a.user_id=u.id AND a.library_id=i.library_id))
 ORDER BY m.id`

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
}

// ListPlaybackSources returns every media source of a visible item. Only
// the file name of each source leaves this adapter; roots and directories
// do not.
func (s *Store) ListPlaybackSources(parent context.Context, actor domain.Actor, itemID string) ([]domain.PlaybackSourceRecord, error) {
	ctx, cancel, err := readerContext(parent, actor, itemID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.Pool.Query(ctx, listPlaybackSourcesSQL, actor.UserID, actor.SessionID, itemID)
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
		if err := rows.Scan(&present, &id, &contentType, &relativePath, &metadata, &scanSize, &sidecars); err != nil {
			return nil, storageError(err)
		}
		if !present {
			continue
		}
		record := domain.PlaybackSourceRecord{ID: id, ContentType: contentType, FileName: path.Base(relativePath), ScanSize: scanSize}
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
			record.Sidecars = append(record.Sidecars, domain.SidecarTrackRecord{ID: t.ID, SourceID: id, Track: track, Charset: sidecarText(t.Charset), Size: t.Size})
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
