package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.WatchStatsRepository = (*Store)(nil)

const (
	// watchStatsLock serializes aggregation runs across instances; a run
	// that cannot take it leaves the work to the holder.
	watchStatsLock = 17481267
	// watchStatsPriorMax bounds the earlier sessions one batch reads.
	watchStatsPriorMax = 2000
	// watchStatsTimeout bounds one aggregation batch and one report.
	watchStatsTimeout = 30 * time.Second
)

const watchStatsSessionColumns = `p.id::text,p.user_id::text,p.item_id::text,p.library_id::text,p.started_at,p.ended_at,COALESCE(p.runtime_ticks,0),
 p.stats_through,COALESCE(to_char(p.stats_day,'YYYY-MM-DD'),''),p.stats_counted,p.stats_completed,p.stats_completion_milli,COALESCE(p.stats_play,'none')`

func scanWatchStatsSession(row pgx.Row) (domain.WatchStatsSession, error) {
	var (
		s       domain.WatchStatsSession
		ended   *time.Time
		through *time.Time
		mark    domain.WatchStatsMark
		play    string
	)
	if err := row.Scan(&s.ID, &s.UserID, &s.ItemID, &s.LibraryID, &s.StartedAt, &ended, &s.RuntimeTicks,
		&through, &mark.Day, &mark.Counted, &mark.Completed, &mark.CompletionMilli, &play); err != nil {
		return s, err
	}
	if ended != nil {
		s.EndedAt = *ended
	}
	if through != nil {
		mark.Through, mark.Play = *through, domain.WatchPlayKind(play)
		s.Previous = &mark
	}
	return s, nil
}

