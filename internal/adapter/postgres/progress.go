package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.ProgressRepository = (*Store)(nil)

// progressTimeout bounds every progress statement. Flushes write at most
// one batch per statement.
const progressTimeout = 5 * time.Second

// startPlaybackSQL opens a session or rejoins the stored one of the same
// user and play key. The live native session, the user and the item grant
// are checked in the same statement, like ListPlaybackSources: a web
// session, an invisible item and a missing item insert nothing. A version
// must belong to the item. Rejoining is limited to an active or timed out
// session of the same item; a stopped or failed one stays closed.
var startPlaybackSQL = `WITH principal AS MATERIALIZED (
 SELECT ` + principalColumnsSQL("u") + `,s.id AS sid,s.device_id,s.client_name FROM users u
 JOIN sessions s ON s.id=@session::uuid AND s.user_id=u.id AND s.client_kind='native' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
 WHERE u.id=@user::uuid AND NOT u.disabled AND u.deleted_at IS NULL
), item AS (
 SELECT i.id,i.library_id FROM principal u JOIN items i ON i.id=@item::uuid
 WHERE ` + itemVisibleSQL("i.library_id", "i.id") + `
  AND (@source::text='' OR EXISTS(SELECT 1 FROM media_sources m WHERE m.id=NULLIF(@source::text,'')::uuid AND m.item_id=i.id AND m.library_id=i.library_id))
)
INSERT INTO playback_sessions AS p(user_id,play_key,auth_session_id,device_id,client_name,item_id,library_id,source_id,state,started_at,last_report_at,position_ticks,runtime_ticks,paused)
SELECT u.id,@key,u.sid,u.device_id,u.client_name,i.id,i.library_id,NULLIF(@source::text,'')::uuid,'active',@at,@at,@position,NULLIF(@runtime::bigint,0),@paused
 FROM principal u CROSS JOIN item i
ON CONFLICT (user_id,play_key) DO UPDATE SET state='active',ended_at=NULL,failure_reason=NULL,
 last_report_at=GREATEST(p.last_report_at,EXCLUDED.last_report_at),auth_session_id=EXCLUDED.auth_session_id,paused=EXCLUDED.paused,
 position_ticks=CASE WHEN @known::boolean THEN EXCLUDED.position_ticks ELSE p.position_ticks END,
 source_id=COALESCE(EXCLUDED.source_id,p.source_id),runtime_ticks=COALESCE(EXCLUDED.runtime_ticks,p.runtime_ticks)
 WHERE p.item_id=EXCLUDED.item_id AND p.state IN ('active','timed_out')
RETURNING p.id::text,p.xmax=0,p.user_id::text,p.item_id::text,COALESCE(p.source_id::text,''),p.started_at,p.last_report_at,p.position_ticks,COALESCE(p.runtime_ticks,0),p.paused,p.sample_count`

// startPlaybackEventSQL is startPlaybackSQL that also raises
// playback.started (G12.1) in the same statement, and so the same
// transaction, when it inserted a new session. Rejoining a session raises
// nothing.
var startPlaybackEventSQL = func() string {
	insert := strings.Index(startPlaybackSQL, "\nINSERT INTO playback_sessions")
	returning := strings.LastIndex(startPlaybackSQL, "\nRETURNING ")
	if insert < 0 || returning < insert {
		panic("startPlaybackSQL shape changed")
	}
	return startPlaybackSQL[:insert] + `, started AS (` + startPlaybackSQL[insert:returning] + `
RETURNING p.id::text AS id,p.xmax=0 AS created,p.user_id::text AS user_id,p.item_id::text AS item_id,COALESCE(p.source_id::text,'') AS source_id,p.started_at,p.last_report_at,p.position_ticks,COALESCE(p.runtime_ticks,0) AS runtime_ticks,p.paused,p.sample_count
), event AS (
 INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,data)
 SELECT 'playback.started',s.started_at,'session',s.id,jsonb_strip_nulls(jsonb_build_object('userId',s.user_id,'itemId',s.item_id,'sourceId',NULLIF(s.source_id,''),'positionTicks',s.position_ticks,'paused',s.paused))
 FROM started s WHERE s.created AND ` + webhookSubscribedSQL("'playback.started'") + `
)
SELECT id,created,user_id,item_id,source_id,started_at,last_report_at,position_ticks,runtime_ticks,paused,sample_count FROM started`
}()

