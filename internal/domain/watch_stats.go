package domain

import (
	"math"
	"sort"
	"time"
)

// G23.3 watch statistics. These pure functions define the counting rules;
// docs/watch-statistics.md restates them for operators and API consumers, and
// the SQL roll-ups (G23.5) aggregate the per-day rows produced here.
//
// Input is the ordered sample stream of one playback session as the server
// received it. Times are server receive times, positions are media
// positions. Rules, in order of application to each interval between two
// consecutive valid samples (the "anchor" and the next sample):
//
//  1. Only intervals that start while playing count. Start, Resume and
//     Progress without Paused set playing; Pause and Progress with Paused
//     clear it; Seek keeps it; Stop and Fail end the session and later
//     samples are ignored. So pauses never count.
//  2. Clock rollback: if the next sample's time is before the anchor's, the
//     interval counts nothing and the next sample becomes the anchor.
//  3. Seek: for a Seek sample with a known From position, the interval is
//     judged up to From (the part actually played before the jump); the new
//     anchor is the post-seek Position. Without From the interval is judged
//     up to Position like any other.
//  4. Discontinuity: a media advance below -PositionTolerance (backward
//     jump) or above wall*MaxPlaybackRate + PositionTolerance (forward jump,
//     fast-forward, chapter skip) counts nothing for that interval. Small
//     negative advances within tolerance count nothing and are not jumps.
//  5. Credit: the interval credits min(wall, media advance). This removes
//     stalls and idle time (wall exceeds advance) and stops faster-than-1x
//     playback from inflating time (advance exceeds wall).
//  6. Report gap: if wall exceeds MaxReportGap the credit is capped at
//     MaxReportGap; the session was probably disconnected and only the
//     first stretch is trusted.
//  7. Overlaps: credited wall stretches [anchorAt, anchorAt+credit) are
//     merged before summing, so duplicate or rolled-back samples cannot
//     count the same wall time twice. AggregateWatchDaily also merges
//     across sessions of the same user and item (two devices reporting the
//     same viewing).
//
// Media coverage is the union of [anchorPos, anchorPos+advance] for
// credited intervals (only the credited length when capped by rule 6),
// clipped to the runtime. CompletionRate = coverage / runtime; a session is
// Completed when CompletionRate >= CompletedRatio. Re-watching a part
// adds effective time but not coverage.

type WatchSampleKind string

const (
	WatchSampleStart    WatchSampleKind = "start"
	WatchSampleProgress WatchSampleKind = "progress"
	WatchSamplePause    WatchSampleKind = "pause"
	WatchSampleResume   WatchSampleKind = "resume"
	WatchSampleSeek     WatchSampleKind = "seek"
	WatchSampleStop     WatchSampleKind = "stop"
	WatchSampleFail     WatchSampleKind = "fail"
)

func (k WatchSampleKind) Valid() bool {
	switch k {
	case WatchSampleStart, WatchSampleProgress, WatchSamplePause, WatchSampleResume, WatchSampleSeek, WatchSampleStop, WatchSampleFail:
		return true
	}
	return false
}

// WatchSample is one playback report.
type WatchSample struct {
	At       time.Time
	Kind     WatchSampleKind
	Position time.Duration
	// Paused is meaningful for Progress only (clients report it inline).
	Paused bool
	// From is the position before a Seek, valid when FromKnown is set.
	From      time.Duration
	FromKnown bool
}

// WatchStatsRules are the configurable G23.3 parameters.
type WatchStatsRules struct {
	MaxReportGap      time.Duration
	MaxPlaybackRate   float64
	PositionTolerance time.Duration
	// CompletedRatio of the runtime covered marks a session completed.
	CompletedRatio float64
	// A session counts as a view when its effective time reaches
	// min(MinViewDuration, MinViewRatio*runtime) (MinViewDuration alone
	// when the runtime is unknown or MinViewRatio is 0), or it completed.
	MinViewDuration time.Duration
	MinViewRatio    float64
	// Positions below MinResumePosition do not leave a resume point.
	MinResumePosition time.Duration
}

