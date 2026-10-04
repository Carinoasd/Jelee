package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	rollUser    = "00000000-0000-4000-8000-0000000000a1"
	rollItem    = "00000000-0000-4000-8000-0000000000b1"
	rollItem2   = "00000000-0000-4000-8000-0000000000b2"
	rollLibrary = "00000000-0000-4000-8000-0000000000c1"
)

// rollSample is a stored sample at base+sec with position pos seconds.
func rollSample(base time.Time, kind WatchSampleKind, sec, pos float64) PlaybackSample {
	return PlaybackSample{At: base.Add(time.Duration(sec * float64(time.Second))), Kind: kind, PositionTicks: DurationToTicks(time.Duration(pos * float64(time.Second)))}
}

// rollSession builds a session of rollUser on item whose samples are pairs
// of (seconds after base, position seconds); the first is a start, the last
// a stop, the others progress.
func rollSession(id, item string, base time.Time, runtime float64, points ...[2]float64) WatchStatsSession {
	s := WatchStatsSession{ID: id, UserID: rollUser, ItemID: item, LibraryID: rollLibrary, StartedAt: base,
		RuntimeTicks: DurationToTicks(time.Duration(runtime * float64(time.Second)))}
	for i, p := range points {
		kind := WatchSampleProgress
		switch i {
		case 0:
			kind = WatchSampleStart
		case len(points) - 1:
			kind = WatchSampleStop
		}
		sample := rollSample(base, kind, p[0], p[1])
		sample.Seq = i
		s.Samples = append(s.Samples, sample)
		s.EndedAt = sample.At
	}
	return s
}

// rollLinear is a session playing from position from for seconds, reporting
// every minute (well inside MaxReportGap).
func rollLinear(id, item string, base time.Time, runtime, from, seconds float64) WatchStatsSession {
	var points [][2]float64
	for t := 0.0; t < seconds; t += 60 {
		points = append(points, [2]float64{t, from + t})
	}
	points = append(points, [2]float64{seconds, from + seconds})
	return rollSession(id, item, base, runtime, points...)
}

func rollID(n int) string {
	return "00000000-0000-4000-8000-" + string([]byte{'0', '0', '0', '0', '0', '0', '0', '0', '0', '0', byte('0' + n/10), byte('0' + n%10)})
}

func rollOne(t *testing.T, batch WatchStatsBatch, loc *time.Location) WatchStatsOutcome {
	t.Helper()
	out, err := AggregateWatchStats(batch, DefaultWatchStatsRules(), loc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rollDays(out WatchStatsOutcome) map[string]WatchStatsDelta {
	days := map[string]WatchStatsDelta{}
	for _, d := range out.Daily {
		days[d.Day+" "+d.ItemID] = d
	}
	return days
}

// Fast-forward, pauses and stalls never add effective time; the counters
// follow docs/watch-statistics.md.
func TestAggregateWatchStatsCountingRules(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	day := "2026-03-10 "
	cases := []struct {
		name      string
		session   WatchStatsSession
		effective int64
		sessions  int
		completed int
		milli     int64
	}{
		{
			// 0-300 s played, then a jump of 20 minutes in 10 s, then 300 s.
			name:      "fast-forward excluded",
			session:   rollSession(rollID(1), rollItem, base, 3600, [2]float64{0, 0}, [2]float64{300, 300}, [2]float64{310, 1500}, [2]float64{610, 1800}),
			effective: 600_000, sessions: 1, milli: 167, // coverage 600 s of 3600 s
		},
		{
			// A jump just inside the tolerance (2x wall + 3 s) still counts the wall time only.
			name:      "fast playback capped at wall time",
			session:   rollSession(rollID(2), rollItem, base, 3600, [2]float64{0, 0}, [2]float64{100, 203}),
			effective: 100_000, sessions: 0, milli: 0, // 100 s < 2 min view threshold
		},
		{
			name: "pause excluded",
			session: func() WatchStatsSession {
				s := rollSession(rollID(3), rollItem, base, 3600, [2]float64{0, 0}, [2]float64{60, 60}, [2]float64{1000, 60}, [2]float64{1060, 120}, [2]float64{1180, 240})
				s.Samples[1].Kind, s.Samples[1].Paused = WatchSamplePause, true
				s.Samples[2].Kind = WatchSampleResume
				return s
			}(),
			effective: 240_000, sessions: 1, milli: 67,
		},
		{
			// Idle: the client keeps reporting the same position for ten minutes.
			name:      "stall credits media advance only",
			session:   rollSession(rollID(4), rollItem, base, 3600, [2]float64{0, 0}, [2]float64{600, 30}, [2]float64{900, 330}),
			effective: 330_000, sessions: 1, milli: 92,
		},
		{
			name:      "report gap capped",
			session:   rollSession(rollID(5), rollItem, base, 0, [2]float64{0, 0}, [2]float64{3600, 3600}),
			effective: 300_000, sessions: 1, milli: 0, // unknown runtime: never completed
		},
		{
			name:      "completed",
			session:   rollSession(rollID(6), rollItem, base, 600, [2]float64{0, 0}, [2]float64{300, 300}, [2]float64{560, 560}),
			effective: 560_000, sessions: 1, completed: 1, milli: 933,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{c.session}}, time.UTC)
			d := rollDays(out)[day+rollItem]
			if d.EffectiveMillis != c.effective || d.Sessions != c.sessions || d.Completions != c.completed || d.CompletionMilli != c.milli {
				t.Fatalf("got %+v", d)
			}
			mark := out.Marks[c.session.ID]
			if !mark.Through.Equal(c.session.EndedAt) || mark.Counted != (c.sessions == 1) || mark.Completed != (c.completed == 1) {
				t.Fatalf("mark %+v", mark)
			}
			if c.sessions == 1 && (d.Views != 1 || d.FirstPlays != 1 || mark.Play != WatchPlayFirst || mark.Day != "2026-03-10") {
				t.Fatalf("first play not recorded: %+v %+v", d, mark)
			}
		})
	}
}

