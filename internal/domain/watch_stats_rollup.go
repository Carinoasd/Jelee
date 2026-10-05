package domain

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// Incremental watch statistics roll-up (G23.3, G23.5).
//
// Ended playback sessions are aggregated once into daily rows of user, item
// and local day (docs/watch-statistics.md). Requests read only those rows;
// they never read the sample table. The aggregator applies the rules of
// ComputeWatchSession to each session's stored samples and adds the result
// to the daily rows as a delta:
//
//   - Effective time is a set union per user and item: for the sessions of a
//     batch, the delta of a day is the length of (prior ∪ new) minus the
//     length of prior on that day, where prior is the wall time already
//     counted by overlapping sessions aggregated earlier. Two devices
//     reporting the same viewing therefore count once, whichever ends first.
//   - Counters (sessions, views, first plays, re-watches, completions) go to
//     the local day of the session's first credited segment, or of its start
//     when nothing was credited.
//   - First play and re-watch are classified against the per user and item
//     history in aggregation order (sessions of one batch in start order).
//   - A timed out session that a client rejoined and ended again is
//     aggregated again: the part counted before (up to its mark) is prior
//     for the effective time, and its counters change only where they grew
//     (counted now, completed now, higher completion).

// ErrWatchStatsExportLimit reports that an export would exceed the
// configured row limit; the caller should narrow the range.
var ErrWatchStatsExportLimit = errors.New("watch statistics export exceeds the row limit")

// WatchStatsMark records what an earlier aggregation counted for a session.
type WatchStatsMark struct {
	// Through is the session end the aggregation read; segments before it
	// are counted.
	Through time.Time
	// Day is the local day the session's counters went to; empty when the
	// session was not counted.
	Day             string
	Counted         bool
	Completed       bool
	CompletionMilli int
	Play            WatchPlayKind
}

// WatchStatsSession is one stored session as the aggregator reads it.
// Samples hold only samples received up to EndedAt (for a session to
// aggregate) or up to Previous.Through (for a prior session).
type WatchStatsSession struct {
	ID           string
	UserID       string
	ItemID       string
	LibraryID    string
	StartedAt    time.Time
	EndedAt      time.Time
	RuntimeTicks int64
	Samples      []PlaybackSample
	// Previous is set when the session was aggregated before.
	Previous *WatchStatsMark
}

// WatchStatsBatch is one aggregation step: sessions to aggregate, the
// already aggregated sessions of the same users and items that overlap
// them in time, and the classification history of those users and items.
type WatchStatsBatch struct {
	Sessions []WatchStatsSession
	Prior    []WatchStatsSession
	// History is keyed by WatchPairKey.
	History map[string]WatchHistory
}

// WatchStatsDelta is what one batch adds to one daily row.
type WatchStatsDelta struct {
	Day             string
	UserID          string
	ItemID          string
	LibraryID       string
	EffectiveMillis int64
	Sessions        int
	Views           int
	FirstPlays      int
	Rewatches       int
	Completions     int
	// CompletionMilli sums the completion rate (per mille) of the counted
	// sessions; divided by Sessions it is the average completion rate.
	CompletionMilli int64
}

// WatchStatsOutcome is the result of one batch: daily deltas, the history
// of every user and item the batch touched and the mark of every session.
type WatchStatsOutcome struct {
	Daily   []WatchStatsDelta
	History map[string]WatchHistory
	Marks   map[string]WatchStatsMark
}

// WatchPairKey keys per user and item state.
func WatchPairKey(userID, itemID string) string { return userID + "\x00" + itemID }

// SplitWatchPairKey reverses WatchPairKey.
func SplitWatchPairKey(key string) (userID, itemID string) {
	userID, itemID, _ = strings.Cut(key, "\x00")
	return userID, itemID
}

