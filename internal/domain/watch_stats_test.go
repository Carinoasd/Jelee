package domain

import (
	"errors"
	"testing"
	"time"
)

var watchT0 = time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC)

// ws builds a sample at t0+sec with position pos seconds.
func ws(kind WatchSampleKind, sec, pos float64) WatchSample {
	return WatchSample{At: watchT0.Add(secs(sec)), Kind: kind, Position: secs(pos)}
}

func secs(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func progress(sec, pos float64) WatchSample { return ws(WatchSampleProgress, sec, pos) }

func seek(sec, from, to float64) WatchSample {
	s := ws(WatchSampleSeek, sec, to)
	s.From, s.FromKnown = secs(from), true
	return s
}

func TestComputeWatchSessionRules(t *testing.T) {
	rules := DefaultWatchStatsRules()
	paused := progress(30, 30)
	paused.Paused = true
	cases := []struct {
		name       string
		runtime    float64
		samples    []WatchSample
		effective  float64
		covered    float64
		anomalies  WatchAnomalies
		completed  bool
		counted    bool
		resumeSecs float64
	}{
		{
			name:      "steady playback",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(10, 10), progress(20, 20), ws(WatchSampleStop, 30, 30)},
			effective: 30, covered: 30, resumeSecs: 30, // MinResumePosition is inclusive
		},
		{
			name:      "pause excluded",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), ws(WatchSamplePause, 60, 60), ws(WatchSampleResume, 660, 60), ws(WatchSampleStop, 720, 120)},
			effective: 120, covered: 120, counted: true, resumeSecs: 120,
		},
		{
			name:      "position moved while paused is not credited",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), ws(WatchSamplePause, 60, 60), ws(WatchSampleResume, 120, 90), ws(WatchSampleStop, 130, 100)},
			effective: 70, covered: 70, resumeSecs: 100,
		},
		{
			name:      "progress with paused flag",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), paused, progress(600, 30), progress(610, 40)},
			effective: 40, covered: 40, resumeSecs: 40,
		},
		{
			name:      "stall counts media advance only",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(60, 15)},
			effective: 15, covered: 15,
		},
		{
			name:      "2x speed counts wall time, covers media",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(60, 120)},
			effective: 60, covered: 120, resumeSecs: 120,
		},
		{
			name:      "forward jump without seek event",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(10, 10), progress(20, 600), progress(30, 610)},
			effective: 20, covered: 20, anomalies: WatchAnomalies{Jumps: 1}, resumeSecs: 610,
		},
		{
			name:      "backward jump without seek event",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(100, 100), progress(110, 20), progress(120, 30)},
			effective: 110, covered: 100, anomalies: WatchAnomalies{Jumps: 1}, resumeSecs: 30,
		},
		{
			name:      "jitter within tolerance is not a jump",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(10, 10), progress(20, 8), progress(30, 22)},
			effective: 20, covered: 22,
		},
		{
			name:      "seek with known from credits the part before the jump",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(10, 10), seek(15, 15, 900), progress(25, 910)},
			effective: 25, covered: 25, resumeSecs: 910,
		},
		{
			name:      "seek back re-watch adds time not coverage",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(60, 60), seek(60, 60, 0), progress(120, 60)},
			effective: 120, covered: 60, counted: true, resumeSecs: 60,
		},
		{
			name:      "seek while paused stays paused",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), ws(WatchSamplePause, 10, 10), seek(20, 10, 100), progress(80, 100)},
			effective: 10, covered: 10, resumeSecs: 100,
		},
		{
			name:      "report gap capped",
			runtime:   7200,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(1800, 1800), progress(1810, 1810)},
			effective: 310, covered: 310, anomalies: WatchAnomalies{CappedGaps: 1}, counted: true, resumeSecs: 1810,
		},
		{
			name:      "gap at limit is not capped",
			runtime:   7200,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(300, 300)},
			effective: 300, covered: 300, counted: true, resumeSecs: 300,
		},
		{
			name:    "clock rollback discards interval and overlap is merged",
			runtime: 3600,
			samples: []WatchSample{ws(WatchSampleStart, 0, 0), progress(60, 60), progress(30, 70), progress(90, 130)},
			// credited: [0,60) and [30,90) on the wall clock -> union 90s.
			effective: 90, covered: 120, anomalies: WatchAnomalies{ClockRollbacks: 1}, resumeSecs: 130,
		},
		{
			name:      "duplicate samples count once",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(10, 10), progress(10, 10), progress(20, 20)},
			effective: 20, covered: 20,
		},
		{
			name:      "samples after stop and malformed samples ignored",
			runtime:   3600,
			samples:   []WatchSample{{Kind: WatchSampleProgress}, ws(WatchSampleStart, 0, 0), ws("teleport", 5, 5), ws(WatchSampleProgress, 6, -1), ws(WatchSampleStop, 10, 10), progress(20, 20)},
			effective: 10, covered: 10, anomalies: WatchAnomalies{Ignored: 4},
		},
		{
			name:      "fail ends session",
			runtime:   3600,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), ws(WatchSampleFail, 10, 10), progress(20, 20)},
			effective: 10, covered: 10, anomalies: WatchAnomalies{Ignored: 1},
		},
		{
			name:      "completion at threshold",
			runtime:   1000,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 100), progress(300, 400), progress(600, 700), progress(900, 1000)},
			effective: 900, covered: 900, completed: true, counted: true,
		},
		{
			name:      "just below completion threshold",
			runtime:   1000,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(300, 300), progress(600, 600), progress(899, 899)},
			effective: 899, covered: 899, counted: true, resumeSecs: 899,
		},
		{
			name:      "coverage clipped to runtime",
			runtime:   100,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(120, 120)},
			effective: 120, covered: 100, completed: true, counted: true,
		},
		{
			name:      "unknown runtime",
			runtime:   0,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(200, 200)},
			effective: 200, covered: 200, counted: true, resumeSecs: 200,
		},
		{
			name:      "short item counts by ratio",
			runtime:   300,
			samples:   []WatchSample{ws(WatchSampleStart, 0, 0), progress(30, 30), progress(40, 40)},
			effective: 40, covered: 40, counted: true, resumeSecs: 40,
		},
		{
			name:    "empty session",
			runtime: 3600,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ComputeWatchSession(c.samples, secs(c.runtime), rules)
			if err != nil {
				t.Fatal(err)
			}
			if got.Effective != secs(c.effective) || got.Covered != secs(c.covered) {
				t.Fatalf("effective=%v covered=%v, want %vs %vs", got.Effective, got.Covered, c.effective, c.covered)
			}
			if got.Anomalies != c.anomalies {
				t.Fatalf("anomalies %+v, want %+v", got.Anomalies, c.anomalies)
			}
			if got.Completed != c.completed || got.Counted != c.counted {
				t.Fatalf("completed=%v counted=%v", got.Completed, got.Counted)
			}
			if got.ResumePosition != secs(c.resumeSecs) {
				t.Fatalf("resume %v, want %vs", got.ResumePosition, c.resumeSecs)
			}
			if c.runtime > 0 && got.CompletionRate != min(1, c.covered/c.runtime) {
				t.Fatalf("completion rate %v", got.CompletionRate)
			}
			var sum time.Duration
			for i, s := range got.Segments {
				sum += s.Duration()
				if i > 0 && !s.Start.After(got.Segments[i-1].End) {
					t.Fatalf("segments not merged: %+v", got.Segments)
				}
			}
			if sum != got.Effective {
				t.Fatalf("segments sum %v != effective %v", sum, got.Effective)
			}
		})
	}
}