func scanPlaybackSession(row pgx.Row, created *bool) (domain.PlaybackSessionRecord, error) {
	var r domain.PlaybackSessionRecord
	targets := []any{&r.ID}
	if created != nil {
		targets = append(targets, created)
	}
	targets = append(targets, &r.UserID, &r.ItemID, &r.SourceID, &r.StartedAt, &r.LastReportAt, &r.PositionTicks, &r.RuntimeTicks, &r.Paused, &r.SampleCount)
	err := row.Scan(targets...)
	return r, err
}

func (s *Store) StartPlayback(parent context.Context, start domain.PlaybackStart) (domain.PlaybackSessionRecord, error) {
	if parent == nil || !domain.ValidPlayKey(start.PlayKey) || start.At.IsZero() || start.SourceID != "" && !domain.ValidID(start.SourceID) ||
		start.PositionTicks < 0 || start.PositionTicks > domain.PlaybackPositionMax || start.RuntimeTicks < 0 || start.RuntimeTicks > domain.PlaybackPositionMax {
		return domain.PlaybackSessionRecord{}, domain.ErrInvalid
	}
	ctx, cancel, err := readerContext(parent, start.Actor, start.ItemID)
	if err != nil {
		return domain.PlaybackSessionRecord{}, err
	}
	defer cancel()
	var created bool
	statement := startPlaybackSQL
	if s.webhooksOn() {
		statement = startPlaybackEventSQL
	}
	record, err := scanPlaybackSession(s.Pool.QueryRow(ctx, statement, pgx.NamedArgs{
		"user": start.Actor.UserID, "session": start.Actor.SessionID, "item": start.ItemID, "source": start.SourceID, "key": start.PlayKey,
		"at": start.At, "position": start.PositionTicks, "known": start.PositionKnown, "runtime": start.RuntimeTicks, "paused": start.Paused,
	}), &created)
	record.Created = created
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was written: the item is not playable for this session,
		// or the play key names a closed session or another item.
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM playback_sessions p JOIN users u ON u.id=p.user_id AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE p.user_id=$1::uuid AND p.play_key=$2)`, start.Actor.UserID, start.PlayKey).Scan(&exists); err != nil {
			return domain.PlaybackSessionRecord{}, storageError(err)
		}
		if exists {
			return domain.PlaybackSessionRecord{}, domain.ErrConflict
		}
		return domain.PlaybackSessionRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PlaybackSessionRecord{}, storageError(err)
	}
	return record, nil
}

// flushPlaybackSQL writes one batch in a single statement (G23.2): the
// latest state of every session, the user data of their items and the kept
// samples. Only sessions still active are updated, and user data and samples
// follow only the updated rows, so a session that ended elsewhere, a cleared
// history or a deleted user is never written back. Several sessions of one
// user and item in a batch (two devices) are folded into one user data row:
// the latest report decides the resume point and every completed session
// counts one play.
const flushPlaybackSQL = `WITH v AS (
 SELECT * FROM unnest(@id::uuid[],@pos::bigint[],@paused::boolean[],@at::timestamptz[],@reports::integer[],@state::text[],@ended::timestamptz[],@reason::text[],@resume::bigint[],@completed::boolean[],@source::text[])
  AS v(id,pos,paused,at,reports,state,ended,reason,resume,completed,source)
), smp AS (
 SELECT * FROM unnest(@sid::uuid[],@seq::integer[],@sat::timestamptz[],@kind::text[],@spos::bigint[],@spaused::boolean[]) AS s(session_id,seq,at,kind,pos,paused)
), upd AS (
 UPDATE playback_sessions p SET position_ticks=v.pos,paused=v.paused,last_report_at=GREATEST(p.last_report_at,v.at),report_count=p.report_count+v.reports,
  sample_count=GREATEST(p.sample_count,COALESCE((SELECT max(x.seq)+1 FROM smp x WHERE x.session_id=v.id),0)),
  state=COALESCE(v.state,'active'),ended_at=v.ended,failure_reason=v.reason
 FROM v WHERE p.id=v.id AND p.state='active'
 RETURNING p.id,p.user_id,p.item_id
), folded AS (
 SELECT upd.user_id,upd.item_id,(array_agg(v.resume ORDER BY v.at DESC,v.completed))[1] AS resume,bool_or(v.completed) AS completed,
  count(*) FILTER (WHERE v.completed) AS plays,max(v.at) AS at,(array_agg(NULLIF(v.source,'') ORDER BY v.at DESC))[1] AS source
 FROM upd JOIN v ON v.id=upd.id GROUP BY upd.user_id,upd.item_id
), ud AS (
 INSERT INTO user_item_data AS d(user_id,item_id,resume_ticks,played,play_count,last_played_at,last_source_id,updated_at)
 SELECT f.user_id,f.item_id,f.resume,f.completed,f.plays,f.at,m.id,f.at FROM folded f LEFT JOIN media_sources m ON m.id=f.source::uuid AND m.item_id=f.item_id
 ON CONFLICT (user_id,item_id) DO UPDATE SET resume_ticks=EXCLUDED.resume_ticks,played=d.played OR EXCLUDED.played,play_count=d.play_count+EXCLUDED.play_count,
  last_played_at=GREATEST(d.last_played_at,EXCLUDED.last_played_at),last_source_id=COALESCE(EXCLUDED.last_source_id,d.last_source_id),updated_at=GREATEST(d.updated_at,EXCLUDED.updated_at)
 RETURNING 1
), ins AS (
 INSERT INTO playback_samples(session_id,seq,at,kind,position_ticks,paused)
 SELECT x.session_id,x.seq,x.at,x.kind,x.pos,x.paused FROM smp x JOIN upd ON upd.id=x.session_id
 ON CONFLICT DO NOTHING RETURNING 1
)
SELECT (SELECT count(*) FROM upd),(SELECT count(*) FROM ud),(SELECT count(*) FROM ins)`

// flushPlaybackEventSQL is flushPlaybackSQL that also raises
// playback.stopped (G12.1) for every session the batch ends, in the same
// statement. Only rows the batch actually moved out of active count, so a
// session that ended elsewhere raises nothing twice.
var flushPlaybackEventSQL = func() string {
	tail := "\nSELECT (SELECT count(*) FROM upd)"
	at := strings.LastIndex(flushPlaybackSQL, tail)
	if at < 0 {
		panic("flushPlaybackSQL shape changed")
	}
	return flushPlaybackSQL[:at] + `, event AS (
 INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind,subject_id,data)
 SELECT 'playback.stopped',v.ended,'session',upd.id::text,jsonb_build_object('userId',upd.user_id::text,'itemId',upd.item_id::text,'state',v.state,'positionTicks',v.pos,'completed',v.completed)
 FROM upd JOIN v ON v.id=upd.id WHERE v.state IS NOT NULL AND ` + webhookSubscribedSQL("'playback.stopped'") + `
 RETURNING 1
)` + flushPlaybackSQL[at:]
}()

func (s *Store) FlushPlayback(parent context.Context, entries []domain.PlaybackFlush) (domain.PlaybackFlushResult, error) {
	if parent == nil {
		return domain.PlaybackFlushResult{}, domain.ErrInvalid
	}
	if len(entries) == 0 {
		return domain.PlaybackFlushResult{}, nil
	}
	n := len(entries)
	var (
		ids, sources              []string
		positions, resumes        = make([]int64, 0, n), make([]int64, 0, n)
		paused, completed         = make([]bool, 0, n), make([]bool, 0, n)
		ats                       = make([]time.Time, 0, n)
		ended                     = make([]*time.Time, 0, n)
		reports                   = make([]int32, 0, n)
		stateValues, reasonValues = make([]*string, 0, n), make([]*string, 0, n)
		sid, kinds                []string
		seqs                      []int32
		sats                      []time.Time
		spos                      []int64
		spaused                   []bool
	)
	for _, e := range entries {
		if !domain.ValidID(e.SessionID) || e.LastReportAt.IsZero() || e.PositionTicks < 0 || e.PositionTicks > domain.PlaybackPositionMax ||
			e.ResumeTicks < 0 || e.ResumeTicks > domain.PlaybackPositionMax || e.Reports < 0 || e.SourceID != "" && !domain.ValidID(e.SourceID) {
			return domain.PlaybackFlushResult{}, domain.ErrInvalid
		}
		ids, sources = append(ids, e.SessionID), append(sources, e.SourceID)
		positions, resumes = append(positions, e.PositionTicks), append(resumes, e.ResumeTicks)
		paused, completed = append(paused, e.Paused), append(completed, e.Completed)
		ats, reports = append(ats, e.LastReportAt), append(reports, int32(min(e.Reports, 1<<30)))
		if e.End != nil {
			switch e.End.State {
			case domain.PlaybackStopped, domain.PlaybackTimedOut:
				if e.End.FailureReason != "" {
					return domain.PlaybackFlushResult{}, domain.ErrInvalid
				}
			case domain.PlaybackFailed:
				if !domain.ValidPlaybackFailureReason(e.End.FailureReason) {
					return domain.PlaybackFlushResult{}, domain.ErrInvalid
				}
			default:
				return domain.PlaybackFlushResult{}, domain.ErrInvalid
			}
			state, at := string(e.End.State), e.End.At
			stateValues, ended = append(stateValues, &state), append(ended, &at)
			if e.End.FailureReason != "" {
				reason := e.End.FailureReason
				reasonValues = append(reasonValues, &reason)
			} else {
				reasonValues = append(reasonValues, nil)
			}
		} else {
			stateValues, ended, reasonValues = append(stateValues, nil), append(ended, nil), append(reasonValues, nil)
		}
		for _, sample := range e.Samples {
			if !sample.Kind.Valid() || sample.Seq < 0 || sample.Seq >= 4096 || sample.At.IsZero() || sample.PositionTicks < 0 || sample.PositionTicks > domain.PlaybackPositionMax {
				return domain.PlaybackFlushResult{}, domain.ErrInvalid
			}
			sid, seqs, sats = append(sid, e.SessionID), append(seqs, int32(sample.Seq)), append(sats, sample.At)
			kinds, spos, spaused = append(kinds, string(sample.Kind)), append(spos, sample.PositionTicks), append(spaused, sample.Paused)
		}
	}
	ctx, cancel := context.WithTimeout(parent, progressTimeout)
	defer cancel()
	result := domain.PlaybackFlushResult{Statements: 1}
	statement := flushPlaybackSQL
	if s.webhooksOn() {
		statement = flushPlaybackEventSQL
	}
	err := s.Pool.QueryRow(ctx, statement, pgx.NamedArgs{
		"id": ids, "pos": positions, "paused": paused, "at": ats, "reports": reports, "state": stateValues, "ended": ended, "reason": reasonValues,
		"resume": resumes, "completed": completed, "source": sources,
		"sid": emptyIfNil(sid), "seq": seqs, "sat": sats, "kind": emptyIfNil(kinds), "spos": spos, "spaused": spaused,
	}).Scan(&result.Sessions, &result.UserData, &result.Samples)
	if err != nil {
		return domain.PlaybackFlushResult{}, storageError(err)
	}
	return result, nil
}

func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Store) ListStalePlayback(parent context.Context, before time.Time, limit int) ([]domain.PlaybackSessionRecord, error) {
	if parent == nil || before.IsZero() || limit < 1 || limit > 10000 {
		return nil, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, progressTimeout)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT id::text,user_id::text,item_id::text,COALESCE(source_id::text,''),started_at,last_report_at,position_ticks,COALESCE(runtime_ticks,0),paused,sample_count
 FROM playback_sessions WHERE state='active' AND last_report_at<$1 ORDER BY last_report_at,id LIMIT $2`, before, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []domain.PlaybackSessionRecord
	for rows.Next() {
		r, err := scanPlaybackSession(rows, nil)
		if err != nil {
			return nil, storageError(err)
		}
		out = append(out, r)
	}
	return out, storageError(rows.Err())
}

