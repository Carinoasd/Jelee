package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Track preferences (G16.5, G20.4). A user reads and writes only their own
// rows, and only for items the unified visibility filter shows them; a
// hidden item is answered like a missing one. Preferences choose among the
// original tracks and are not audited, like the interface preferences.

const trackPreferenceColumns = `p.audio_language,p.audio_commentary,p.audio_track,p.subtitle_mode,p.subtitle_language,p.subtitle_sdh,p.subtitle_track`

// trackPreferencesSQL reads, in one statement bound to the live session and
// the visibility filter, the user's defaults, the item level and the
// version levels of the item's current versions. A visible item always
// yields a row (all null without preferences); an invisible one none.
const trackPreferencesSQL = `SELECT COALESCE(u.locale,''),p.id IS NOT NULL,p.item_id IS NOT NULL,COALESCE(p.source_id::text,''),` + trackPreferenceColumns + `
 FROM items i
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 LEFT JOIN user_track_preferences p ON p.user_id=u.id AND (p.item_id IS NULL OR p.item_id=i.id AND (p.source_id IS NULL OR EXISTS(SELECT 1 FROM media_sources m WHERE m.id=p.source_id AND m.item_id=i.id)))
 WHERE i.id=$3::uuid AND `

var trackPreferencesQuery = trackPreferencesSQL + itemVisibleSQL("$4", "i.library_id", "i.id") + ` ORDER BY p.item_id NULLS FIRST,p.source_id NULLS FIRST`

type trackRow struct {
	present, item bool
	source        string
	p             domain.TrackPreference
}

func scanTrackPreference(row pgx.Row, extra ...any) (domain.TrackPreference, error) {
	var p domain.TrackPreference
	err := row.Scan(append(extra, &p.AudioLanguage, &p.AudioCommentary, &p.AudioTrack, &p.SubtitleMode, &p.SubtitleLanguage, &p.SubtitleSDH, &p.SubtitleTrack)...)
	return p, err
}

func (s *Store) readTrackPreferences(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, actor domain.Actor, itemID string) (domain.TrackPreferenceSet, bool, error) {
	set := domain.TrackPreferenceSet{Versions: map[string]domain.TrackPreference{}}
	rows, err := q.Query(ctx, trackPreferencesQuery, actor.UserID, actor.SessionID, itemID, requestScopeArg(ctx))
	if err != nil {
		return set, false, storageError(err)
	}
	defer rows.Close()
	visible := false
	for rows.Next() {
		visible = true
		var r trackRow
		if r.p, err = scanTrackPreference(rows, &set.Locale, &r.present, &r.item, &r.source); err != nil {
			return set, false, storageError(err)
		}
		switch {
		case !r.present:
		case !r.item:
			p := r.p
			set.User = &p
		case r.source == "":
			p := r.p
			set.Item = &p
		default:
			set.Versions[r.source] = r.p
		}
	}
	if err = rows.Err(); err != nil {
		return set, false, storageError(err)
	}
	return set, visible, nil
}

// EffectiveTrackPreferences reads every level for playback information.
func (s *Store) EffectiveTrackPreferences(parent context.Context, actor domain.Actor, itemID string) (domain.TrackPreferenceSet, error) {
	ctx, cancel, err := readerContext(parent, actor, itemID)
	if err != nil {
		return domain.TrackPreferenceSet{}, err
	}
	defer cancel()
	set, visible, err := s.readTrackPreferences(ctx, s.Pool, actor, itemID)
	if err != nil {
		return set, err
	}
	if !visible {
		return set, domain.ErrNotFound
	}
	// The item is visible to the live session; its session choice (only
	// while unexpired and while the version still belongs to the item) and
	// the user's version preference complete the set.
	var (
		session      domain.SessionTracks
		audio, sub   *string
		sessionFound bool
	)
	err = s.Pool.QueryRow(ctx, `SELECT v.max_height,v.dynamic_range,v.prefer_smaller,v.max_bitrate,t.source_id IS NOT NULL,COALESCE(t.source_id::text,''),t.audio_track,t.subtitle_track
 FROM (SELECT 1) one
 LEFT JOIN user_version_preferences v ON v.user_id=$1::uuid
 LEFT JOIN session_track_selections t ON t.user_id=$1::uuid AND t.item_id=$2::uuid AND t.updated_at>clock_timestamp()-make_interval(secs=>$3)
  AND EXISTS(SELECT 1 FROM media_sources m WHERE m.id=t.source_id AND m.item_id=t.item_id)`,
		actor.UserID, itemID, domain.SessionTrackTTL.Seconds()).Scan(&set.VersionPreference.MaxHeight, &set.VersionPreference.DynamicRange,
		&set.VersionPreference.PreferSmaller, &set.VersionPreference.MaxBitrate, &sessionFound, &session.SourceID, &audio, &sub)
	if err != nil {
		return set, storageError(err)
	}
	if sessionFound {
		if audio != nil {
			session.AudioTrack = *audio
		}
		if sub != nil {
			session.SubtitleTrack = *sub
		}
		set.Session = &session
	}
	return set, nil
}