// watchStatsSegments computes a session's credited segments, clipped to end
// before through when through is set.
func watchStatsSegments(s WatchStatsSession, through time.Time, rules WatchStatsRules) (WatchSessionStats, []WatchSegment, error) {
	samples := make([]WatchSample, 0, len(s.Samples))
	for _, x := range s.Samples {
		samples = append(samples, x.WatchSample())
	}
	stats, err := ComputeWatchSession(samples, TicksToDuration(s.RuntimeTicks), rules)
	if err != nil {
		return WatchSessionStats{}, nil, err
	}
	if through.IsZero() {
		return stats, stats.Segments, nil
	}
	clipped := make([]WatchSegment, 0, len(stats.Segments))
	for _, seg := range stats.Segments {
		if !seg.Start.Before(through) {
			continue
		}
		if seg.End.After(through) {
			seg.End = through
		}
		clipped = append(clipped, seg)
	}
	return stats, clipped, nil
}

// watchDayDurations splits segments at local midnight after merging them.
func watchDayDurations(segments []WatchSegment, loc *time.Location) map[string]time.Duration {
	out := map[string]time.Duration{}
	for _, seg := range MergeWatchSegments(segments) {
		for start := seg.Start; start.Before(seg.End); {
			local := start.In(loc)
			midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			end := seg.End
			if midnight.Before(end) {
				end = midnight
			}
			out[local.Format(time.DateOnly)] += end.Sub(start)
			start = end
		}
	}
	return out
}

// CompletionMilli is a completion rate in per mille.
func CompletionMilli(rate float64) int {
	if math.IsNaN(rate) || rate <= 0 {
		return 0
	}
	return int(math.Min(1000, math.Round(rate*1000)))
}