func (s *Store) PurgePlaybackHistory(parent context.Context, cutoff time.Time, limit int) (int, error) {
	if parent == nil || cutoff.IsZero() || limit < 1 || limit > 10000 {
		return 0, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, progressTimeout)
	defer cancel()
	// Sessions the statistics roll-up has not aggregated yet are kept until
	// it has, so retention never loses statistics (G23.5).
	tag, err := s.Pool.Exec(ctx, `DELETE FROM playback_sessions WHERE id IN (
 SELECT id FROM playback_sessions WHERE ended_at<$1 AND stats_through>=ended_at ORDER BY ended_at LIMIT $2)`, cutoff, limit)
	if err != nil {
		return 0, storageError(err)
	}
	return int(tag.RowsAffected()), nil
}

const userItemDataColumns = `COALESCE(d.resume_ticks,0),COALESCE(d.played,false),COALESCE(d.play_count,0),d.last_played_at`

func (s *Store) UserItemData(ctx context.Context, userID string, itemIDs []string) (map[string]domain.UserItemData, error) {
	if ctx == nil || !domain.ValidID(userID) || len(itemIDs) > domain.BrowseLimitMax {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, browsePrincipalSQL+`
SELECT i.id::text,`+userItemDataColumns+` FROM principal u JOIN items i ON i.id=ANY(@ids::uuid[])
 LEFT JOIN user_item_data d ON d.user_id=u.id AND d.item_id=i.id
 WHERE `+itemVisibleSQL("i.library_id", "i.id"), pgx.NamedArgs{"user": userID, "ids": itemIDs})
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := make(map[string]domain.UserItemData, len(itemIDs))
	for rows.Next() {
		var d domain.UserItemData
		if err = rows.Scan(&d.ItemID, &d.ResumeTicks, &d.Played, &d.PlayCount, &d.LastPlayedAt); err != nil {
			return nil, storageError(err)
		}
		out[d.ItemID] = d
	}
	return out, storageError(rows.Err())
}