func DefaultWatchStatsRules() WatchStatsRules {
	return WatchStatsRules{
		MaxReportGap:      5 * time.Minute,
		MaxPlaybackRate:   2,
		PositionTolerance: 3 * time.Second,
		CompletedRatio:    0.9,
		MinViewDuration:   2 * time.Minute,
		MinViewRatio:      0.1,
		MinResumePosition: 30 * time.Second,
	}
}

func (r WatchStatsRules) Validate() error {
	switch {
	case r.MaxReportGap < 10*time.Second || r.MaxReportGap > time.Hour,
		math.IsNaN(r.MaxPlaybackRate) || r.MaxPlaybackRate < 1 || r.MaxPlaybackRate > 4,
		r.PositionTolerance < 0 || r.PositionTolerance > 30*time.Second,
		math.IsNaN(r.CompletedRatio) || r.CompletedRatio < 0.5 || r.CompletedRatio > 1,
		r.MinViewDuration < 0 || r.MinViewDuration > 30*time.Minute,
		math.IsNaN(r.MinViewRatio) || r.MinViewRatio < 0 || r.MinViewRatio > 1,
		r.MinResumePosition < 0 || r.MinResumePosition > 10*time.Minute:
		return ErrInvalid
	}
	return nil
}

// WatchSegment is a credited wall-clock stretch [Start, End).
type WatchSegment struct {
	Start time.Time
	End   time.Time
}

func (s WatchSegment) Duration() time.Duration { return s.End.Sub(s.Start) }

// WatchAnomalies counts samples and intervals the rules discarded or
// limited, for diagnostics and tests.
type WatchAnomalies struct {
	Ignored        int // malformed samples and samples after Stop/Fail
	ClockRollbacks int
	Jumps          int
	CappedGaps     int
}

type WatchSessionStats struct {
	StartedAt      time.Time // first valid sample
	Segments       []WatchSegment
	Effective      time.Duration
	Covered        time.Duration
	CompletionRate float64
	Completed      bool
	Counted        bool
	LastPosition   time.Duration
	ResumePosition time.Duration
	Anomalies      WatchAnomalies
}

type mediaSpan struct{ start, end time.Duration }

// ComputeWatchSession applies the G23.3 rules to one session. runtime <= 0
// means unknown: coverage is not clipped and completion stays 0.
func ComputeWatchSession(samples []WatchSample, runtime time.Duration, rules WatchStatsRules) (WatchSessionStats, error) {
	if err := rules.Validate(); err != nil {
		return WatchSessionStats{}, err
	}
	var (
		out              WatchSessionStats
		started, ended   bool
		playing          bool
		anchorAt         time.Time
		anchorPos        time.Duration
		segments         []WatchSegment
		spans            []mediaSpan
		tol              = rules.PositionTolerance
		maxRate          = rules.MaxPlaybackRate
		credit           func(wall, advance time.Duration)
		applyStateChange = func(s WatchSample) {
			switch s.Kind {
			case WatchSampleStart, WatchSampleResume:
				playing = true
			case WatchSamplePause:
				playing = false
			case WatchSampleProgress:
				playing = !s.Paused
			case WatchSampleStop, WatchSampleFail:
				playing, ended = false, true
			}
		}
	)
	credit = func(wall, advance time.Duration) {
		if advance < -tol || float64(advance) > float64(wall)*maxRate+float64(tol) {
			out.Anomalies.Jumps++
			return
		}
		if advance <= 0 || wall <= 0 {
			return
		}
		c, covered := min(wall, advance), advance
		if wall > rules.MaxReportGap {
			out.Anomalies.CappedGaps++
			c = min(c, rules.MaxReportGap)
			covered = c
		}
		segments = append(segments, WatchSegment{Start: anchorAt, End: anchorAt.Add(c)})
		spans = append(spans, mediaSpan{start: anchorPos, end: anchorPos + covered})
	}
	for _, s := range samples {
		if s.At.IsZero() || !s.Kind.Valid() || s.Position < 0 || (s.FromKnown && s.From < 0) || ended {
			out.Anomalies.Ignored++
			continue
		}
		if !started {
			started = true
			out.StartedAt = s.At
			playing = s.Kind != WatchSamplePause && !(s.Kind == WatchSampleProgress && s.Paused)
			if s.Kind == WatchSampleStop || s.Kind == WatchSampleFail {
				playing, ended = false, true
			}
			anchorAt, anchorPos, out.LastPosition = s.At, s.Position, s.Position
			continue
		}
		wall := s.At.Sub(anchorAt)
		if wall < 0 {
			out.Anomalies.ClockRollbacks++
		} else if playing {
			end := s.Position
			if s.Kind == WatchSampleSeek && s.FromKnown {
				end = s.From
			}
			credit(wall, end-anchorPos)
		}
		applyStateChange(s)
		anchorAt, anchorPos, out.LastPosition = s.At, s.Position, s.Position
	}

	out.Segments = MergeWatchSegments(segments)
	for _, seg := range out.Segments {
		out.Effective += seg.Duration()
	}
	out.Covered = mediaCoverage(spans, runtime)
	if runtime > 0 {
		out.CompletionRate = min(1, float64(out.Covered)/float64(runtime))
		out.Completed = out.CompletionRate >= rules.CompletedRatio
	}
	threshold := rules.MinViewDuration
	if runtime > 0 && rules.MinViewRatio > 0 {
		threshold = min(threshold, time.Duration(float64(runtime)*rules.MinViewRatio))
	}
	out.Counted = out.Completed || (out.Effective > 0 && out.Effective >= threshold)
	out.ResumePosition = out.LastPosition
	if out.Completed || out.LastPosition < rules.MinResumePosition ||
		(runtime > 0 && float64(out.LastPosition) >= float64(runtime)*rules.CompletedRatio) {
		out.ResumePosition = 0
	}
	return out, nil
}