// AggregateWatchStats computes the outcome of one batch in loc. It is pure:
// storage reads the batch and writes the outcome in one transaction.
func AggregateWatchStats(batch WatchStatsBatch, rules WatchStatsRules, loc *time.Location) (WatchStatsOutcome, error) {
	if err := rules.Validate(); err != nil {
		return WatchStatsOutcome{}, err
	}
	if loc == nil {
		loc = time.UTC
	}
	sessions := append([]WatchStatsSession(nil), batch.Sessions...)
	sort.SliceStable(sessions, func(i, j int) bool {
		if !sessions[i].StartedAt.Equal(sessions[j].StartedAt) {
			return sessions[i].StartedAt.Before(sessions[j].StartedAt)
		}
		return sessions[i].ID < sessions[j].ID
	})
	inBatch := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		if !ValidID(s.ID) || !ValidID(s.UserID) || !ValidID(s.ItemID) || !ValidID(s.LibraryID) || s.EndedAt.IsZero() || inBatch[s.ID] {
			return WatchStatsOutcome{}, ErrInvalid
		}
		inBatch[s.ID] = true
	}
	type pairState struct {
		library  string
		prior    []WatchSegment
		combined []WatchSegment
	}
	pairs := map[string]*pairState{}
	pair := func(s WatchStatsSession) *pairState {
		k := WatchPairKey(s.UserID, s.ItemID)
		p := pairs[k]
		if p == nil {
			p = &pairState{library: s.LibraryID}
			pairs[k] = p
		}
		return p
	}
	for _, s := range batch.Prior {
		if inBatch[s.ID] || s.Previous == nil {
			continue
		}
		p := pairs[WatchPairKey(s.UserID, s.ItemID)]
		if p == nil {
			// Prior sessions matter only for pairs of the batch.
			if !batchHasPair(sessions, s.UserID, s.ItemID) {
				continue
			}
			p = pair(s)
		}
		_, segs, err := watchStatsSegments(s, s.Previous.Through, rules)
		if err != nil {
			return WatchStatsOutcome{}, err
		}
		p.prior = append(p.prior, segs...)
	}
	out := WatchStatsOutcome{History: map[string]WatchHistory{}, Marks: make(map[string]WatchStatsMark, len(sessions))}
	type dayKey struct{ day, pair string }
	rows := map[dayKey]*WatchStatsDelta{}
	row := func(day string, s WatchStatsSession) *WatchStatsDelta {
		k := dayKey{day, WatchPairKey(s.UserID, s.ItemID)}
		r := rows[k]
		if r == nil {
			r = &WatchStatsDelta{Day: day, UserID: s.UserID, ItemID: s.ItemID, LibraryID: s.LibraryID}
			rows[k] = r
		}
		return r
	}
	for _, s := range sessions {
		p := pair(s)
		stats, segs, err := watchStatsSegments(s, time.Time{}, rules)
		if err != nil {
			return WatchStatsOutcome{}, err
		}
		if s.Previous != nil {
			_, counted, err := watchStatsSegments(s, s.Previous.Through, rules)
			if err != nil {
				return WatchStatsOutcome{}, err
			}
			p.prior = append(p.prior, counted...)
		}
		p.combined = append(p.combined, segs...)

		key := WatchPairKey(s.UserID, s.ItemID)
		history, ok := out.History[key]
		if !ok {
			history = batch.History[key]
		}
		milli := CompletionMilli(stats.CompletionRate)
		mark := WatchStatsMark{Through: s.EndedAt, Completed: stats.Completed, CompletionMilli: milli, Play: WatchPlayNone}
		prev := s.Previous
		switch {
		case prev != nil && prev.Counted:
			// Counted before: keep its day and classification; only what
			// grew is added.
			mark.Counted, mark.Day, mark.Play = true, prev.Day, prev.Play
			mark.Completed = prev.Completed || stats.Completed
			mark.CompletionMilli = max(prev.CompletionMilli, milli)
			r := row(prev.Day, s)
			if mark.Completed && !prev.Completed {
				r.Completions++
				history.Completions++
				history.LastCompleted = true
			}
			r.CompletionMilli += int64(mark.CompletionMilli - prev.CompletionMilli)
		case stats.Counted:
			at := stats.StartedAt
			if len(stats.Segments) > 0 {
				at = stats.Segments[0].Start
			}
			if at.IsZero() {
				at = s.StartedAt
			}
			kind, next := ClassifyWatchPlay(history, stats)
			history = next
			mark.Counted, mark.Day, mark.Play = true, at.In(loc).Format(time.DateOnly), kind
			r := row(mark.Day, s)
			r.Sessions++
			switch kind {
			case WatchPlayFirst:
				r.FirstPlays++
				r.Views++
			case WatchPlayRewatch:
				r.Rewatches++
				r.Views++
			}
			if stats.Completed {
				r.Completions++
			}
			r.CompletionMilli += int64(milli)
		}
		out.History[key] = history
		out.Marks[s.ID] = mark
	}
	for key, p := range pairs {
		before := watchDayDurations(p.prior, loc)
		after := watchDayDurations(append(append([]WatchSegment(nil), p.prior...), p.combined...), loc)
		userID, itemID := SplitWatchPairKey(key)
		for day, total := range after {
			delta := (total - before[day]).Milliseconds()
			if delta <= 0 {
				continue
			}
			row(day, WatchStatsSession{UserID: userID, ItemID: itemID, LibraryID: p.library}).EffectiveMillis += delta
		}
	}
	out.Daily = make([]WatchStatsDelta, 0, len(rows))
	for _, r := range rows {
		if r.EffectiveMillis == 0 && r.Sessions == 0 && r.Completions == 0 && r.CompletionMilli == 0 {
			continue
		}
		out.Daily = append(out.Daily, *r)
	}
	sort.Slice(out.Daily, func(i, j int) bool {
		a, b := out.Daily[i], out.Daily[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		return a.ItemID < b.ItemID
	})
	return out, nil
}

func batchHasPair(sessions []WatchStatsSession, userID, itemID string) bool {
	for _, s := range sessions {
		if s.UserID == userID && s.ItemID == itemID {
			return true
		}
	}
	return false
}

// Reporting queries.