// SetPlayed follows the upstream semantics: marking played counts one more
// play and clears the resume point; marking unplayed clears the play count
// and the resume point. The item grant is checked in the same statement.
func (s *Store) SetPlayed(ctx context.Context, userID, itemID string, played bool, at time.Time) (domain.UserItemData, error) {
	if ctx == nil || at.IsZero() {
		return domain.UserItemData{}, domain.ErrInvalid
	}
	if !domain.ValidID(userID) || !domain.ValidID(itemID) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	var d domain.UserItemData
	err := s.Pool.QueryRow(ctx, browsePrincipalSQL+`, item AS (
 SELECT i.id FROM principal u JOIN items i ON i.id=@item::uuid WHERE `+itemVisibleSQL("i.library_id", "i.id")+`
)
INSERT INTO user_item_data AS d(user_id,item_id,resume_ticks,played,play_count,last_played_at,updated_at)
SELECT @user::uuid,item.id,0,@played,CASE WHEN @played THEN 1 ELSE 0 END,CASE WHEN @played THEN @at::timestamptz END,@at FROM item
ON CONFLICT (user_id,item_id) DO UPDATE SET resume_ticks=0,played=EXCLUDED.played,
 play_count=CASE WHEN EXCLUDED.played THEN d.play_count+1 ELSE 0 END,
 last_played_at=COALESCE(EXCLUDED.last_played_at,d.last_played_at),updated_at=EXCLUDED.updated_at
RETURNING d.item_id::text,d.resume_ticks,d.played,d.play_count,d.last_played_at`,
		pgx.NamedArgs{"user": userID, "item": itemID, "played": played, "at": at}).Scan(&d.ItemID, &d.ResumeTicks, &d.Played, &d.PlayCount, &d.LastPlayedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserItemData{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.UserItemData{}, storageError(err)
	}
	return d, nil
}

// resumeKinds are the playable kinds; series and seasons have no position.
var resumeKinds = []string{"Movie", "Episode", "HomeVideo"}

func (s *Store) ListResume(ctx context.Context, userID string, q domain.ResumeQuery) (domain.ResumePage, error) {
	if ctx == nil || !domain.ValidID(userID) || !domain.ValidResumeQuery(q) {
		return domain.ResumePage{}, domain.ErrInvalid
	}
	kinds := resumeKinds
	if len(q.Kinds) > 0 {
		kinds = nil
		for _, k := range q.Kinds {
			for _, playable := range resumeKinds {
				if k == playable {
					kinds = append(kinds, k)
				}
			}
		}
		if len(kinds) == 0 {
			return domain.ResumePage{Items: []domain.ResumeEntry{}}, nil
		}
	}
	args := pgx.NamedArgs{"user": userID, "kinds": kinds, "limit": q.Limit, "offset": q.Offset}
	matched := browsePrincipalSQL + `, matched AS (
 SELECT i.id,i.library_id,i.kind,i.title,d.resume_ticks,d.play_count,d.last_played_at FROM principal u
 JOIN user_item_data d ON d.user_id=u.id AND d.resume_ticks>0 AND NOT d.played
 JOIN items i ON i.id=d.item_id
 WHERE ` + itemVisibleSQL("i.library_id", "i.id") + ` AND i.kind=ANY(@kinds::text[])
)`
	rows, err := s.Pool.Query(ctx, matched+`
SELECT i.id::text,i.library_id::text,COALESCE(p.parent_id,i.library_id)::text,i.kind,i.title,COALESCE(fs.value,''),COALESCE(fo.value,''),COALESCE(NULLIF(fd.value,''),''),COALESCE(`+browseYearSQL+`,0),
 i.resume_ticks,i.play_count,i.last_played_at,COALESCE(r.runtime_ticks,0),count(*) OVER()
 FROM matched i`+browseMetadataSQL+`
 LEFT JOIN LATERAL (SELECT x.runtime_ticks FROM playback_sessions x WHERE x.user_id=@user::uuid AND x.item_id=i.id AND x.runtime_ticks IS NOT NULL
  ORDER BY x.last_report_at DESC LIMIT 1) r ON true
 ORDER BY i.last_played_at DESC NULLS LAST,i.id LIMIT @limit OFFSET @offset`, args)
	if err != nil {
		return domain.ResumePage{}, storageError(err)
	}
	defer rows.Close()
	page := domain.ResumePage{Items: make([]domain.ResumeEntry, 0, min(q.Limit, 64))}
	for rows.Next() {
		var e domain.ResumeEntry
		item := &e.Item
		if err = rows.Scan(&item.ID, &item.LibraryID, &item.ParentID, &item.Kind, &item.Title, &item.SortTitle, &item.Overview, &item.PremiereDate, &item.Year,
			&e.Data.ResumeTicks, &e.Data.PlayCount, &e.Data.LastPlayedAt, &e.RuntimeTicks, &page.Total); err != nil {
			return domain.ResumePage{}, storageError(err)
		}
		e.Data.ItemID = item.ID
		page.Items = append(page.Items, e)
	}
	if err = rows.Err(); err != nil {
		return domain.ResumePage{}, storageError(err)
	}
	rows.Close()
	if len(page.Items) == 0 && q.Offset > 0 {
		if err = s.Pool.QueryRow(ctx, matched+` SELECT count(*) FROM matched`, args).Scan(&page.Total); err != nil {
			return domain.ResumePage{}, storageError(err)
		}
	}
	return page, nil
}

// ClearPlaybackHistory deletes the actor's own sessions, samples and user
// data in one transaction with the audit record (G23.4). The audit keeps
// only counts.
func (s *Store) ClearPlaybackHistory(ctx context.Context, actor domain.Actor) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	sessions, data, err := deletePlaybackData(ctx, tx, actor.UserID)
	if err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "playback.history_cleared", Actor: actor, TargetID: actor.UserID,
		After: map[string]int64{"sessions": sessions, "userData": data}}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// deletePlaybackData removes every playback row of a user, including the
