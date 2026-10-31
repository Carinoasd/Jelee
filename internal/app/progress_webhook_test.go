package app

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// flushMarks collects the event marks of every flushed entry in order.
func flushMarks(repo *fakeProgressRepo) (progress int, pauses []domain.PlaybackPause, ended []domain.PlaybackFlush) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, batch := range repo.flushes {
		for _, e := range batch {
			if e.Progress {
				progress++
			}
			if e.Pause != nil {
				pauses = append(pauses, *e.Pause)
			}
			if e.End != nil {
				ended = append(ended, e)
			}
		}
	}
	return progress, pauses, ended
}

// G12.1 playback.progress is throttled to one mark per interval of a
// playing session however often the client reports and the buffer flushes;
// a paused session raises none, every pause raises playback.paused once with
// the position it paused at, and an ending raises no progress.
func TestProgressWebhookMarksThrottleProgressAndReportPauses(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{FlushInterval: 10 * time.Second, ProgressEventInterval: time.Minute})
	actor := progressActor(1)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "hook", progressItem(1), 0)); err != nil {
		t.Fatal(err)
	}
	// Three minutes of playing, a report every 5 s and a flush every 10 s:
	// 18 flushes, three progress marks.
	position := time.Duration(0)
	for second := 5; second <= 180; second += 5 {
		clock.Advance(5 * time.Second)
		position += 5 * time.Second
		if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "hook", "", position)); err != nil {
			t.Fatal(err)
		}
		if second%10 == 0 {
			if _, err := p.Flush(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if progress, pauses, _ := flushMarks(repo); progress != 3 || len(pauses) != 0 {
		t.Fatalf("progress marks %d pauses %v, want 3 and none", progress, pauses)
	}
	// Pause: one pause mark at the pause position; two paused minutes add
	// no progress mark.
	paused := report(domain.PlaybackReportProgress, "hook", "", position)
	paused.Paused = true
	clock.Advance(5 * time.Second)
	pausedAt := clock.Now()
	if err := p.Report(ctx, actor, paused); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		clock.Advance(10 * time.Second)
		if err := p.Report(ctx, actor, paused); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	progress, pauses, _ := flushMarks(repo)
	if progress != 3 || len(pauses) != 1 || !pauses[0].At.Equal(pausedAt) || pauses[0].PositionTicks != ticksOf(position) {
		t.Fatalf("after pause: progress %d pauses %+v", progress, pauses)
	}
	// Resume, pause, resume, pause within one flush: one mark, the last.
	for i, pause := range []bool{false, true, false, true} {
		clock.Advance(time.Second)
		position += time.Second
		r := report(domain.PlaybackReportProgress, "hook", "", position)
		r.Paused = pause
		if err := p.Report(ctx, actor, r); err != nil {
			t.Fatal(i, err)
		}
	}
	lastPause := clock.Now()
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// Resuming after more than an interval since the last progress mark
	// marks progress at the next flush, and a stop marks none.
	resumed := report(domain.PlaybackReportProgress, "hook", "", position+time.Second)
	clock.Advance(time.Second)
	if err := p.Report(ctx, actor, resumed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute + time.Second)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStop, "hook", "", position+time.Minute)); err != nil {
		t.Fatal(err)
	}
	progress, pauses, ended := flushMarks(repo)
	if len(pauses) != 2 || !pauses[1].At.Equal(lastPause) || pauses[1].PositionTicks != ticksOf(position) {
		t.Fatalf("coalesced pauses %+v", pauses)
	}
	if progress != 4 || len(ended) != 1 || ended[0].Progress {
		t.Fatalf("progress %d ended %+v", progress, ended)
	}
}

// A flush that fails keeps its marks: the next flush raises the pause, and
// the progress mark comes again as soon as the session plays.
func TestProgressWebhookMarksSurviveFailedFlush(t *testing.T) {
	ctx := context.Background()
	p, repo, clock := newTestProgress(t, ProgressOptions{ProgressEventInterval: time.Minute})
	actor := progressActor(2)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportStart, "retry", progressItem(2), 0)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "retry", "", 2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	paused := report(domain.PlaybackReportProgress, "retry", "", 2*time.Minute)
	paused.Paused = true
	if err := p.Report(ctx, actor, paused); err != nil {
		t.Fatal(err)
	}
	// The failed flush carries the pause; progress is not marked while
	// paused.
	repo.failFlush = domain.ErrDatabase
	if _, err := p.Flush(ctx); err == nil {
		t.Fatal("flush did not fail")
	}
	repo.failFlush = nil
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if progress, pauses, _ := flushMarks(repo); len(pauses) != 1 || progress != 0 {
		t.Fatalf("pause lost with the failed flush: %+v, progress %d", pauses, progress)
	}
	// A progress mark lost with a failed flush is raised again at once.
	clock.Advance(time.Second)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "retry", "", 2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	repo.failFlush = domain.ErrDatabase
	if _, err := p.Flush(ctx); err == nil {
		t.Fatal("flush did not fail")
	}
	repo.failFlush = nil
	clock.Advance(time.Second)
	if err := p.Report(ctx, actor, report(domain.PlaybackReportProgress, "retry", "", 2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if progress, _, _ := flushMarks(repo); progress != 1 {
		t.Fatalf("progress marks after retry %d", progress)
	}
}

func TestProgressWebhookIntervalValidated(t *testing.T) {
	if _, err := NewProgress(newFakeProgressRepo(), fakeSources{}, ProgressOptions{ProgressEventInterval: -time.Second}); err == nil {
		t.Fatal("negative progress event interval accepted")
	}
}