// First play, continuation and re-watch, within one batch and across
// batches through the stored history.
func TestAggregateWatchStatsRewatch(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	first := rollLinear(rollID(1), rollItem, base, 3600, 0, 3300)                             // completed: first play
	again := rollLinear(rollID(2), rollItem, base.Add(24*time.Hour), 3600, 0, 600)            // after a completion: re-watch
	short := rollLinear(rollID(3), rollItem, base.Add(48*time.Hour), 3600, 600, 60)           // below the view threshold
	finish := rollLinear(rollID(4), rollItem, base.Add(72*time.Hour), 3600, 0, 3300)          // re-watch unfinished: continue
	out := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{again, first}}, time.UTC) // start order is applied
	days := rollDays(out)
	d1, d2 := days["2026-03-10 "+rollItem], days["2026-03-11 "+rollItem]
	if d1.Sessions != 1 || d1.Views != 1 || d1.FirstPlays != 1 || d1.Rewatches != 0 || d1.Completions != 1 || d1.EffectiveMillis != 3_300_000 || d1.CompletionMilli != 917 {
		t.Fatalf("day 1 %+v", d1)
	}
	if d2.Sessions != 1 || d2.Views != 1 || d2.Rewatches != 1 || d2.FirstPlays != 0 || d2.Completions != 0 || d2.EffectiveMillis != 600_000 {
		t.Fatalf("day 2 %+v", d2)
	}
	if out.Marks[first.ID].Play != WatchPlayFirst || out.Marks[again.ID].Play != WatchPlayRewatch {
		t.Fatalf("classification %+v", out.Marks)
	}
	h := out.History[WatchPairKey(rollUser, rollItem)]
	if h.Views != 2 || h.Completions != 1 || h.LastCompleted {
		t.Fatalf("history %+v", h)
	}
	// The next batch continues from the stored history.
	out = rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{short, finish}, History: map[string]WatchHistory{WatchPairKey(rollUser, rollItem): h}}, time.UTC)
	days = rollDays(out)
	if s := days["2026-03-12 "+rollItem]; s.Sessions != 0 || s.EffectiveMillis != 60_000 || out.Marks[short.ID].Counted || out.Marks[short.ID].Play != WatchPlayNone {
		t.Fatalf("short session %+v", s)
	}
	if f := days["2026-03-13 "+rollItem]; f.Sessions != 1 || f.Views != 0 || f.Completions != 1 || out.Marks[finish.ID].Play != WatchPlayContinue {
		t.Fatalf("finishing session %+v", f)
	}
	if h = out.History[WatchPairKey(rollUser, rollItem)]; h.Views != 2 || h.Completions != 2 || !h.LastCompleted {
		t.Fatalf("history after %+v", h)
	}
}

