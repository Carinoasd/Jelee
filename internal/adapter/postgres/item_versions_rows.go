package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Rows of an item that a merge removes or transfers (G20.3, G20.5). Rows are
// copied verbatim as data, like the metadata backup copies them; nothing here
// decides what a user may see. TestItemVersionSnapshotCoversCascades fails
// when a table that cascades from items is neither kept nor accounted for.

type versionRowTable struct {
	table string
	where string
	// prune drops kept rows that no longer fit the catalog before they are
	// restored (a user, root, parent or version deleted since, a folder or
	// scan group taken by another item); it reads them as vr.
	prune string
}

const versionUserGone = `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.id=vr.user_id)`

// versionSnapshotTables are restored in this order: the item row first,
// then, after its versions moved back, everything else.
var versionSnapshotTables = []versionRowTable{
	{"items", `id=$1::uuid`, ""},
	{"item_metadata_state", `item_id=$1::uuid`, ""},
	{"item_metadata_fields", `item_id=$1::uuid`, ""},
	{"item_metadata_facts", `item_id=$1::uuid`, ""},
	{"item_nfo_field_locks", `item_id=$1::uuid`, ""},
	{"item_nfo_observations", `item_id=$1::uuid`, ""},
	{"item_directory_sources", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.id=vr.root_id)
 OR EXISTS(SELECT 1 FROM item_directory_sources d WHERE d.id=vr.id OR d.root_id=vr.root_id AND d.relative_path=vr.relative_path)`},
	{"item_parent_links", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM items p WHERE p.id=vr.parent_id)`},
	{"catalog_scan_items", `item_id=$1::uuid`, `DELETE FROM vr WHERE EXISTS(SELECT 1 FROM catalog_scan_items c WHERE c.library_id=vr.library_id AND c.kind=vr.kind AND c.group_digest=vr.group_digest)`},
	{"item_images", `item_id=$1::uuid`, `DELETE FROM vr WHERE vr.root_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.id=vr.root_id)`},
	{"user_item_data", `item_id=$1::uuid`, versionUserGone + `;
 UPDATE vr SET last_source_id=NULL WHERE NOT EXISTS(SELECT 1 FROM media_sources m WHERE m.id=vr.last_source_id)`},
	{"user_item_access_rules", `item_id=$1::uuid`, versionUserGone},
	{"watch_stats_daily", `item_id=$1::uuid`, versionUserGone},
	{"watch_stats_history", `item_id=$1::uuid`, versionUserGone},
	// Version-level preferences move with their version instead.
	{"user_track_preferences", `item_id=$1::uuid AND source_id IS NULL`, versionUserGone},
	{"item_version_exclusions", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.id=vr.root_id)`},
	{"item_primary_versions", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM media_sources m WHERE m.id=vr.source_id AND m.item_id=vr.item_id)`},
	// Collection and playlist membership (G02.1) comes back with the item
	// unless its collection or playlist was deleted since; the merge
	// itself does not carry it to the target.
	{"collection_items", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM collections c WHERE c.id=vr.collection_id)`},
	{"playlist_items", `item_id=$1::uuid`, `DELETE FROM vr WHERE NOT EXISTS(SELECT 1 FROM playlists p WHERE p.id=vr.playlist_id)`},
}

// versionSnapshotMoved are the cascading tables whose rows a merge moves to
// the target before the item is removed, so none are left to keep.
var versionSnapshotMoved = []string{"media_sources", "catalog_scan_sources", "playback_sessions", "catalog_scan_item_aliases"}

// versionSnapshotDropped cascade with the item and are not kept: prepared
// NFO writes expire within minutes and are rebuilt on request; embedded cover
// attempts are a rebuildable cache keyed by the file fingerprint; share links
// on the absorbed item are only revoked or expired ones (a live one refuses
// the merge, liveShareOnItem).
var versionSnapshotDropped = []string{"nfo_write_preparations", "item_embedded_cover_attempts", "share_links"}

type versionRowsPhase int

const (
	versionRowsItem versionRowsPhase = iota
	versionRowsRest
)

// snapshotVersionItem reads every kept row of an item.
func snapshotVersionItem(ctx context.Context, tx pgx.Tx, item string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for _, t := range versionSnapshotTables {
		var doc []byte
		if err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb)::text FROM `+t.table+` t WHERE `+t.where, item).Scan(&doc); err != nil {
			return nil, storageError(err)
		}
		out[t.table] = doc
	}
	return out, nil
}