func TestWatchStatsRulesValidation(t *testing.T) {
	if err := DefaultWatchStatsRules().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*WatchStatsRules){
		func(r *WatchStatsRules) { r.MaxReportGap = time.Second },
		func(r *WatchStatsRules) { r.MaxReportGap = 2 * time.Hour },
		func(r *WatchStatsRules) { r.MaxPlaybackRate = 0.5 },
		func(r *WatchStatsRules) { r.MaxPlaybackRate = nanValue() },
		func(r *WatchStatsRules) { r.PositionTolerance = -1 },
		func(r *WatchStatsRules) { r.CompletedRatio = 0.4 },
		func(r *WatchStatsRules) { r.CompletedRatio = 1.01 },
		func(r *WatchStatsRules) { r.MinViewDuration = -1 },
		func(r *WatchStatsRules) { r.MinViewRatio = 2 },
		func(r *WatchStatsRules) { r.MinResumePosition = time.Hour },
	} {
		r := DefaultWatchStatsRules()
		mutate(&r)
		if _, err := ComputeWatchSession(nil, 0, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v", r)
		}
	}
	// A stricter rate makes 2x playback a jump.
	strict := DefaultWatchStatsRules()
	strict.MaxPlaybackRate = 1
	got, _ := ComputeWatchSession([]WatchSample{ws(WatchSampleStart, 0, 0), progress(60, 120)}, 0, strict)
	if got.Effective != 0 || got.Anomalies.Jumps != 1 {
		t.Fatalf("strict rate: %+v", got)
	}
}