// watch statistics computed from them (G23.4); samples follow their
// sessions. Sessions go first: an aggregation that already locked them
// commits before the statistics rows are deleted, and one that has not
// finds them gone and writes nothing.
func deletePlaybackData(ctx context.Context, tx pgx.Tx, userID string) (sessions, data int64, err error) {
	tag, err := tx.Exec(ctx, `DELETE FROM playback_sessions WHERE user_id=$1::uuid`, userID)
	if err != nil {
		return 0, 0, storageError(err)
	}
	sessions = tag.RowsAffected()
	if tag, err = tx.Exec(ctx, `DELETE FROM user_item_data WHERE user_id=$1::uuid`, userID); err != nil {
		return 0, 0, storageError(err)
	}
	data = tag.RowsAffected()
	if _, err = tx.Exec(ctx, `DELETE FROM watch_stats_daily WHERE user_id=$1::uuid`, userID); err != nil {
		return 0, 0, storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM watch_stats_history WHERE user_id=$1::uuid`, userID); err != nil {
		return 0, 0, storageError(err)
	}
	return sessions, data, nil
}

func (s *Store) ListActivePlayback(ctx context.Context, actor domain.Actor, limit int) ([]domain.ActivePlayback, error) {
	if ctx == nil || limit < 1 || limit > domain.ActivePlaybackMax {
		return nil, domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return nil, domain.ErrUnauthenticated
	}
	var admin bool
	err := s.Pool.QueryRow(ctx, `SELECT u.is_admin FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1::uuid AND s.id=$2::uuid
 AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()`, actor.UserID, actor.SessionID).Scan(&admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUnauthenticated
	}
	if err != nil {
		return nil, storageError(err)
	}
	if !admin {
		return nil, domain.ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.id::text,p.user_id::text,u.name,COALESCE(p.device_id,''),COALESCE(p.client_name,''),p.item_id::text,i.title,
 COALESCE(p.source_id::text,''),p.delivery,p.started_at,p.last_report_at,p.position_ticks,COALESCE(p.runtime_ticks,0),p.paused
 FROM playback_sessions p JOIN users u ON u.id=p.user_id JOIN items i ON i.id=p.item_id
 WHERE p.state='active' ORDER BY p.last_report_at DESC,p.id LIMIT $1`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := make([]domain.ActivePlayback, 0)
	for rows.Next() {
		var a domain.ActivePlayback
		if err = rows.Scan(&a.ID, &a.UserID, &a.UserName, &a.DeviceID, &a.ClientName, &a.ItemID, &a.ItemTitle, &a.SourceID, &a.Delivery,
			&a.StartedAt, &a.LastReportAt, &a.PositionTicks, &a.RuntimeTicks, &a.Paused); err != nil {
			return nil, storageError(err)
		}
		out = append(out, a)
	}
	return out, storageError(rows.Err())
}
