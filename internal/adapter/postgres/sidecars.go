package postgres

import (
	"bytes"
	"context"
	"slices"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.SidecarTrackRepository = (*Store)(nil)

const sidecarColumns = `t.id::text,t.source_id::text,t.library_id::text,t.root_id::text,t.relative_path,t.kind,t.format,
 t.language,t.languages,t.title,t.forced,t.sdh,t.is_default,t.commentary,t.charset,t.size,t.modified_unix_nano,t.fingerprint,
 t.probe_state,t.codec,t.profile,t.channels,t.channel_layout,t.sample_rate,t.bit_rate,t.duration_micros,t.subtitle_kind,t.paired_track_id::text,
 t.created_at,t.updated_at`

// Reads bind the live web or native session and the library grant in the
// same statement as the sidecar rows (G48.2/G48.8), as media and image reads
// do. An invisible source or track and a missing one are indistinguishable.
const sidecarVisibleActor = `JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.client_kind IN ('web','native')
  AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`

// sidecarRow holds nullable scan targets so the LEFT JOIN listing and the
// transaction reads share one decoder.
type sidecarRow struct {
	id, sourceID, libraryID, rootID, relativePath, kind, format *string
	language, title, charset                                    *string
	languages                                                   []string
	forced, sdh, isDefault, commentary                          *bool
	size, mtime                                                 *int64
	fingerprint                                                 []byte
	probeState, codec, profile, layout, subtitleKind, paired    *string
	channels, sampleRate                                        *int
	bitRate, duration                                           *int64
	created, updated                                            *time.Time
}

func (r *sidecarRow) targets() []any {
	return []any{&r.id, &r.sourceID, &r.libraryID, &r.rootID, &r.relativePath, &r.kind, &r.format, &r.language, &r.languages,
		&r.title, &r.forced, &r.sdh, &r.isDefault, &r.commentary, &r.charset, &r.size, &r.mtime, &r.fingerprint,
		&r.probeState, &r.codec, &r.profile, &r.channels, &r.layout, &r.sampleRate, &r.bitRate, &r.duration, &r.subtitleKind, &r.paired,
		&r.created, &r.updated}
}

func (r *sidecarRow) value() (domain.SidecarTrackRecord, error) {
	if r.id == nil || r.sourceID == nil || r.libraryID == nil || r.rootID == nil || r.relativePath == nil || r.kind == nil ||
		r.format == nil || r.forced == nil || r.sdh == nil || r.isDefault == nil || r.commentary == nil || r.size == nil ||
		r.mtime == nil || r.probeState == nil || r.created == nil || r.updated == nil {
		return domain.SidecarTrackRecord{}, domain.ErrDatabase
	}
	track := domain.SidecarTrack{Kind: *r.kind, Format: *r.format, Forced: *r.forced, SDH: *r.sdh, Default: *r.isDefault,
		Commentary: *r.commentary, Language: sidecarText(r.language), Title: sidecarText(r.title)}
	if len(r.languages) > 0 {
		track.Languages = r.languages
	}
	return domain.SidecarTrackRecord{ID: *r.id, SourceID: *r.sourceID, LibraryID: *r.libraryID, RootID: *r.rootID,
		RelativePath: *r.relativePath, Track: track, Charset: sidecarText(r.charset), Size: *r.size, ModifiedUnixNano: *r.mtime,
		Fingerprint: r.fingerprint, Probe: sidecarProbeRecord(*r.probeState, r.codec, r.profile, r.channels, r.layout, r.sampleRate, r.bitRate, r.duration),
		SubtitleKind: sidecarText(r.subtitleKind), PairedTrackID: sidecarText(r.paired), CreatedAt: r.created.UTC(), UpdatedAt: r.updated.UTC()}, nil
}

// sidecarProbeRecord assembles the stored probe columns; NULL is unknown.
func sidecarProbeRecord(state string, codec, profile *string, channels *int, layout *string, sampleRate *int, bitRate, duration *int64) domain.SidecarProbeRecord {
	value := domain.SidecarProbeRecord{State: state, Facts: domain.SidecarProbeFacts{Codec: sidecarText(codec), Profile: sidecarText(profile),
		ChannelLayout: sidecarText(layout)}}
	if channels != nil {
		value.Facts.Channels = *channels
	}
	if sampleRate != nil {
		value.Facts.SampleRate = *sampleRate
	}
	if bitRate != nil {
		value.Facts.BitRate = *bitRate
	}
	if duration != nil {
		value.Facts.DurationMicros = *duration
	}
	return value
}

func sidecarText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ListSidecarTracks returns the sidecar rows of a visible media source. A
// visible source without sidecars yields an empty slice; an invisible or
// missing source yields ErrNotFound.
func (s *Store) ListSidecarTracks(parent context.Context, actor domain.Actor, sourceID string) ([]domain.SidecarTrackRecord, error) {
	ctx, cancel, err := readerContext(parent, actor, sourceID)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT m.id IS NOT NULL,t.id IS NOT NULL,`+sidecarColumns+` FROM media_sources m
 `+sidecarVisibleActor+`
 LEFT JOIN media_sidecar_tracks t ON t.source_id=m.id AND t.library_id=m.library_id
 WHERE m.id=$3::uuid AND `+itemVisibleSQL("$4", "m.library_id", "m.item_id")+`
 ORDER BY t.kind DESC,t.root_id,t.relative_path`, actor.UserID, actor.SessionID, sourceID, requestScopeArg(ctx))
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	visible := false
	result := make([]domain.SidecarTrackRecord, 0)
	for rows.Next() {
		var found, present bool
		var r sidecarRow
		if err := rows.Scan(append([]any{&found, &present}, r.targets()...)...); err != nil {
			return nil, storageError(err)
		}
		visible = visible || found
		if !present {
			continue
		}
		value, err := r.value()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
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

// ResolveSidecarTrack binds one visible track to its root and relative path
// for direct delivery. The track's own source must still exist, so a row is
// never served through a source the reader could not see.
func (s *Store) ResolveSidecarTrack(parent context.Context, actor domain.Actor, trackID string) (domain.SidecarTrackLocation, error) {
	ctx, cancel, err := readerContext(parent, actor, trackID)
	if err != nil {
		return domain.SidecarTrackLocation{}, err
	}
	defer cancel()
	var value domain.SidecarTrackLocation
	err = s.Pool.QueryRow(ctx, `SELECT t.id::text,t.source_id::text,t.kind,t.format,r.path,t.relative_path,COALESCE(t.charset,''),COALESCE(t.subtitle_kind,''),t.size,t.modified_unix_nano
 FROM media_sidecar_tracks t
 JOIN media_sources m ON m.id=t.source_id AND m.library_id=t.library_id
 JOIN library_roots r ON r.id=t.root_id AND r.library_id=t.library_id
 `+sidecarVisibleActor+`
 WHERE t.id=$3::uuid AND `+itemVisibleSQL("$4", "m.library_id", "m.item_id"),
		actor.UserID, actor.SessionID, trackID, requestScopeArg(ctx)).Scan(&value.ID, &value.SourceID, &value.Kind, &value.Format, &value.RootPath,
		&value.RelativePath, &value.Charset, &value.SubtitleKind, &value.Size, &value.ModifiedUnixNano)
	if err != nil {
		return domain.SidecarTrackLocation{}, storageError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.SidecarTrackLocation{}, err
	}
	return value, nil
}

type sidecarKey struct{ root, path string }

func sameSidecarTrack(old domain.SidecarTrackRecord, in domain.SidecarTrackInput) bool {
	a, b := old.Track, in.Track
	return a.Kind == b.Kind && a.Format == b.Format && a.Language == b.Language && slices.Equal(a.Languages, b.Languages) &&
		a.Title == b.Title && a.Forced == b.Forced && a.SDH == b.SDH && a.Default == b.Default && a.Commentary == b.Commentary &&
		old.Charset == in.Charset && old.Size == in.Size && old.ModifiedUnixNano == in.ModifiedUnixNano &&
		bytes.Equal(old.Fingerprint, in.Fingerprint)
}

type sidecarAudit struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Removed   int `json:"removed"`
	Subtitles int `json:"subtitles"`
	Audio     int `json:"audio"`
}

// UpsertSidecarTracks replaces the sidecar set of one media source with
// tracks inside the caller's transaction: new files are added, changed rows
// updated and files no longer observed removed. An unchanged set writes
// nothing. The work runs under a savepoint, so a rejected set leaves the
// caller's transaction usable and the stored set untouched.
//
// Every root must belong to the source's library. A changed set is audited
// as counts only, without paths or names, against the system actor.
func UpsertSidecarTracks(ctx context.Context, tx pgx.Tx, sourceID string, tracks []domain.SidecarTrackInput) (domain.SidecarTrackChanges, error) {
	if ctx == nil || tx == nil || !domain.ValidID(sourceID) || len(tracks) > domain.SidecarTracksPerSource {
		return domain.SidecarTrackChanges{}, domain.ErrInvalid
	}
	wanted := make(map[sidecarKey]domain.SidecarTrackInput, len(tracks))
	roots := make([]string, 0, 1)
	for _, in := range tracks {
		if !domain.ValidSidecarTrackInput(in) {
			return domain.SidecarTrackChanges{}, domain.ErrInvalid
		}
		key := sidecarKey{in.RootID, in.RelativePath}
		if _, dup := wanted[key]; dup {
			return domain.SidecarTrackChanges{}, domain.ErrInvalid
		}
		wanted[key] = in
		if !slices.Contains(roots, in.RootID) {
			roots = append(roots, in.RootID)
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return domain.SidecarTrackChanges{}, storageError(err)
	}
	defer sp.Rollback(ctx)
	changes, err := replaceSidecarTracks(ctx, sp, sourceID, wanted, roots)
	if err != nil {
		return domain.SidecarTrackChanges{}, err
	}
	return changes, storageError(sp.Commit(ctx))
}

func replaceSidecarTracks(ctx context.Context, tx pgx.Tx, sourceID string, wanted map[sidecarKey]domain.SidecarTrackInput, roots []string) (domain.SidecarTrackChanges, error) {
	var changes domain.SidecarTrackChanges
	// Serialize replacements per source; FK checks only take key-share locks
	// and are not blocked.
	var library string
	if err := tx.QueryRow(ctx, `SELECT library_id::text FROM media_sources WHERE id=$1::uuid FOR NO KEY UPDATE`, sourceID).Scan(&library); err != nil {
		return changes, storageError(err)
	}
	if len(roots) > 0 {
		var owned int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid AND id=ANY($2::uuid[])`, library, roots).Scan(&owned); err != nil {
			return changes, storageError(err)
		}
		if owned != len(roots) {
			return changes, domain.ErrInvalid
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+sidecarColumns+` FROM media_sidecar_tracks t WHERE t.source_id=$1::uuid FOR UPDATE`, sourceID)
	if err != nil {
		return changes, storageError(err)
	}
	existing := make(map[sidecarKey]domain.SidecarTrackRecord)
	for rows.Next() {
		var r sidecarRow
		if err := rows.Scan(r.targets()...); err != nil {
			rows.Close()
			return changes, storageError(err)
		}
		value, err := r.value()
		if err != nil {
			rows.Close()
			return changes, err
		}
		existing[sidecarKey{value.RootID, value.RelativePath}] = value
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return changes, storageError(err)
	}
	for key, old := range existing {
		if _, keep := wanted[key]; keep {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM media_sidecar_tracks WHERE id=$1::uuid`, old.ID); err != nil {
			return changes, storageError(err)
		}
		changes.Removed++
	}
	var audit sidecarAudit
	for key, in := range wanted {
		if in.Track.Kind == domain.SidecarKindSubtitle {
			audit.Subtitles++
		} else {
			audit.Audio++
		}
		old, found := existing[key]
		if found && sameSidecarTrack(old, in) {
			continue
		}
		t := in.Track
		languages := t.Languages
		if languages == nil {
			languages = []string{}
		}
		args := []any{sourceID, library, in.RootID, in.RelativePath, t.Kind, t.Format, t.Language, languages, t.Title,
			t.Forced, t.SDH, t.Default, t.Commentary, in.Charset, in.Size, in.ModifiedUnixNano, in.Fingerprint}
		args = append(args, domain.InitialSidecarProbeState(t.Kind, t.Format))
		if found {
			// Probe facts and the ".sub" content kind describe one stamp of
			// one file: a changed size, modification time, kind or format
			// sends the row back to its initial probe state (B4).
			_, err = tx.Exec(ctx, `UPDATE media_sidecar_tracks SET kind=$5,format=$6,language=NULLIF($7,''),languages=$8::text[],
 title=NULLIF($9,''),forced=$10,sdh=$11,is_default=$12,commentary=$13,charset=NULLIF($14,''),size=$15,modified_unix_nano=$16,
 fingerprint=$17,`+sidecarProbeResetSQL+`,updated_at=greatest(clock_timestamp(),created_at)
 WHERE source_id=$1::uuid AND library_id=$2::uuid AND root_id=$3::uuid AND relative_path=$4`, args...)
			changes.Updated++
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,language,languages,
 title,forced,sdh,is_default,commentary,charset,size,modified_unix_nano,fingerprint,probe_state)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,NULLIF($7,''),$8::text[],NULLIF($9,''),$10,$11,$12,$13,NULLIF($14,''),$15,$16,$17,$18)`, args...)
			changes.Added++
		}
		if err != nil {
			return domain.SidecarTrackChanges{}, storageError(err)
		}
	}
	if changes.Total() == 0 {
		return changes, nil
	}
	if err := pairSidecarTracks(ctx, tx, sourceID); err != nil {
		return domain.SidecarTrackChanges{}, err
	}
	audit.Added, audit.Updated, audit.Removed = changes.Added, changes.Updated, changes.Removed
	if err := auditAccount(ctx, tx, domain.Actor{}, "media.sidecars_changed", sourceID, nil, audit); err != nil {
		return domain.SidecarTrackChanges{}, err
	}
	return changes, nil
}