func TestClassifyWatchPlay(t *testing.T) {
	counted := WatchSessionStats{Counted: true}
	done := WatchSessionStats{Counted: true, Completed: true}
	short := WatchSessionStats{}
	steps := []struct {
		s    WatchSessionStats
		want WatchPlayKind
	}{
		{short, WatchPlayNone},
		{counted, WatchPlayFirst},
		{short, WatchPlayNone},
		{counted, WatchPlayContinue},
		{done, WatchPlayContinue}, // finishes the first watch-through
		{counted, WatchPlayRewatch},
		{done, WatchPlayContinue}, // finishes the re-watch
		{done, WatchPlayRewatch},  // watched straight through again
	}
	var h WatchHistory
	for i, step := range steps {
		var kind WatchPlayKind
		kind, h = ClassifyWatchPlay(h, step.s)
		if kind != step.want {
			t.Fatalf("step %d: %s, want %s", i, kind, step.want)
		}
	}
	if h != (WatchHistory{Views: 3, Completions: 3, LastCompleted: true}) {
		t.Fatalf("history %+v", h)
	}
}

func TestAggregateWatchDaily(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	rules := DefaultWatchStatsRules()
	// 23:30-00:30 local (15:30-16:30 UTC): crosses local midnight.
	start := time.Date(2026, 10, 4, 15, 30, 0, 0, time.UTC)
	var samples []WatchSample
	for i := 0; i <= 60; i++ {
		samples = append(samples, WatchSample{At: start.Add(time.Duration(i) * time.Minute), Kind: WatchSampleProgress, Position: time.Duration(i) * time.Minute})
	}
	samples[0].Kind = WatchSampleStart
	a, err := ComputeWatchSession(samples, time.Hour, rules)
	if err != nil || !a.Completed {
		t.Fatalf("session a: %v %+v", err, a)
	}
	// The same viewing reported by a second device for 10 minutes overlaps.
	b, _ := ComputeWatchSession(samples[10:21], time.Hour, rules)
	// Another user, unrelated item, short session (not counted).
	c, _ := ComputeWatchSession(samples[:2], 2*time.Hour, rules)

	var h WatchHistory
	var playA, playB WatchPlayKind
	playA, h = ClassifyWatchPlay(h, a)
	playB, _ = ClassifyWatchPlay(h, b)
	rows := AggregateWatchDaily([]WatchSessionRecord{
		{UserID: "u1", ItemID: "i1", LibraryID: "lib", ItemKind: "movie", Stats: a, Play: playA},
		{UserID: "u1", ItemID: "i1", LibraryID: "lib", ItemKind: "movie", Stats: b, Play: playB},
		{UserID: "u2", ItemID: "i2", LibraryID: "lib", ItemKind: "episode", Stats: c, Play: WatchPlayNone},
	}, loc)
	want := []WatchDaily{
		{Day: "2026-10-04", UserID: "u1", ItemID: "i1", LibraryID: "lib", ItemKind: "movie", Effective: 30 * time.Minute, Sessions: 2, Views: 2, FirstPlays: 1, Rewatches: 1, Completions: 1},
		{Day: "2026-10-04", UserID: "u2", ItemID: "i2", LibraryID: "lib", ItemKind: "episode", Effective: time.Minute},
		{Day: "2026-10-05", UserID: "u1", ItemID: "i1", LibraryID: "lib", ItemKind: "movie", Effective: 30 * time.Minute},
	}
	if playB != WatchPlayRewatch {
		t.Fatalf("second device session after completion: %s", playB)
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d\n got %+v\nwant %+v", i, rows[i], want[i])
		}
	}
	var total time.Duration
	for _, r := range rows {
		if r.UserID == "u1" {
			total += r.Effective
		}
	}
	if total != time.Hour {
		t.Fatalf("overlapping devices double counted: %v", total)
	}
}

func TestWatchPeriodStart(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	// 2026-10-04 is a Sunday; 17:00 UTC is 01:00 on Monday 10-05 local.
	at := time.Date(2026, 10, 4, 17, 0, 0, 0, time.UTC)
	cases := map[WatchPeriod]string{
		WatchPeriodDay:   "2026-10-05",
		WatchPeriodWeek:  "2026-10-05",
		WatchPeriodMonth: "2026-10-01",
		WatchPeriodYear:  "2026-01-01",
	}
	for period, want := range cases {
		got, err := WatchPeriodStart(at, period, loc)
		if err != nil || got.Format(time.DateOnly) != want || got.Hour() != 0 || got.Location() != loc {
			t.Errorf("%s: %v %v, want %s", period, got, err, want)
		}
	}
	sunday, _ := WatchPeriodStart(at, WatchPeriodWeek, time.UTC)
	if sunday.Format(time.DateOnly) != "2026-09-28" {
		t.Fatalf("UTC Sunday belongs to the week of Monday 09-28, got %v", sunday)
	}
	if _, err := WatchPeriodStart(at, "quarter", loc); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown period accepted")
	}
}