// AggregateWatchStats runs one aggregation batch in one transaction
// (G23.5): it claims up to limit pending sessions (ended and not yet
// aggregated through their end), reads their samples up to that end, the
// already aggregated sessions of the same users and items that overlap
// them and the classification history, lets compute turn them into daily
// deltas, and writes the deltas, the history and the session marks. The
// marks are written only while each session still ends where it was read,
// so a session deleted (history cleared, user deleted, retention) or
// rejoined in the meantime rolls the batch back; the next run reads the
// current state. Only one run proceeds at a time across instances.
func (s *Store) AggregateWatchStats(parent context.Context, limit int, compute func(domain.WatchStatsBatch) (domain.WatchStatsOutcome, error)) (int, error) {
	if parent == nil || compute == nil || limit < 1 || limit > 1000 {
		return 0, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, watchStatsTimeout)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext(current_schema()),$1)`, watchStatsLock).Scan(&locked); err != nil {
		return 0, storageError(err)
	}
	if !locked {
		return 0, nil
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return 0, storageError(err)
	}
	batch := domain.WatchStatsBatch{History: map[string]domain.WatchHistory{}}
	rows, err := tx.Query(ctx, `SELECT `+watchStatsSessionColumns+` FROM playback_sessions p
 WHERE p.ended_at IS NOT NULL AND (p.stats_through IS NULL OR p.stats_through<p.ended_at) ORDER BY p.ended_at,p.id LIMIT $1`, limit)
	if err != nil {
		return 0, storageError(err)
	}
	batch.Sessions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.WatchStatsSession, error) { return scanWatchStatsSession(r) })
	if err != nil {
		return 0, storageError(err)
	}
	if len(batch.Sessions) == 0 {
		return 0, nil
	}
	n := len(batch.Sessions)
	ids, users, items := make([]string, 0, n), make([]string, 0, n), make([]string, 0, n)
	starts, ends := make([]time.Time, 0, n), make([]time.Time, 0, n)
	for _, x := range batch.Sessions {
		ids, users, items = append(ids, x.ID), append(users, x.UserID), append(items, x.ItemID)
		starts, ends = append(starts, x.StartedAt), append(ends, x.EndedAt)
	}
	// Earlier aggregated sessions of the same user and item whose counted
	// span overlaps a pending session: their wall time is already in the
	// daily rows and must not be counted again (rule 7).
	rows, err = tx.Query(ctx, `SELECT `+watchStatsSessionColumns+` FROM playback_sessions p
 WHERE p.stats_through IS NOT NULL AND p.id<>ALL(@ids::uuid[]) AND EXISTS(
  SELECT 1 FROM unnest(@users::uuid[],@items::uuid[],@starts::timestamptz[],@ends::timestamptz[]) n(user_id,item_id,s,e)
  WHERE p.user_id=n.user_id AND p.item_id=n.item_id AND p.started_at<n.e AND p.stats_through>n.s)
 ORDER BY p.started_at,p.id LIMIT @limit`, pgx.NamedArgs{"ids": ids, "users": users, "items": items, "starts": starts, "ends": ends, "limit": watchStatsPriorMax})
	if err != nil {
		return 0, storageError(err)
	}
	if batch.Prior, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.WatchStatsSession, error) { return scanWatchStatsSession(r) }); err != nil {
		return 0, storageError(err)
	}
	// Samples up to the end each session was read with: a pending session's
	// samples up to its end, a prior session's up to its mark.
	sampleIDs, through := append([]string(nil), ids...), append([]time.Time(nil), ends...)
	index := make(map[string]*domain.WatchStatsSession, n+len(batch.Prior))
	for i := range batch.Sessions {
		index[batch.Sessions[i].ID] = &batch.Sessions[i]
	}
	for i := range batch.Prior {
		p := &batch.Prior[i]
		index[p.ID] = p
		sampleIDs, through = append(sampleIDs, p.ID), append(through, p.Previous.Through)
	}
	rows, err = tx.Query(ctx, `SELECT x.session_id::text,x.seq,x.at,x.kind,x.position_ticks,x.paused FROM playback_samples x
 JOIN unnest($1::uuid[],$2::timestamptz[]) t(id,through) ON x.session_id=t.id AND x.at<=t.through ORDER BY x.session_id,x.seq`, sampleIDs, through)
	if err != nil {
		return 0, storageError(err)
	}
	for rows.Next() {
		var (
			id     string
			sample domain.PlaybackSample
			kind   string
		)
		if err = rows.Scan(&id, &sample.Seq, &sample.At, &kind, &sample.PositionTicks, &sample.Paused); err != nil {
			rows.Close()
			return 0, storageError(err)
		}
		sample.Kind = domain.WatchSampleKind(kind)
		if target := index[id]; target != nil {
			target.Samples = append(target.Samples, sample)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, storageError(err)
	}
	rows, err = tx.Query(ctx, `SELECT h.user_id::text,h.item_id::text,h.views,h.completions,h.last_completed FROM watch_stats_history h
 JOIN (SELECT DISTINCT * FROM unnest($1::uuid[],$2::uuid[])) k(user_id,item_id) ON h.user_id=k.user_id AND h.item_id=k.item_id`, users, items)
	if err != nil {
		return 0, storageError(err)
	}
	for rows.Next() {
		var user, item string
		var h domain.WatchHistory
		if err = rows.Scan(&user, &item, &h.Views, &h.Completions, &h.LastCompleted); err != nil {
			rows.Close()
			return 0, storageError(err)
		}
		batch.History[domain.WatchPairKey(user, item)] = h
	}
	if err = rows.Err(); err != nil {
		return 0, storageError(err)
	}

	outcome, err := compute(batch)
	if err != nil {
		return 0, err
	}
	if err = writeWatchStatsOutcome(ctx, tx, batch, outcome); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, storageError(err)
	}
	return n, nil
}

func writeWatchStatsOutcome(ctx context.Context, tx pgx.Tx, batch domain.WatchStatsBatch, outcome domain.WatchStatsOutcome) error {
	n := len(batch.Sessions)
	var (
		ids, days, plays  = make([]string, 0, n), make([]string, 0, n), make([]string, 0, n)
		old               = make([]*time.Time, 0, n)
		through           = make([]time.Time, 0, n)
		counted, complete = make([]bool, 0, n), make([]bool, 0, n)
		milli             = make([]int32, 0, n)
	)
	for _, x := range batch.Sessions {
		mark, ok := outcome.Marks[x.ID]
		if !ok || !mark.Through.Equal(x.EndedAt) || mark.CompletionMilli < 0 || mark.CompletionMilli > 1000 || mark.Counted && mark.Day == "" {
			return domain.ErrInvalid
		}
		var previous *time.Time
		if x.Previous != nil {
			t := x.Previous.Through
			previous = &t
		}
		ids, days, plays = append(ids, x.ID), append(days, mark.Day), append(plays, string(mark.Play))
		old, through = append(old, previous), append(through, mark.Through)
		counted, complete, milli = append(counted, mark.Counted), append(complete, mark.Completed), append(milli, int32(mark.CompletionMilli))
	}
	tag, err := tx.Exec(ctx, `UPDATE playback_sessions p SET stats_through=v.through,stats_day=NULLIF(v.day,'')::date,stats_counted=v.counted,
 stats_completed=v.completed,stats_completion_milli=v.milli,stats_play=v.play
 FROM unnest(@id::uuid[],@old::timestamptz[],@through::timestamptz[],@day::text[],@counted::boolean[],@completed::boolean[],@milli::integer[],@play::text[])
  AS v(id,old,through,day,counted,completed,milli,play)
 WHERE p.id=v.id AND p.stats_through IS NOT DISTINCT FROM v.old AND p.ended_at=v.through`,
		pgx.NamedArgs{"id": ids, "old": old, "through": through, "day": days, "counted": counted, "completed": complete, "milli": milli, "play": plays})
	if err != nil {
		return storageError(err)
	}
	if int(tag.RowsAffected()) != n {
		// A session was deleted or rejoined since it was read.
		return domain.ErrConflict
	}
	if len(outcome.Daily) > 0 {
		m := len(outcome.Daily)
		var (
			du, dd, di, dl         = make([]string, 0, m), make([]string, 0, m), make([]string, 0, m), make([]string, 0, m)
			eff, cm                = make([]int64, 0, m), make([]int64, 0, m)
			ses, vw, fp, rw, comps = make([]int32, 0, m), make([]int32, 0, m), make([]int32, 0, m), make([]int32, 0, m), make([]int32, 0, m)
		)
		for _, r := range outcome.Daily {
			if _, err := domain.ParseWatchStatsDate(r.Day); err != nil || r.EffectiveMillis < 0 || r.Sessions < 0 || r.Views != r.FirstPlays+r.Rewatches || r.CompletionMilli < 0 {
				return domain.ErrInvalid
			}
			du, dd, di, dl = append(du, r.UserID), append(dd, r.Day), append(di, r.ItemID), append(dl, r.LibraryID)
			eff, cm = append(eff, r.EffectiveMillis), append(cm, r.CompletionMilli)
			ses, vw, fp, rw, comps = append(ses, int32(r.Sessions)), append(vw, int32(r.Views)), append(fp, int32(r.FirstPlays)), append(rw, int32(r.Rewatches)), append(comps, int32(r.Completions))
		}
		// Effective time is a union per user, item and day, so it never
		// exceeds the day; the bound only keeps a rounding drift from
		// stopping the roll-up.
		if _, err = tx.Exec(ctx, `INSERT INTO watch_stats_daily AS d(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,rewatches,completions,completion_milli,updated_at)
 SELECT v.user_id,v.day::date,v.item_id,v.library_id,LEAST(v.eff,90000000),v.ses,v.vw,v.fp,v.rw,v.comps,v.cm,now()
 FROM unnest(@user::uuid[],@day::text[],@item::uuid[],@library::uuid[],@eff::bigint[],@ses::integer[],@vw::integer[],@fp::integer[],@rw::integer[],@comps::integer[],@cm::bigint[])
  AS v(user_id,day,item_id,library_id,eff,ses,vw,fp,rw,comps,cm)
 ON CONFLICT (user_id,day,item_id) DO UPDATE SET effective_ms=LEAST(d.effective_ms+EXCLUDED.effective_ms,90000000),sessions=d.sessions+EXCLUDED.sessions,
  views=d.views+EXCLUDED.views,first_plays=d.first_plays+EXCLUDED.first_plays,rewatches=d.rewatches+EXCLUDED.rewatches,
  completions=d.completions+EXCLUDED.completions,completion_milli=d.completion_milli+EXCLUDED.completion_milli,updated_at=now()`,
			pgx.NamedArgs{"user": du, "day": dd, "item": di, "library": dl, "eff": eff, "ses": ses, "vw": vw, "fp": fp, "rw": rw, "comps": comps, "cm": cm}); err != nil {
			return storageError(err)
		}
	}
	if len(outcome.History) > 0 {
		var hu, hi []string
		var hv, hc []int32
		var hl []bool
		for key, h := range outcome.History {
			user, item := domain.SplitWatchPairKey(key)
			hu, hi, hv, hc, hl = append(hu, user), append(hi, item), append(hv, int32(h.Views)), append(hc, int32(h.Completions)), append(hl, h.LastCompleted)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO watch_stats_history AS h(user_id,item_id,views,completions,last_completed,updated_at)
 SELECT * ,now() FROM unnest($1::uuid[],$2::uuid[],$3::integer[],$4::integer[],$5::boolean[])
 ON CONFLICT (user_id,item_id) DO UPDATE SET views=EXCLUDED.views,completions=EXCLUDED.completions,last_completed=EXCLUDED.last_completed,updated_at=now()`,
			hu, hi, hv, hc, hl); err != nil {
			return storageError(err)
		}
	}
	return nil
}