// Effective time is cut at local midnight of the reporting zone; counters
// go to the day of the first credited segment.
func TestAggregateWatchStatsDaysAndTimeZones(t *testing.T) {
	// 23:50 to 00:10 UTC.
	utc := time.Date(2026, 3, 10, 23, 50, 0, 0, time.UTC)
	s := rollLinear(rollID(1), rollItem, utc, 3600, 0, 1200)
	days := rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{s}}, time.UTC))
	if a, b := days["2026-03-10 "+rollItem], days["2026-03-11 "+rollItem]; a.EffectiveMillis != 600_000 || b.EffectiveMillis != 600_000 || a.Sessions != 1 || b.Sessions != 0 {
		t.Fatalf("UTC split %+v %+v", a, b)
	}
	// The same wall time is a single day in Taipei (07:50 to 08:10).
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Fatal(err)
	}
	days = rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{s}}, taipei))
	if d := days["2026-03-11 "+rollItem]; len(days) != 1 || d.EffectiveMillis != 1_200_000 || d.Sessions != 1 {
		t.Fatalf("Taipei %+v", days)
	}
	// 15:50 to 16:10 UTC is 23:50 to 00:10 in Taipei.
	s = rollLinear(rollID(2), rollItem, time.Date(2026, 3, 10, 15, 50, 0, 0, time.UTC), 3600, 0, 1200)
	days = rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{s}}, taipei))
	if a, b := days["2026-03-10 "+rollItem], days["2026-03-11 "+rollItem]; a.EffectiveMillis != 600_000 || b.EffectiveMillis != 600_000 || a.Sessions != 1 {
		t.Fatalf("Taipei split %+v %+v", a, b)
	}
	// The night daylight saving time starts is still cut at local midnight.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	s = rollLinear(rollID(3), rollItem, time.Date(2026, 3, 8, 4, 50, 0, 0, time.UTC), 3600, 0, 1200) // 23:50 EST
	days = rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{s}}, ny))
	if a, b := days["2026-03-07 "+rollItem], days["2026-03-08 "+rollItem]; a.EffectiveMillis != 600_000 || b.EffectiveMillis != 600_000 {
		t.Fatalf("New York split %+v %+v", a, b)
	}
}

// Two devices reporting the same viewing count once, in one batch or in
// two; a rejoined session adds only what it played after its first end.
func TestAggregateWatchStatsUnionAndReaggregation(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	a := rollLinear(rollID(1), rollItem, base, 3600, 0, 600)
	b := rollLinear(rollID(2), rollItem, base.Add(300*time.Second), 3600, 300, 600)
	together := rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{a, b}}, time.UTC))["2026-03-10 "+rollItem]
	if together.EffectiveMillis != 900_000 || together.Sessions != 2 || together.Views != 1 {
		t.Fatalf("one batch %+v", together)
	}
	first := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{a}}, time.UTC)
	priorA := a
	markA := first.Marks[a.ID]
	priorA.Previous = &markA
	second := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{b}, Prior: []WatchStatsSession{priorA}, History: first.History}, time.UTC)
	d1, d2 := rollDays(first)["2026-03-10 "+rollItem], rollDays(second)["2026-03-10 "+rollItem]
	if d1.EffectiveMillis+d2.EffectiveMillis != together.EffectiveMillis || d2.EffectiveMillis != 300_000 || d2.Views != 0 || second.Marks[b.ID].Play != WatchPlayContinue {
		t.Fatalf("two batches %+v %+v", d1, d2)
	}
	// Prior sessions of another item do not reduce anything.
	other := priorA
	other.ItemID = rollItem2
	if d := rollDays(rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{b}, Prior: []WatchStatsSession{other}}, time.UTC))["2026-03-10 "+rollItem]; d.EffectiveMillis != 600_000 {
		t.Fatalf("unrelated prior %+v", d)
	}

	// Rejoined: the session timed out at 600 s and was aggregated; the
	// client came back 20 minutes later and played to the end.
	timedOut := rollLinear(rollID(3), rollItem, base, 3600, 0, 600)
	timedOut.Samples[len(timedOut.Samples)-1].Kind = WatchSampleProgress
	once := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{timedOut}}, time.UTC)
	mark := once.Marks[timedOut.ID]
	if !mark.Counted || mark.Completed || mark.Play != WatchPlayFirst || mark.CompletionMilli != 167 {
		t.Fatalf("first aggregation %+v", mark)
	}
	rejoined := timedOut
	rejoined.Previous = &mark
	rejoined.Samples = append([]PlaybackSample(nil), timedOut.Samples...)
	for i := 0; i <= 50; i++ {
		rejoined.Samples = append(rejoined.Samples, PlaybackSample{Seq: len(rejoined.Samples), At: base.Add(time.Duration(1800+60*i) * time.Second),
			Kind: WatchSampleProgress, PositionTicks: DurationToTicks(time.Duration(600+60*i) * time.Second)})
	}
	rejoined.EndedAt = rejoined.Samples[len(rejoined.Samples)-1].At
	again := rollOne(t, WatchStatsBatch{Sessions: []WatchStatsSession{rejoined}, History: once.History}, time.UTC)
	d := rollDays(again)["2026-03-10 "+rollItem]
	// Only the 50 minutes after the return are new; the session and its
	// first play stay counted once and the completion is added.
	if d.EffectiveMillis != 3_000_000 || d.Sessions != 0 || d.Views != 0 || d.Completions != 1 || d.CompletionMilli != 1000-167 {
		t.Fatalf("re-aggregation %+v marks %+v", d, again.Marks)
	}
	if m := again.Marks[rejoined.ID]; !m.Completed || m.Play != WatchPlayFirst || m.Day != mark.Day || !m.Through.Equal(rejoined.EndedAt) || m.CompletionMilli != 1000 {
		t.Fatalf("re-aggregated mark %+v", m)
	}
	if h := again.History[WatchPairKey(rollUser, rollItem)]; h.Views != 1 || h.Completions != 1 || !h.LastCompleted {
		t.Fatalf("history %+v", h)
	}
}

