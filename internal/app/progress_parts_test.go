package app

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	partOne   = "00000000-0000-4000-9000-0000000000a1"
	partTwo   = "00000000-0000-4000-9000-0000000000a2"
	partThree = "00000000-0000-4000-9000-0000000000a3"
)

// partSources answers every item with one version in three parts of 40, 30
// and 20 minutes; durations names parts without a probe result.
type partSources struct{ unknown map[string]bool }

func (s partSources) ListPlaybackSources(_ context.Context, _ domain.Actor, _ string) ([]domain.PlaybackSourceRecord, error) {
	record := func(id string, number int, minutes int64) domain.PlaybackSourceRecord {
		r := domain.PlaybackSourceRecord{ID: id, ContentType: "video/x-matroska", FileName: "m.mkv", PartOf: partOne, PartNumber: number}
		if !s.unknown[id] {
			micros := minutes * 60_000_000
			r.Metadata = &domain.MediaMetadata{Format: domain.MediaFormat{DurationMicros: &micros}}
		}
		return r
	}
	return []domain.PlaybackSourceRecord{record(partThree, 3, 20), record(partOne, 1, 40), record(partTwo, 2, 30)}, nil
}

// G21.1: progress of a multi-part version is a position on the combined
// timeline. A report naming a later part plays the version, and its
// positions are moved by the earlier parts' durations; the runtime is the
// combined duration.
func TestProgressMapsPartsOntoCombinedTimeline(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	repo := newFakeProgressRepo()
	p, err := NewProgress(repo, partSources{}, ProgressOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	actor := progressActor(1)
	item := progressItem(1)
	start := report(domain.PlaybackReportStart, "part-two", item, 5*time.Minute)
	start.SourceID = partTwo
	if err = p.Report(ctx, actor, start); err != nil {
		t.Fatal(err)
	}
	stored := repo.sessions[actor.UserID+"/part-two"]
	if stored.SourceID != partOne || stored.RuntimeTicks != ticksOf(90*time.Minute) || stored.PositionTicks != ticksOf(45*time.Minute) {
		t.Fatalf("start: source %s runtime %d position %d", stored.SourceID, stored.RuntimeTicks, stored.PositionTicks)
	}
	clock.Advance(time.Minute)
	if err = p.Report(ctx, actor, report(domain.PlaybackReportProgress, "part-two", "", 10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := repo.flushes[len(repo.flushes)-1][0].PositionTicks; got != ticksOf(50*time.Minute) {
		t.Fatalf("progress position %d", got)
	}

	// Without a version the best one plays from the start of part 1.
	if err = p.Report(ctx, actor, report(domain.PlaybackReportStart, "default", item, 3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stored = repo.sessions[actor.UserID+"/default"]; stored.SourceID != partOne || stored.PositionTicks != ticksOf(3*time.Minute) {
		t.Fatalf("default: %+v", stored)
	}

	// An unknown earlier duration drops the later part's positions and
	// leaves the runtime unknown.
	p, err = NewProgress(repo, partSources{unknown: map[string]bool{partTwo: true}}, ProgressOptions{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	third := report(domain.PlaybackReportStart, "part-three", item, 5*time.Minute)
	third.SourceID = partThree
	if err = p.Report(ctx, actor, third); err != nil {
		t.Fatal(err)
	}
	if stored = repo.sessions[actor.UserID+"/part-three"]; stored.SourceID != partOne || stored.PositionTicks != 0 || stored.RuntimeTicks != 0 {
		t.Fatalf("unknown offset: %+v", stored)
	}

	// A source of no version of the item is answered like a hidden item.
	other := report(domain.PlaybackReportStart, "other", item, 0)
	other.SourceID = progressSource
	if err = p.Report(ctx, actor, other); err != domain.ErrNotFound {
		t.Fatalf("foreign source: %v", err)
	}
}