const (
	// WatchStatsTopMax bounds Top N lists.
	WatchStatsTopMax = 100
	// WatchStatsRangeMaxDays bounds a report or export range.
	WatchStatsRangeMaxDays = 3660
	// WatchStatsDailyMaxDays bounds a range reported per day.
	WatchStatsDailyMaxDays = 400
	// WatchStatsDefaultDays is the range reported without from and to.
	WatchStatsDefaultDays = 30
	// WatchStatsLibrariesMax bounds the per library breakdown.
	WatchStatsLibrariesMax = 200
)

// WatchStatsRequest is a statistics request as a client sends it; empty
// members take the defaults.
type WatchStatsRequest struct {
	// SubjectID names the user whose statistics are read; empty reads every
	// user (administrators only).
	SubjectID string
	From, To  string // YYYY-MM-DD in the reporting time zone, inclusive
	Period    string // day, week, month or year
	Top       int    // 0 for the default
}

// WatchStatsQuery is a validated request.
type WatchStatsQuery struct {
	SubjectID string
	// From and To are local dates (midnight in UTC carrying the date only).
	From, To  time.Time
	Period    WatchPeriod
	Top       int
	WeekStart time.Weekday
}

// ParseWatchStatsDate parses a YYYY-MM-DD date.
func ParseWatchStatsDate(value string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, value)
	if err != nil || t.Format(time.DateOnly) != value {
		return time.Time{}, ErrInvalid
	}
	return t, nil
}

// ResolveWatchStatsRequest validates a request against today (the current
// local date) and fills the defaults: the last WatchStatsDefaultDays days
// ending today, per day, top 10.
func ResolveWatchStatsRequest(r WatchStatsRequest, today time.Time, weekStart time.Weekday) (WatchStatsQuery, error) {
	q := WatchStatsQuery{SubjectID: r.SubjectID, Period: WatchPeriod(r.Period), Top: r.Top, WeekStart: weekStart}
	if q.SubjectID != "" && !ValidID(q.SubjectID) {
		return WatchStatsQuery{}, ErrInvalid
	}
	if weekStart != time.Monday && weekStart != time.Sunday {
		return WatchStatsQuery{}, ErrInvalid
	}
	if q.Period == "" {
		q.Period = WatchPeriodDay
	}
	switch q.Period {
	case WatchPeriodDay, WatchPeriodWeek, WatchPeriodMonth, WatchPeriodYear:
	default:
		return WatchStatsQuery{}, ErrInvalid
	}
	if q.Top == 0 {
		q.Top = 10
	}
	if q.Top < 1 || q.Top > WatchStatsTopMax {
		return WatchStatsQuery{}, ErrInvalid
	}
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	var err error
	q.To = today
	if r.To != "" {
		if q.To, err = ParseWatchStatsDate(r.To); err != nil {
			return WatchStatsQuery{}, err
		}
	}
	q.From = q.To.AddDate(0, 0, -(WatchStatsDefaultDays - 1))
	if r.From != "" {
		if q.From, err = ParseWatchStatsDate(r.From); err != nil {
			return WatchStatsQuery{}, err
		}
	}
	days := int(q.To.Sub(q.From).Hours()/24) + 1
	if days < 1 || days > WatchStatsRangeMaxDays || q.Period == WatchPeriodDay && days > WatchStatsDailyMaxDays {
		return WatchStatsQuery{}, ErrInvalid
	}
	return q, nil
}