// restoreVersionItemRows inserts kept rows back, the item row alone in the
// first phase and every other table in the second.
func restoreVersionItemRows(ctx context.Context, tx pgx.Tx, rows map[string]json.RawMessage, phase versionRowsPhase) error {
	for i, t := range versionSnapshotTables {
		if (i == 0) != (phase == versionRowsItem) {
			continue
		}
		doc, ok := rows[t.table]
		if !ok || string(doc) == "[]" {
			continue
		}
		if t.prune == "" {
			if _, err := tx.Exec(ctx, `INSERT INTO `+t.table+` SELECT * FROM jsonb_populate_recordset(NULL::`+t.table+`,$1::jsonb)`, string(doc)); err != nil {
				return storageError(err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE vr ON COMMIT DROP AS SELECT * FROM jsonb_populate_recordset(NULL::`+t.table+`,$1::jsonb)`, string(doc)); err != nil {
			return storageError(err)
		}
		if _, err := tx.Exec(ctx, t.prune); err != nil {
			return storageError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+t.table+` SELECT * FROM vr; DROP TABLE vr`); err != nil {
			return storageError(err)
		}
	}
	return nil
}

// versionTransfer records what a transfer changed on the receiving item so
// undo can reverse it.
type versionTransfer struct {
	At       time.Time `json:"at"`
	Sessions []string  `json:"sessions"`
	// UserDataUsers had rows on the giving item; UserDataPrior holds the
	// receiving item's rows of those users before the transfer.
	UserDataUsers []string        `json:"userDataUsers"`
	UserDataPrior json.RawMessage `json:"userDataPrior"`
	Rules         []ruleTransfer  `json:"rules"`
	Preferences   []string        `json:"preferences"`
}

type ruleTransfer struct {
	User   string  `json:"user"`
	Prior  *string `json:"prior"`
	Merged string  `json:"merged"`
}

// transferItemData applies the merge policy from one item to another:
//
//   - playback sessions (history and statistics marks) move;
//   - user data: played if either was played, play counts add, the resume
//     point and last version come from the later play;
//   - access rules: a hide on either item hides the result, else allow;
//   - watch statistics add up per user and day;
//   - item-level track preferences are copied where the receiving item has
//     none.
//
// The giving item's own rows stay until it is removed.
func transferItemData(ctx context.Context, tx pgx.Tx, from, to string) (versionTransfer, error) {
	var out versionTransfer
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.At); err != nil {
		return out, storageError(err)
	}
	out.At = out.At.Truncate(time.Microsecond)
	rows, err := tx.Query(ctx, `UPDATE playback_sessions SET item_id=$2::uuid WHERE item_id=$1::uuid RETURNING id::text`, from, to)
	if err != nil {
		return out, storageError(err)
	}
	if out.Sessions, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, storageError(err)
	}
	var prior []byte
	if err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(f.user_id::text ORDER BY f.user_id),'{}'),
 (SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb)::text FROM user_item_data t WHERE t.item_id=$2::uuid AND t.user_id IN (SELECT user_id FROM user_item_data WHERE item_id=$1::uuid))
 FROM user_item_data f WHERE f.item_id=$1::uuid`, from, to).Scan(&out.UserDataUsers, &prior); err != nil {
		return out, storageError(err)
	}
	out.UserDataPrior = prior
	if _, err = tx.Exec(ctx, `INSERT INTO user_item_data AS d(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at)
 SELECT user_id,$2::uuid,resume_ticks,played,play_count,last_played_at,last_source_id,$3 FROM user_item_data WHERE item_id=$1::uuid
 ON CONFLICT(user_id,item_id) DO UPDATE SET played=d.played OR EXCLUDED.played,
  play_count=least(d.play_count::bigint+EXCLUDED.play_count,2147483647)::integer,
  resume_ticks=CASE WHEN EXCLUDED.last_played_at>d.last_played_at OR d.last_played_at IS NULL AND EXCLUDED.last_played_at IS NOT NULL THEN EXCLUDED.resume_ticks ELSE d.resume_ticks END,
  last_source_id=CASE WHEN EXCLUDED.last_played_at>d.last_played_at OR d.last_played_at IS NULL AND EXCLUDED.last_played_at IS NOT NULL THEN COALESCE(EXCLUDED.last_source_id,d.last_source_id) ELSE COALESCE(d.last_source_id,EXCLUDED.last_source_id) END,
  last_played_at=GREATEST(d.last_played_at,EXCLUDED.last_played_at),updated_at=EXCLUDED.updated_at`, from, to, out.At); err != nil {
		return out, storageError(err)
	}
	rows, err = tx.Query(ctx, `WITH prior AS (SELECT user_id,effect FROM user_item_access_rules WHERE item_id=$2::uuid),
 up AS (INSERT INTO user_item_access_rules AS r(user_id,item_id,effect) SELECT user_id,$2::uuid,effect FROM user_item_access_rules WHERE item_id=$1::uuid
  ON CONFLICT(user_id,item_id) DO UPDATE SET effect=CASE WHEN r.effect='hide' OR EXCLUDED.effect='hide' THEN 'hide' ELSE 'allow' END RETURNING r.user_id,r.effect)
 SELECT up.user_id::text,prior.effect,up.effect FROM up LEFT JOIN prior ON prior.user_id=up.user_id ORDER BY up.user_id`, from, to)
	if err != nil {
		return out, storageError(err)
	}
	for rows.Next() {
		var r ruleTransfer
		if err = rows.Scan(&r.User, &r.Prior, &r.Merged); err != nil {
			rows.Close()
			return out, storageError(err)
		}
		out.Rules = append(out.Rules, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO watch_stats_daily AS d(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,rewatches,completions,completion_milli,updated_at)
 SELECT user_id,day,$2::uuid,library_id,effective_ms,sessions,views,first_plays,rewatches,completions,completion_milli,now() FROM watch_stats_daily WHERE item_id=$1::uuid
 ON CONFLICT(user_id,day,item_id) DO UPDATE SET effective_ms=least(d.effective_ms+EXCLUDED.effective_ms,90000000),sessions=d.sessions+EXCLUDED.sessions,
  views=d.views+EXCLUDED.views,first_plays=d.first_plays+EXCLUDED.first_plays,rewatches=d.rewatches+EXCLUDED.rewatches,
  completions=d.completions+EXCLUDED.completions,completion_milli=d.completion_milli+EXCLUDED.completion_milli,updated_at=now()`, from, to); err != nil {
		return out, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO watch_stats_history AS d(user_id,item_id,views,completions,last_completed,updated_at)
 SELECT user_id,$2::uuid,views,completions,last_completed,updated_at FROM watch_stats_history WHERE item_id=$1::uuid
 ON CONFLICT(user_id,item_id) DO UPDATE SET views=d.views+EXCLUDED.views,completions=d.completions+EXCLUDED.completions,
  last_completed=CASE WHEN EXCLUDED.updated_at>d.updated_at THEN EXCLUDED.last_completed ELSE d.last_completed END,updated_at=GREATEST(d.updated_at,EXCLUDED.updated_at)`, from, to); err != nil {
		return out, storageError(err)
	}
	rows, err = tx.Query(ctx, `INSERT INTO user_track_preferences(user_id,item_id,audio_language,audio_commentary,subtitle_mode,subtitle_language,subtitle_sdh,updated_at)
 SELECT user_id,$2::uuid,audio_language,audio_commentary,subtitle_mode,subtitle_language,subtitle_sdh,$3 FROM user_track_preferences WHERE item_id=$1::uuid AND source_id IS NULL
 AND num_nonnulls(audio_language,audio_commentary,subtitle_mode,subtitle_language,subtitle_sdh)>0
 ON CONFLICT(user_id,item_id) WHERE item_id IS NOT NULL AND source_id IS NULL DO NOTHING RETURNING id::text`, from, to, out.At)
	if err != nil {
		return out, storageError(err)
	}
	if out.Preferences, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, storageError(err)
	}
	return out, nil
}

// undoTransfer reverses transferItemData on the receiving item. Rows the
// receiving item changed after the transfer (a user played it, an
// administrator changed a rule) keep their newer state; statistics are
// subtracted where every counter still covers the given amount.
func undoTransfer(ctx context.Context, tx pgx.Tx, to, from string, t versionTransfer, absorbed map[string]json.RawMessage) error {
	if len(t.Sessions) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE playback_sessions SET item_id=$3::uuid WHERE id=ANY($1::uuid[]) AND item_id=$2::uuid`, t.Sessions, to, from); err != nil {
			return storageError(err)
		}
	}
	if len(t.UserDataUsers) > 0 {
		rows, err := tx.Query(ctx, `DELETE FROM user_item_data WHERE item_id=$1::uuid AND user_id=ANY($2::uuid[]) AND updated_at=$3 RETURNING user_id::text`, to, t.UserDataUsers, t.At)
		if err != nil {
			return storageError(err)
		}
		unchanged, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return storageError(err)
		}
		if len(unchanged) > 0 && len(t.UserDataPrior) > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO user_item_data SELECT p.* FROM jsonb_populate_recordset(NULL::user_item_data,$1::jsonb) p WHERE p.user_id=ANY($2::uuid[])`, string(t.UserDataPrior), unchanged); err != nil {
				return storageError(err)
			}
		}
	}
	for _, r := range t.Rules {
		var err error
		if r.Prior == nil {
			_, err = tx.Exec(ctx, `DELETE FROM user_item_access_rules WHERE user_id=$1::uuid AND item_id=$2::uuid AND effect=$3`, r.User, to, r.Merged)
		} else {
			_, err = tx.Exec(ctx, `UPDATE user_item_access_rules SET effect=$4 WHERE user_id=$1::uuid AND item_id=$2::uuid AND effect=$3`, r.User, to, r.Merged, *r.Prior)
		}
		if err != nil {
			return storageError(err)
		}
	}
	if doc := absorbed["watch_stats_daily"]; len(doc) > 0 && string(doc) != "[]" {
		if _, err := tx.Exec(ctx, `UPDATE watch_stats_daily d SET effective_ms=greatest(d.effective_ms-g.effective_ms,0),sessions=d.sessions-g.sessions,views=d.views-g.views,
 first_plays=d.first_plays-g.first_plays,rewatches=d.rewatches-g.rewatches,completions=d.completions-g.completions,completion_milli=d.completion_milli-g.completion_milli,updated_at=now()
 FROM jsonb_populate_recordset(NULL::watch_stats_daily,$1::jsonb) g
 WHERE d.item_id=$2::uuid AND d.user_id=g.user_id AND d.day=g.day AND d.sessions>=g.sessions AND d.views>=g.views AND d.first_plays>=g.first_plays
  AND d.rewatches>=g.rewatches AND d.completions>=g.completions AND d.completion_milli>=g.completion_milli`, string(doc), to); err != nil {
			return storageError(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM watch_stats_daily WHERE item_id=$1::uuid AND sessions=0 AND views=0 AND completions=0 AND effective_ms=0`, to); err != nil {
			return storageError(err)
		}
	}
	if doc := absorbed["watch_stats_history"]; len(doc) > 0 && string(doc) != "[]" {
		if _, err := tx.Exec(ctx, `UPDATE watch_stats_history d SET views=d.views-g.views,completions=d.completions-g.completions
 FROM jsonb_populate_recordset(NULL::watch_stats_history,$1::jsonb) g
 WHERE d.item_id=$2::uuid AND d.user_id=g.user_id AND d.views>=g.views AND d.completions>=g.completions`, string(doc), to); err != nil {
			return storageError(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM watch_stats_history WHERE item_id=$1::uuid AND views=0 AND completions=0`, to); err != nil {
			return storageError(err)
		}
	}
	if len(t.Preferences) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM user_track_preferences WHERE id=ANY($1::uuid[]) AND updated_at=$2`, t.Preferences, t.At); err != nil {
			return storageError(err)
		}
	}
	return nil
}