// PurgeWatchStats deletes at most limit daily rows of days before before
// (the statistics retention period).
func (s *Store) PurgeWatchStats(parent context.Context, before time.Time, limit int) (int, error) {
	if parent == nil || before.IsZero() || limit < 1 || limit > 100000 {
		return 0, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, watchStatsTimeout)
	defer cancel()
	tag, err := s.Pool.Exec(ctx, `DELETE FROM watch_stats_daily WHERE (user_id,day,item_id) IN (
 SELECT user_id,day,item_id FROM watch_stats_daily WHERE day<$1::date ORDER BY day LIMIT $2)`, before.Format(time.DateOnly), limit)
	if err != nil {
		return 0, storageError(err)
	}
	return int(tag.RowsAffected()), nil
}

// PendingWatchStats counts the ended sessions the roll-up has not
// aggregated yet.
func (s *Store) PendingWatchStats(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, domain.ErrInvalid
	}
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM playback_sessions WHERE ended_at IS NOT NULL AND (stats_through IS NULL OR stats_through<ended_at)`).Scan(&n)
	return n, storageError(err)
}

// watchStatsViewer authorizes the actor's live session and returns whether
// it is an administrator.
func watchStatsViewer(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor domain.Actor) (bool, error) {
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return false, domain.ErrUnauthenticated
	}
	var admin bool
	err := q.QueryRow(ctx, `SELECT u.is_admin FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1::uuid AND s.id=$2::uuid
 AND NOT u.disabled AND u.deleted_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>now()`, actor.UserID, actor.SessionID).Scan(&admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrUnauthenticated
	}
	return admin, storageError(err)
}

// watchStatsScope selects the daily rows of a report: the day range, the
// subject when there is one, and only items the viewer can see (G48.3),
// through the unified filter.
func watchStatsScope(subject bool) string {
	scope := ` FROM watch_stats_daily d WHERE d.day BETWEEN @from::date AND @to::date
 AND EXISTS(SELECT 1 FROM users u WHERE u.id=@viewer::uuid AND ` + itemVisibleSQL("@rq", "d.library_id", "d.item_id") + `)`
	if subject {
		scope += ` AND d.user_id=@subject::uuid`
	}
	return scope
}

const watchStatsSums = `COALESCE(sum(d.effective_ms),0)::bigint,COALESCE(sum(d.sessions),0)::bigint,COALESCE(sum(d.views),0)::bigint,
 COALESCE(sum(d.first_plays),0)::bigint,COALESCE(sum(d.rewatches),0)::bigint,COALESCE(sum(d.completions),0)::bigint,COALESCE(sum(d.completion_milli),0)::bigint`

func scanSums(dest []any, sums *domain.WatchStatsSums) []any {
	return append(dest, &sums.EffectiveMillis, &sums.Sessions, &sums.Views, &sums.FirstPlays, &sums.Rewatches, &sums.Completions, &sums.CompletionMilli)
}

func watchStatsBucket(period domain.WatchPeriod) string {
	switch period {
	case domain.WatchPeriodWeek:
		return `(d.day-((extract(dow FROM d.day)::int-@wstart::int+7)%7))`
	case domain.WatchPeriodMonth:
		return `date_trunc('month',d.day)::date`
	case domain.WatchPeriodYear:
		return `date_trunc('year',d.day)::date`
	}
	return `d.day`
}

// WatchStatsReport reads one statistics report from the daily rows in a
// repeatable read snapshot, so every part sums the same rows. A subject
// other than the actor, or every user, needs an administrator; an unknown
// or deleted subject is ErrNotFound.
func (s *Store) WatchStatsReport(parent context.Context, actor domain.Actor, q domain.WatchStatsQuery) (domain.WatchStatsReport, error) {
	if parent == nil || q.From.IsZero() || q.To.IsZero() || q.To.Before(q.From) || q.Top < 1 || q.Top > domain.WatchStatsTopMax ||
		q.SubjectID != "" && !domain.ValidID(q.SubjectID) {
		return domain.WatchStatsReport{}, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, watchStatsTimeout)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.WatchStatsReport{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	admin, err := watchStatsViewer(ctx, tx, actor)
	if err != nil {
		return domain.WatchStatsReport{}, err
	}
	if !admin && q.SubjectID != actor.UserID {
		return domain.WatchStatsReport{}, domain.ErrForbidden
	}
	if q.SubjectID != "" && q.SubjectID != actor.UserID {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid AND deleted_at IS NULL)`, q.SubjectID).Scan(&exists); err != nil {
			return domain.WatchStatsReport{}, storageError(err)
		}
		if !exists {
			return domain.WatchStatsReport{}, domain.ErrNotFound
		}
	}
	subject := q.SubjectID != ""
	scope := watchStatsScope(subject)
	args := pgx.NamedArgs{"from": q.From.Format(time.DateOnly), "to": q.To.Format(time.DateOnly), "admin": admin, "viewer": actor.UserID,
		"subject": q.SubjectID, "top": q.Top, "wstart": int(q.WeekStart), "libraries": domain.WatchStatsLibrariesMax, "rq": requestScopeArg(ctx)}
	report := domain.WatchStatsReport{UserID: q.SubjectID, From: q.From.Format(time.DateOnly), To: q.To.Format(time.DateOnly), Period: q.Period,
		Periods: []domain.WatchStatsPeriodRow{}, TopItems: []domain.WatchStatsItemRow{}, Libraries: []domain.WatchStatsLibraryRow{}, Kinds: []domain.WatchStatsKindRow{}}

	var totals domain.WatchStatsSums
	if err = tx.QueryRow(ctx, `SELECT `+watchStatsSums+scope, args).Scan(scanSums(nil, &totals)...); err != nil {
		return domain.WatchStatsReport{}, storageError(err)
	}
	report.Totals = totals.Totals()

	bucket := watchStatsBucket(q.Period)
	if err = collectWatchStats(ctx, tx, `SELECT to_char(`+bucket+`,'YYYY-MM-DD'),`+watchStatsSums+scope+` GROUP BY 1 ORDER BY 1`, args,
		func(row pgx.Rows) error {
			var r domain.WatchStatsPeriodRow
			var sums domain.WatchStatsSums
			if err := row.Scan(scanSums([]any{&r.Start}, &sums)...); err != nil {
				return err
			}
			r.WatchStatsTotals = sums.Totals()
			report.Periods = append(report.Periods, r)
			return nil
		}); err != nil {
		return domain.WatchStatsReport{}, err
	}

	userData, userDataJoin := ``, ``
	if subject {
		userData = `,ud.item_id IS NOT NULL,COALESCE(ud.resume_ticks,0),COALESCE(ud.played,false),COALESCE(ud.play_count,0),ud.last_played_at`
		userDataJoin = ` LEFT JOIN user_item_data ud ON ud.user_id=@subject::uuid AND ud.item_id=t.item_id`
	}
	if err = collectWatchStats(ctx, tx, `SELECT t.item_id::text,i.library_id::text,i.kind,i.title,t.eff,t.ses,t.vw,t.fp,t.rw,t.comps,t.cm`+userData+` FROM (
 SELECT d.item_id,`+watchStatsSums+scope+` GROUP BY d.item_id ORDER BY 2 DESC,4 DESC,d.item_id LIMIT @top) t(item_id,eff,ses,vw,fp,rw,comps,cm)
 JOIN items i ON i.id=t.item_id`+userDataJoin+` ORDER BY t.eff DESC,t.vw DESC,t.item_id`, args,
		func(row pgx.Rows) error {
			var r domain.WatchStatsItemRow
			var sums domain.WatchStatsSums
			dest := scanSums([]any{&r.ItemID, &r.LibraryID, &r.Kind, &r.Title}, &sums)
			var has bool
			var d domain.UserItemData
			if subject {
				dest = append(dest, &has, &d.ResumeTicks, &d.Played, &d.PlayCount, &d.LastPlayedAt)
			}
			if err := row.Scan(dest...); err != nil {
				return err
			}
			r.WatchStatsTotals = sums.Totals()
			if subject {
				d.ItemID = r.ItemID
				r.UserData = &d
			}
			report.TopItems = append(report.TopItems, r)
			return nil
		}); err != nil {
		return domain.WatchStatsReport{}, err
	}

	if err = collectWatchStats(ctx, tx, `SELECT t.library_id::text,l.name,t.eff,t.ses,t.vw,t.fp,t.rw,t.comps,t.cm FROM (
 SELECT d.library_id,`+watchStatsSums+scope+` GROUP BY d.library_id) t(library_id,eff,ses,vw,fp,rw,comps,cm)
 JOIN libraries l ON l.id=t.library_id ORDER BY t.eff DESC,t.library_id LIMIT @libraries`, args,
		func(row pgx.Rows) error {
			var r domain.WatchStatsLibraryRow
			var sums domain.WatchStatsSums
			if err := row.Scan(scanSums([]any{&r.LibraryID, &r.Name}, &sums)...); err != nil {
				return err
			}
			r.WatchStatsTotals = sums.Totals()
			report.Libraries = append(report.Libraries, r)
			return nil
		}); err != nil {
		return domain.WatchStatsReport{}, err
	}

	if err = collectWatchStats(ctx, tx, `SELECT i.kind,`+watchStatsSums+` FROM (SELECT d.*`+scope+`) d JOIN items i ON i.id=d.item_id
 GROUP BY i.kind ORDER BY 2 DESC,i.kind`, args,
		func(row pgx.Rows) error {
			var r domain.WatchStatsKindRow
			var sums domain.WatchStatsSums
			if err := row.Scan(scanSums([]any{&r.Kind}, &sums)...); err != nil {
				return err
			}
			r.WatchStatsTotals = sums.Totals()
			report.Kinds = append(report.Kinds, r)
			return nil
		}); err != nil {
		return domain.WatchStatsReport{}, err
	}

	if !subject {
		report.TopUsers = []domain.WatchStatsUserRow{}
		if err = collectWatchStats(ctx, tx, `SELECT t.user_id::text,u.name,t.eff,t.ses,t.vw,t.fp,t.rw,t.comps,t.cm FROM (
 SELECT d.user_id,`+watchStatsSums+scope+` GROUP BY d.user_id ORDER BY 2 DESC,4 DESC,d.user_id LIMIT @top) t(user_id,eff,ses,vw,fp,rw,comps,cm)
 JOIN users u ON u.id=t.user_id ORDER BY t.eff DESC,t.vw DESC,t.user_id`, args,
			func(row pgx.Rows) error {
				var r domain.WatchStatsUserRow
				var sums domain.WatchStatsSums
				if err := row.Scan(scanSums([]any{&r.UserID, &r.UserName}, &sums)...); err != nil {
					return err
				}
				r.WatchStatsTotals = sums.Totals()
				report.TopUsers = append(report.TopUsers, r)
				return nil
			}); err != nil {
			return domain.WatchStatsReport{}, err
		}
	}
	return report, nil
}