// WatchStatsBucket returns the first day of the period containing day,
// with weeks starting on weekStart.
func WatchStatsBucket(day time.Time, period WatchPeriod, weekStart time.Weekday) time.Time {
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case WatchPeriodWeek:
		return day.AddDate(0, 0, -((int(day.Weekday()) - int(weekStart) + 7) % 7))
	case WatchPeriodMonth:
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	case WatchPeriodYear:
		return time.Date(day.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return day
}

// WatchStatsTotals sums daily rows. EffectiveSeconds is the effective time
// rounded down; CompletionRate is the average completion of the counted
// sessions (0 without sessions).
type WatchStatsTotals struct {
	EffectiveSeconds int64   `json:"effectiveSeconds"`
	Sessions         int64   `json:"sessions"`
	Views            int64   `json:"views"`
	FirstPlays       int64   `json:"firstPlays"`
	Rewatches        int64   `json:"rewatches"`
	Completions      int64   `json:"completions"`
	CompletionRate   float64 `json:"completionRate"`
}

// WatchStatsSums are the raw column sums storage returns.
type WatchStatsSums struct {
	EffectiveMillis int64
	Sessions        int64
	Views           int64
	FirstPlays      int64
	Rewatches       int64
	Completions     int64
	CompletionMilli int64
}

// Totals converts raw sums.
func (s WatchStatsSums) Totals() WatchStatsTotals {
	t := WatchStatsTotals{EffectiveSeconds: s.EffectiveMillis / 1000, Sessions: s.Sessions, Views: s.Views, FirstPlays: s.FirstPlays,
		Rewatches: s.Rewatches, Completions: s.Completions}
	if s.Sessions > 0 {
		t.CompletionRate = math.Round(float64(s.CompletionMilli)/float64(s.Sessions)) / 1000
	}
	return t
}

type WatchStatsPeriodRow struct {
	Start string `json:"start"`
	WatchStatsTotals
}

type WatchStatsItemRow struct {
	ItemID    string `json:"itemId"`
	LibraryID string `json:"libraryId"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	WatchStatsTotals
	// UserData is the subject's current progress on the item, for a
	// single user's statistics.
	UserData *UserItemData `json:"userData,omitempty"`
}

type WatchStatsLibraryRow struct {
	LibraryID string `json:"libraryId"`
	Name      string `json:"name"`
	WatchStatsTotals
}

type WatchStatsKindRow struct {
	Kind string `json:"kind"`
	WatchStatsTotals
}

type WatchStatsUserRow struct {
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	WatchStatsTotals
}

// WatchStatsReport is one statistics response. Periods without data are
// omitted.
type WatchStatsReport struct {
	UserID    string                 `json:"userId,omitempty"`
	From      string                 `json:"from"`
	To        string                 `json:"to"`
	Period    WatchPeriod            `json:"period"`
	TimeZone  string                 `json:"timeZone"`
	WeekStart string                 `json:"weekStart"`
	Totals    WatchStatsTotals       `json:"totals"`
	Periods   []WatchStatsPeriodRow  `json:"periods"`
	TopItems  []WatchStatsItemRow    `json:"topItems"`
	Libraries []WatchStatsLibraryRow `json:"libraries"`
	Kinds     []WatchStatsKindRow    `json:"kinds"`
	// TopUsers is set for statistics of every user.
	TopUsers []WatchStatsUserRow `json:"topUsers,omitempty"`
}

// Export.

const (
	WatchStatsExportCSV    = "csv"
	WatchStatsExportNDJSON = "ndjson"
)

// WatchStatsExportQuery is a validated export request.
type WatchStatsExportQuery struct {
	SubjectID string
	From, To  time.Time
	Format    string
	// Limit is the most rows the export may hold; a range with more rows
	// is refused.
	Limit int
}

// WatchStatsExportRow is one exported daily row.
type WatchStatsExportRow struct {
	Day             string  `json:"day"`
	UserID          string  `json:"userId"`
	UserName        string  `json:"userName"`
	ItemID          string  `json:"itemId"`
	LibraryID       string  `json:"libraryId"`
	Kind            string  `json:"kind"`
	Title           string  `json:"title"`
	EffectiveMillis int64   `json:"effectiveMillis"`
	Sessions        int64   `json:"sessions"`
	Views           int64   `json:"views"`
	FirstPlays      int64   `json:"firstPlays"`
	Rewatches       int64   `json:"rewatches"`
	Completions     int64   `json:"completions"`
	CompletionRate  float64 `json:"completionRate"`
}