// VersionPreference returns the user's version preference (G20.2).
func (s *Store) VersionPreference(ctx context.Context, actor domain.Actor) (domain.VersionPreference, error) {
	if ctx == nil {
		return domain.VersionPreference{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.VersionPreference{}, err
	}
	defer tx.Rollback(ctx)
	var p domain.VersionPreference
	err = tx.QueryRow(ctx, `SELECT max_height,dynamic_range,prefer_smaller,max_bitrate FROM user_version_preferences WHERE user_id=$1::uuid`, actor.UserID).
		Scan(&p.MaxHeight, &p.DynamicRange, &p.PreferSmaller, &p.MaxBitrate)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.VersionPreference{}, storageError(err)
	}
	return p, storageError(tx.Commit(ctx))
}

// SetVersionPreference replaces the user's version preference; an empty
// preference removes it.
func (s *Store) SetVersionPreference(ctx context.Context, actor domain.Actor, p domain.VersionPreference) (domain.VersionPreference, error) {
	if ctx == nil {
		return domain.VersionPreference{}, domain.ErrInvalid
	}
	if _, err := domain.NormalizeVersionPreference(p); err != nil {
		return domain.VersionPreference{}, err
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.VersionPreference{}, err
	}
	defer tx.Rollback(ctx)
	if p.Empty() {
		_, err = tx.Exec(ctx, `DELETE FROM user_version_preferences WHERE user_id=$1::uuid`, actor.UserID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO user_version_preferences(user_id,max_height,dynamic_range,prefer_smaller,max_bitrate) VALUES($1::uuid,$2,$3,$4,$5)
 ON CONFLICT(user_id) DO UPDATE SET max_height=EXCLUDED.max_height,dynamic_range=EXCLUDED.dynamic_range,prefer_smaller=EXCLUDED.prefer_smaller,
 max_bitrate=EXCLUDED.max_bitrate,updated_at=clock_timestamp()`, actor.UserID, p.MaxHeight, p.DynamicRange, p.PreferSmaller, p.MaxBitrate)
	}
	if err != nil {
		return domain.VersionPreference{}, storageError(err)
	}
	return p, storageError(tx.Commit(ctx))
}

// recordSessionTracksSQL replaces the user's choice of a visible item in
// one statement bound to the live native session; the version must be the
// item's. It also deletes the user's expired and surplus choices and a
// bounded batch of everyone's expired ones, so the table never grows past
// SessionTrackUserMax per user plus what expires.
var recordSessionTracksSQL = `WITH ok AS (SELECT i.id FROM items i
 JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 JOIN sessions s ON s.id=$2::uuid AND s.user_id=u.id AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 WHERE i.id=$3::uuid AND EXISTS(SELECT 1 FROM media_sources m WHERE m.id=$4::uuid AND m.item_id=i.id) AND ` + itemVisibleSQL("$9", "i.library_id", "i.id") + `),
up AS (INSERT INTO session_track_selections AS t(user_id,item_id,source_id,play_key,audio_track,subtitle_track)
 SELECT $1::uuid,ok.id,$4::uuid,$5,$6,$7 FROM ok
 ON CONFLICT(user_id,item_id) DO UPDATE SET source_id=EXCLUDED.source_id,play_key=EXCLUDED.play_key,audio_track=EXCLUDED.audio_track,
  subtitle_track=EXCLUDED.subtitle_track,updated_at=clock_timestamp() RETURNING 1),
expired AS (DELETE FROM session_track_selections WHERE ctid IN (SELECT ctid FROM session_track_selections
 WHERE updated_at<=clock_timestamp()-make_interval(secs=>$8) AND NOT (user_id=$1::uuid AND item_id=$3::uuid) LIMIT 100)),
surplus AS (DELETE FROM session_track_selections WHERE user_id=$1::uuid AND item_id<>$3::uuid AND item_id IN (SELECT item_id FROM session_track_selections
 WHERE user_id=$1::uuid ORDER BY updated_at DESC,item_id OFFSET $10))
SELECT count(*) FROM up`

// RecordSessionTracks stores the session choice of a visible item (G20.4).
func (s *Store) RecordSessionTracks(ctx context.Context, actor domain.Actor, itemID, playKey string, tracks domain.SessionTracks) error {
	if ctx == nil || !domain.ValidID(itemID) || !domain.ValidID(tracks.SourceID) || !domain.ValidPlayKey(playKey) ||
		tracks.AudioTrack == "" && tracks.SubtitleTrack == "" || tracks.AudioTrack != "" && !domain.ValidTrackRef(tracks.AudioTrack) ||
		tracks.SubtitleTrack != "" && tracks.SubtitleTrack != domain.SubtitleTrackOff && !domain.ValidTrackRef(tracks.SubtitleTrack) {
		return domain.ErrInvalid
	}
	var stored int
	err := s.Pool.QueryRow(ctx, recordSessionTracksSQL, actor.UserID, actor.SessionID, itemID, tracks.SourceID, playKey,
		nullableText(tracks.AudioTrack), nullableText(tracks.SubtitleTrack), domain.SessionTrackTTL.Seconds(), requestScopeArg(ctx), domain.SessionTrackUserMax-1).Scan(&stored)
	if err != nil {
		return storageError(err)
	}
	if stored == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func trackPreferenceView(itemID string, set domain.TrackPreferenceSet) domain.TrackPreferenceView {
	view := domain.TrackPreferenceView{ItemID: itemID, User: set.User, Item: set.Item, Versions: []domain.VersionTrackPreference{}}
	for source, p := range set.Versions {
		view.Versions = append(view.Versions, domain.VersionTrackPreference{SourceID: source, Preference: p})
	}
	slices.SortFunc(view.Versions, func(a, b domain.VersionTrackPreference) int { return strings.Compare(a.SourceID, b.SourceID) })
	return view
}

// TrackPreferences returns what the user stored for one visible item.
func (s *Store) TrackPreferences(parent context.Context, actor domain.Actor, itemID string) (domain.TrackPreferenceView, error) {
	set, err := s.EffectiveTrackPreferences(parent, actor, itemID)
	if err != nil {
		return domain.TrackPreferenceView{}, err
	}
	return trackPreferenceView(itemID, set), nil
}

// SetTrackPreference replaces the item level or one version level of a
// visible item; an empty preference removes the level.
func (s *Store) SetTrackPreference(ctx context.Context, actor domain.Actor, itemID, sourceID string, p domain.TrackPreference) (domain.TrackPreferenceView, error) {
	if ctx == nil || !domain.ValidID(itemID) || sourceID != "" && !domain.ValidID(sourceID) {
		return domain.TrackPreferenceView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return domain.TrackPreferenceView{}, err
	}
	defer tx.Rollback(ctx)
	var found bool
	// The version must be one of the visible item's own versions; any other
	// source is answered like a missing item.
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items i JOIN users u ON u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE i.id=$2::uuid AND ($3::uuid IS NULL OR EXISTS(SELECT 1 FROM media_sources m WHERE m.id=$3::uuid AND m.item_id=i.id)) AND `+itemVisibleSQL("$4", "i.library_id", "i.id")+`)`,
		actor.UserID, itemID, nullableID(sourceID), requestScopeArg(ctx)).Scan(&found)
	if err != nil {
		return domain.TrackPreferenceView{}, storageError(err)
	}
	if !found {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	var target string
	switch {
	case p.Empty() && sourceID == "":
		_, err = tx.Exec(ctx, `DELETE FROM user_track_preferences WHERE user_id=$1::uuid AND item_id=$2::uuid AND source_id IS NULL`, actor.UserID, itemID)
	case p.Empty():
		_, err = tx.Exec(ctx, `DELETE FROM user_track_preferences WHERE user_id=$1::uuid AND source_id=$2::uuid`, actor.UserID, sourceID)
	case sourceID == "":
		target = `(user_id,item_id) WHERE item_id IS NOT NULL AND source_id IS NULL`
	default:
		target = `(user_id,source_id) WHERE source_id IS NOT NULL`
	}
	if target != "" {
		_, err = tx.Exec(ctx, `INSERT INTO user_track_preferences(user_id,item_id,source_id,audio_language,audio_commentary,audio_track,subtitle_mode,subtitle_language,subtitle_sdh,subtitle_track)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT `+target+` DO UPDATE SET item_id=EXCLUDED.item_id,audio_language=EXCLUDED.audio_language,
 audio_commentary=EXCLUDED.audio_commentary,audio_track=EXCLUDED.audio_track,subtitle_mode=EXCLUDED.subtitle_mode,subtitle_language=EXCLUDED.subtitle_language,
 subtitle_sdh=EXCLUDED.subtitle_sdh,subtitle_track=EXCLUDED.subtitle_track,updated_at=clock_timestamp()`,
			actor.UserID, itemID, nullableID(sourceID), p.AudioLanguage, p.AudioCommentary, p.AudioTrack, p.SubtitleMode, p.SubtitleLanguage, p.SubtitleSDH, p.SubtitleTrack)
	}
	if err != nil {
		return domain.TrackPreferenceView{}, storageError(err)
	}
	// A deliberate preference replaces the item's session choice (G20.4).
	if _, err = tx.Exec(ctx, `DELETE FROM session_track_selections WHERE user_id=$1::uuid AND item_id=$2::uuid`, actor.UserID, itemID); err != nil {
		return domain.TrackPreferenceView{}, storageError(err)
	}
	set, visible, err := s.readTrackPreferences(ctx, tx, actor, itemID)
	if err != nil {
		return domain.TrackPreferenceView{}, err
	}
	if !visible {
		return domain.TrackPreferenceView{}, domain.ErrNotFound
	}
	return trackPreferenceView(itemID, set), storageError(tx.Commit(ctx))
}

// UserTrackPreference returns the user's own defaults, nil when unset.
func (s *Store) UserTrackPreference(ctx context.Context, actor domain.Actor) (*domain.TrackPreference, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, err := scanTrackPreference(tx.QueryRow(ctx, `SELECT `+trackPreferenceColumns+` FROM user_track_preferences p WHERE p.user_id=$1::uuid AND p.item_id IS NULL`, actor.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storageError(tx.Commit(ctx))
	}
	if err != nil {
		return nil, storageError(err)
	}
	return &p, storageError(tx.Commit(ctx))
}

// SetUserTrackPreference replaces the user's defaults; an empty preference
// removes them.
func (s *Store) SetUserTrackPreference(ctx context.Context, actor domain.Actor, p domain.TrackPreference) (*domain.TrackPreference, error) {
	if ctx == nil || p.AudioTrack != nil || p.SubtitleTrack != nil {
		return nil, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// New defaults replace every session choice of the user (G20.4).
	if _, err = tx.Exec(ctx, `DELETE FROM session_track_selections WHERE user_id=$1::uuid`, actor.UserID); err != nil {
		return nil, storageError(err)
	}
	if p.Empty() {
		if _, err = tx.Exec(ctx, `DELETE FROM user_track_preferences WHERE user_id=$1::uuid AND item_id IS NULL`, actor.UserID); err != nil {
			return nil, storageError(err)
		}
		return nil, storageError(tx.Commit(ctx))
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_track_preferences(user_id,audio_language,audio_commentary,subtitle_mode,subtitle_language,subtitle_sdh) VALUES($1::uuid,$2,$3,$4,$5,$6)
 ON CONFLICT(user_id) WHERE item_id IS NULL DO UPDATE SET audio_language=EXCLUDED.audio_language,audio_commentary=EXCLUDED.audio_commentary,subtitle_mode=EXCLUDED.subtitle_mode,
 subtitle_language=EXCLUDED.subtitle_language,subtitle_sdh=EXCLUDED.subtitle_sdh,updated_at=clock_timestamp()`,
		actor.UserID, p.AudioLanguage, p.AudioCommentary, p.SubtitleMode, p.SubtitleLanguage, p.SubtitleSDH); err != nil {
		return nil, storageError(err)
	}
	out := p
	return &out, storageError(tx.Commit(ctx))
}

func nullableText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