// sidecarProbeResetSQL keeps the probe columns of an unchanged file and
// resets them for a changed one. In an UPDATE the right-hand side reads the
// row's old values, so the comparison is against the previous stamp. $15,
// $16, $5 and $6 are the new size, modification time, kind and format; $18
// is the initial probe state of the new kind and format.
const sidecarProbeResetSQL = `probe_state=CASE WHEN ` + sidecarSameFileSQL + ` THEN probe_state ELSE $18 END,
 probed_size=CASE WHEN ` + sidecarSameFileSQL + ` THEN probed_size END,
 probed_modified_unix_nano=CASE WHEN ` + sidecarSameFileSQL + ` THEN probed_modified_unix_nano END,
 codec=CASE WHEN ` + sidecarSameFileSQL + ` THEN codec END,profile=CASE WHEN ` + sidecarSameFileSQL + ` THEN profile END,
 channels=CASE WHEN ` + sidecarSameFileSQL + ` THEN channels END,channel_layout=CASE WHEN ` + sidecarSameFileSQL + ` THEN channel_layout END,
 sample_rate=CASE WHEN ` + sidecarSameFileSQL + ` THEN sample_rate END,bit_rate=CASE WHEN ` + sidecarSameFileSQL + ` THEN bit_rate END,
 duration_micros=CASE WHEN ` + sidecarSameFileSQL + ` THEN duration_micros END,
 subtitle_kind=CASE WHEN ` + sidecarSameFileSQL + ` THEN subtitle_kind END`

const sidecarSameFileSQL = `(size=$15 AND modified_unix_nano=$16 AND kind=$5 AND format=$6)`

// pairSidecarTracks links every ".idx" of the source to the ".sub" with the
// same name in the same folder, and unlinks it when that file is gone (a
// deleted ".sub" row also clears the link through its foreign key). One
// statement per changed source; unchanged links are not written.
func pairSidecarTracks(ctx context.Context, tx pgx.Tx, sourceID string) error {
	_, err := tx.Exec(ctx, `UPDATE media_sidecar_tracks i SET paired_track_id=p.sub,updated_at=greatest(clock_timestamp(),i.created_at)
 FROM (SELECT x.id,(SELECT s.id FROM media_sidecar_tracks s WHERE s.source_id=x.source_id AND s.root_id=x.root_id AND s.format='sub'
  AND left(s.relative_path,-4)=left(x.relative_path,-4) ORDER BY s.relative_path LIMIT 1) AS sub
  FROM media_sidecar_tracks x WHERE x.source_id=$1::uuid AND x.format='idx') p
 WHERE i.id=p.id AND i.paired_track_id IS DISTINCT FROM p.sub`, sourceID)
	return storageError(err)
}
