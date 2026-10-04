package domain

import (
	"strings"
	"testing"
	"time"
)

func TestPlaybackEndRules(t *testing.T) {
	rules := DefaultWatchStatsRules()
	runtime := DurationToTicks(100 * time.Minute)
	for _, tc := range []struct {
		position, runtime int64
		resume            int64
		completed         bool
	}{
		{DurationToTicks(90 * time.Minute), runtime, 0, true},
		{DurationToTicks(89 * time.Minute), runtime, DurationToTicks(89 * time.Minute), false},
		{DurationToTicks(29 * time.Second), runtime, 0, false},
		{DurationToTicks(30 * time.Second), runtime, DurationToTicks(30 * time.Second), false},
		// Unknown runtime: never completed by position.
		{DurationToTicks(500 * time.Minute), 0, DurationToTicks(500 * time.Minute), false},
	} {
		resume, completed := ResolvePlaybackEnd(tc.position, tc.runtime, rules)
		if resume != tc.resume || completed != tc.completed {
			t.Errorf("%d/%d: resume %d completed %t", tc.position, tc.runtime, resume, completed)
		}
	}
	if ProgressResume(DurationToTicks(95*time.Minute), runtime, rules) != 0 {
		t.Fatal("credits became a resume point")
	}
}

func TestPlaybackReportValidation(t *testing.T) {
	item := "00000000-0000-4000-8000-000000000001"
	valid := []PlaybackReport{
		{Kind: PlaybackReportStart, PlayKey: "abc", ItemID: item},
		{Kind: PlaybackReportStop, PlayKey: DerivedPlayKey(item, item), Failed: true, FailureReason: PlaybackFailureCodecUnsupported},
		{Kind: PlaybackReportPing, PlayKey: strings.Repeat("a", PlayKeyMax)},
	}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	invalid := []PlaybackReport{
		{Kind: "pause", PlayKey: "a"},
		{Kind: PlaybackReportStart, PlayKey: strings.Repeat("a", PlayKeyMax+1)},
		{Kind: PlaybackReportStart, PlayKey: "a/b"},
		{Kind: PlaybackReportStart, PlayKey: "a", SourceID: "x"},
		{Kind: PlaybackReportProgress, PlayKey: "a", PositionTicks: PlaybackPositionMax + 1, PositionKnown: true},
		{Kind: PlaybackReportProgress, PlayKey: "a", Failed: true},
		{Kind: PlaybackReportStop, PlayKey: "a", FailureReason: PlaybackFailureClientBlocked},
	}
	for _, r := range invalid {
		if err := r.Validate(); err != ErrInvalid {
			t.Errorf("%+v accepted", r)
		}
	}
	for _, reason := range PlaybackFailureReasons {
		if !ValidPlaybackFailureReason(reason) {
			t.Fatal(reason)
		}
	}
	if !ValidResumeQuery(ResumeQuery{Limit: 1}) || ValidResumeQuery(ResumeQuery{}) || ValidResumeQuery(ResumeQuery{Limit: 1, Kinds: []string{"Audio"}}) {
		t.Fatal("resume query validation")
	}
}