func TestAggregateWatchStatsRejectsBadInput(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	good := rollLinear(rollID(1), rollItem, base, 3600, 0, 600)
	for name, batch := range map[string]WatchStatsBatch{
		"duplicate": {Sessions: []WatchStatsSession{good, good}},
		"no end":    {Sessions: []WatchStatsSession{func() WatchStatsSession { s := good; s.EndedAt = time.Time{}; return s }()}},
		"bad id":    {Sessions: []WatchStatsSession{func() WatchStatsSession { s := good; s.UserID = "x"; return s }()}},
	} {
		if _, err := AggregateWatchStats(batch, DefaultWatchStatsRules(), time.UTC); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, err := AggregateWatchStats(WatchStatsBatch{}, WatchStatsRules{}, time.UTC); !errors.Is(err, ErrInvalid) {
		t.Error("invalid rules accepted")
	}
}

func TestWatchStatsRequestAndBuckets(t *testing.T) {
	today := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	q, err := ResolveWatchStatsRequest(WatchStatsRequest{}, today, time.Monday)
	if err != nil || q.From.Format(time.DateOnly) != "2026-09-05" || q.To.Format(time.DateOnly) != "2026-10-04" || q.Period != WatchPeriodDay || q.Top != 10 {
		t.Fatalf("defaults %+v %v", q, err)
	}
	for name, r := range map[string]WatchStatsRequest{
		"reversed":       {From: "2026-10-05", To: "2026-10-04"},
		"bad date":       {From: "2026-02-30"},
		"loose date":     {From: "2026-2-3"},
		"daily too long": {From: "2025-01-01", To: "2026-10-04"},
		"range too long": {From: "2010-01-01", To: "2026-10-04", Period: "year"},
		"period":         {Period: "hour"},
		"top":            {Top: 101},
		"negative top":   {Top: -1},
		"subject":        {SubjectID: "nope"},
	} {
		if _, err := ResolveWatchStatsRequest(r, today, time.Monday); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted", name)
		}
	}
	if q, err = ResolveWatchStatsRequest(WatchStatsRequest{From: "2017-01-01", To: "2026-10-04", Period: "year"}, today, time.Sunday); err != nil || q.WeekStart != time.Sunday {
		t.Fatalf("year range %+v %v", q, err)
	}
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) // a Sunday
	for _, c := range []struct {
		period WatchPeriod
		start  time.Weekday
		want   string
	}{
		{WatchPeriodDay, time.Monday, "2026-10-04"},
		{WatchPeriodWeek, time.Monday, "2026-09-28"},
		{WatchPeriodWeek, time.Sunday, "2026-10-04"},
		{WatchPeriodMonth, time.Monday, "2026-10-01"},
		{WatchPeriodYear, time.Monday, "2026-01-01"},
	} {
		if got := WatchStatsBucket(day, c.period, c.start).Format(time.DateOnly); got != c.want {
			t.Errorf("%s/%s: %s, want %s", c.period, c.start, got, c.want)
		}
	}
	if got := WatchStatsBucket(day.AddDate(0, 0, 1), WatchPeriodWeek, time.Sunday).Format(time.DateOnly); got != "2026-10-04" {
		t.Errorf("Monday in a Sunday week: %s", got)
	}
	sums := WatchStatsSums{EffectiveMillis: 1999, Sessions: 3, CompletionMilli: 2000}
	if tot := sums.Totals(); tot.EffectiveSeconds != 1 || tot.CompletionRate != 0.667 {
		t.Fatalf("totals %+v", tot)
	}
	if (WatchStatsSums{}).Totals().CompletionRate != 0 {
		t.Fatal("rate without sessions")
	}
}