// MergeWatchSegments returns the sorted union of segments; empty segments
// are dropped and touching segments are joined.
func MergeWatchSegments(segments []WatchSegment) []WatchSegment {
	sorted := make([]WatchSegment, 0, len(segments))
	for _, s := range segments {
		if s.End.After(s.Start) {
			sorted = append(sorted, s)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	var out []WatchSegment
	for _, s := range sorted {
		if n := len(out); n > 0 && !s.Start.After(out[n-1].End) {
			if s.End.After(out[n-1].End) {
				out[n-1].End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func mediaCoverage(spans []mediaSpan, runtime time.Duration) time.Duration {
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var total, curStart, curEnd time.Duration
	open := false
	for _, s := range spans {
		if runtime > 0 {
			s.start, s.end = min(s.start, runtime), min(s.end, runtime)
		}
		if s.end <= s.start {
			continue
		}
		if open && s.start <= curEnd {
			curEnd = max(curEnd, s.end)
			continue
		}
		if open {
			total += curEnd - curStart
		}
		curStart, curEnd, open = s.start, s.end, true
	}
	if open {
		total += curEnd - curStart
	}
	return total
}

// WatchPlayKind classifies a session against the user's history of the
// item (G23.3 first play / re-watch).
type WatchPlayKind string

const (
	// WatchPlayNone: the session did not reach the view threshold.
	WatchPlayNone WatchPlayKind = "none"
	// WatchPlayFirst: the user's first counted session of the item.
	WatchPlayFirst WatchPlayKind = "first"
	// WatchPlayContinue: continues an unfinished watch-through (first play
	// or re-watch) and does not add a view.
	WatchPlayContinue WatchPlayKind = "continue"
	// WatchPlayRewatch: a counted session after a completed watch-through.
	WatchPlayRewatch WatchPlayKind = "rewatch"
)

// WatchHistory is the per (user, item) running state the classifier needs.
type WatchHistory struct {
	// Views counts watch-throughs: first plays plus re-watches.
	Views       int
	Completions int
	// LastCompleted reports whether the latest counted session completed.
	LastCompleted bool
}

// ClassifyWatchPlay returns the kind of s given prior history and the
// history after it. Sessions must be applied in start order.
func ClassifyWatchPlay(prior WatchHistory, s WatchSessionStats) (WatchPlayKind, WatchHistory) {
	if !s.Counted {
		return WatchPlayNone, prior
	}
	next := prior
	kind := WatchPlayContinue
	switch {
	case prior.Views == 0:
		kind = WatchPlayFirst
		next.Views++
	case prior.LastCompleted:
		kind = WatchPlayRewatch
		next.Views++
	}
	if s.Completed {
		next.Completions++
	}
	next.LastCompleted = s.Completed
	return kind, next
}

// WatchSessionRecord is one computed session with its dimensions.
type WatchSessionRecord struct {
	UserID    string
	ItemID    string
	LibraryID string
	ItemKind  string
	Stats     WatchSessionStats
	Play      WatchPlayKind
}

// WatchDaily is one row of the daily roll-up; weeks, months, years and
// library/kind/Top-N views are sums over these rows (G23.5).
type WatchDaily struct {
	Day         string // YYYY-MM-DD in the reporting location
	UserID      string
	ItemID      string
	LibraryID   string
	ItemKind    string
	Effective   time.Duration
	Sessions    int // counted sessions
	Views       int // first plays + re-watches
	FirstPlays  int
	Rewatches   int
	Completions int
}

// AggregateWatchDaily builds daily rows in loc. Effective time is split at
// local midnight and merged across sessions of the same user and item, so
// overlapping reports from two devices count once. Counters (sessions,
// views, completions) go to the local day of the session's first credited
// segment, or of its first sample when nothing was credited.
func AggregateWatchDaily(records []WatchSessionRecord, loc *time.Location) []WatchDaily {
	if loc == nil {
		loc = time.UTC
	}
	type pair struct{ user, item string }
	type dayKey struct {
		day string
		pair
	}
	rows := map[dayKey]*WatchDaily{}
	dims := map[pair]WatchSessionRecord{}
	segments := map[pair][]WatchSegment{}
	row := func(day string, p pair) *WatchDaily {
		k := dayKey{day, p}
		if r := rows[k]; r != nil {
			return r
		}
		d := dims[p]
		r := &WatchDaily{Day: day, UserID: p.user, ItemID: p.item, LibraryID: d.LibraryID, ItemKind: d.ItemKind}
		rows[k] = r
		return r
	}
	for _, rec := range records {
		p := pair{rec.UserID, rec.ItemID}
		if _, ok := dims[p]; !ok {
			dims[p] = rec
		}
		segments[p] = append(segments[p], rec.Stats.Segments...)
		if !rec.Stats.Counted {
			continue
		}
		at := rec.Stats.StartedAt
		if len(rec.Stats.Segments) > 0 {
			at = rec.Stats.Segments[0].Start
		}
		r := row(at.In(loc).Format(time.DateOnly), p)
		r.Sessions++
		switch rec.Play {
		case WatchPlayFirst:
			r.FirstPlays++
			r.Views++
		case WatchPlayRewatch:
			r.Rewatches++
			r.Views++
		}
		if rec.Stats.Completed {
			r.Completions++
		}
	}
	for p, segs := range segments {
		for _, seg := range MergeWatchSegments(segs) {
			for start := seg.Start; start.Before(seg.End); {
				local := start.In(loc)
				midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
				end := seg.End
				if midnight.Before(end) {
					end = midnight
				}
				row(local.Format(time.DateOnly), p).Effective += end.Sub(start)
				start = end
			}
		}
	}
	out := make([]WatchDaily, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		return a.ItemID < b.ItemID
	})
	return out
}

// WatchPeriod is a reporting granularity.
type WatchPeriod string

const (
	WatchPeriodDay   WatchPeriod = "day"
	WatchPeriodWeek  WatchPeriod = "week" // ISO 8601: weeks start on Monday
	WatchPeriodMonth WatchPeriod = "month"
	WatchPeriodYear  WatchPeriod = "year"
)

// WatchPeriodStart returns local midnight starting the period containing t.
func WatchPeriodStart(t time.Time, period WatchPeriod, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	switch period {
	case WatchPeriodDay:
		return day, nil
	case WatchPeriodWeek:
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset), nil
	case WatchPeriodMonth:
		return time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc), nil
	case WatchPeriodYear:
		return time.Date(l.Year(), 1, 1, 0, 0, 0, 0, loc), nil
	}
	return time.Time{}, ErrInvalid
}