func collectWatchStats(ctx context.Context, tx pgx.Tx, query string, args pgx.NamedArgs, scan func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, query, args)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err = scan(rows); err != nil {
			return storageError(err)
		}
	}
	return storageError(rows.Err())
}

func watchStatsExportArgs(q domain.WatchStatsExportQuery) (string, pgx.NamedArgs, error) {
	if q.From.IsZero() || q.To.IsZero() || q.To.Before(q.From) || q.Limit < 1 || q.SubjectID != "" && !domain.ValidID(q.SubjectID) ||
		q.Format != domain.WatchStatsExportCSV && q.Format != domain.WatchStatsExportNDJSON {
		return "", nil, domain.ErrInvalid
	}
	filter := ` WHERE d.day BETWEEN @from::date AND @to::date`
	if q.SubjectID != "" {
		filter += ` AND d.user_id=@subject::uuid`
	}
	return filter, pgx.NamedArgs{"from": q.From.Format(time.DateOnly), "to": q.To.Format(time.DateOnly), "subject": q.SubjectID, "limit": q.Limit}, nil
}

// AuditWatchStatsExport authorizes an administrator export, counts its rows
// and records the audit event watch_stats.exported before any row is sent
// (G23.4). A range with more than q.Limit rows is refused with
// ErrWatchStatsExportLimit and is not audited.
func (s *Store) AuditWatchStatsExport(parent context.Context, actor domain.Actor, q domain.WatchStatsExportQuery) (int, error) {
	if parent == nil {
		return 0, domain.ErrInvalid
	}
	filter, args, err := watchStatsExportArgs(q)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(parent, watchStatsTimeout)
	defer cancel()
	admin, err := watchStatsViewer(ctx, s.Pool, actor)
	if err != nil {
		return 0, err
	}
	if !admin {
		return 0, domain.ErrForbidden
	}
	var rows int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM watch_stats_daily d`+filter+` LIMIT @limit+1) x`, args).Scan(&rows); err != nil {
		return 0, storageError(err)
	}
	if rows > q.Limit {
		return 0, domain.ErrWatchStatsExportLimit
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	after := map[string]any{"from": args["from"], "to": args["to"], "format": q.Format, "rows": rows}
	entry := AuditEntry{Event: "watch_stats.exported", Actor: actor, After: after}
	if q.SubjectID != "" {
		entry.TargetID = q.SubjectID
	}
	if err = appendAudit(ctx, tx, entry); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, storageError(err)
	}
	return rows, nil
}

// StreamWatchStatsExport sends the daily rows of an audited export in day,
// user and item order, at most q.Limit rows, while the actor is still a
// live administrator.
func (s *Store) StreamWatchStatsExport(ctx context.Context, actor domain.Actor, q domain.WatchStatsExportQuery, write func(domain.WatchStatsExportRow) error) error {
	if ctx == nil || write == nil {
		return domain.ErrInvalid
	}
	filter, args, err := watchStatsExportArgs(q)
	if err != nil {
		return err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrUnauthenticated
	}
	args["actor"], args["session"] = actor.UserID, actor.SessionID
	rows, err := s.Pool.Query(ctx, `WITH principal AS MATERIALIZED (
 SELECT u.id FROM users u JOIN sessions s ON s.user_id=u.id AND s.id=@session::uuid AND s.revoked_at IS NULL AND s.expires_at>now()
 WHERE u.id=@actor::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL)
SELECT to_char(d.day,'YYYY-MM-DD'),d.user_id::text,u.name,d.item_id::text,d.library_id::text,i.kind,i.title,
 d.effective_ms,d.sessions,d.views,d.first_plays,d.rewatches,d.completions,d.completion_milli
 FROM principal p CROSS JOIN watch_stats_daily d JOIN users u ON u.id=d.user_id JOIN items i ON i.id=d.item_id`+filter+`
 ORDER BY d.day,d.user_id,d.item_id LIMIT @limit`, args)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.WatchStatsExportRow
		var milli int64
		if err = rows.Scan(&r.Day, &r.UserID, &r.UserName, &r.ItemID, &r.LibraryID, &r.Kind, &r.Title,
			&r.EffectiveMillis, &r.Sessions, &r.Views, &r.FirstPlays, &r.Rewatches, &r.Completions, &milli); err != nil {
			return storageError(err)
		}
		r.CompletionRate = domain.WatchStatsSums{Sessions: r.Sessions, CompletionMilli: milli}.Totals().CompletionRate
		if err = write(r); err != nil {
			return err
		}
	}
	return storageError(rows.Err())
}
