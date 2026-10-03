//go:build jelee_probe_tests

package runtime

import (
	"context"
	"sort"
	"time"
)

type imagesSoakWorkReport struct {
	Rounds             []imagesSoakRound        `json:"rounds"`
	Rotation           imagesSoakRotation       `json:"rotation"`
	NegativeBefore     imagesMemoryNegative     `json:"negativeBefore"`
	CancellationBefore imagesMemoryCancellation `json:"cancellationBefore"`
	WorkStartedNanos   int64                    `json:"workStartedNanos"`
	WorkFinishedNanos  int64                    `json:"workFinishedNanos"`
	GCBefore           scanGCBoundary           `json:"gcBefore"`
	GCAfter            scanGCBoundary           `json:"gcAfter"`
}

type imagesSoakRoundHooks struct {
	origin time.Time
	begin  func(int64) error
	phase  func(context.Context, string) (residentSample, error)
	emit   func(context.Context, any) error
	// Flush/drain the sampler before returning all-sample extrema for this hour.
	hourRange func(context.Context, int) (rssMin, rssPeak, heapMin, heapPeak uint64, err error)
}

func waitImagesSoakSlot(ctx context.Context, target time.Time) error {
	timer := time.NewTimer(time.Until(target))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func imagesSoakMedianTwice(values [12]uint64) uint64 {
	sort.Slice(values[:], func(i, j int) bool { return values[i] < values[j] })
	return values[5] + values[6]
}

// Fixed workload sizes and slots. Smoke has the same fixtures and two complete
// five-minute slots; no environment knob can accelerate a formal run.
func runImagesSoakRounds(ctx context.Context, scope string, fixtureBytes int64, hooks imagesSoakRoundHooks, session imagesAcceptanceSession, base *imagesMemoryAcceptanceReport, report *imagesSoakWorkReport) string {
	rounds := 288
	if scope == "smoke" {
		rounds = 2
	} else if scope != "formal" {
		return "soak_scope_invalid"
	}
	if fixtureBytes <= 0 || base.FixtureItems != 1000 || hooks.begin == nil || hooks.phase == nil || hooks.emit == nil || hooks.hourRange == nil || hooks.origin.IsZero() {
		return "soak_round_configuration_invalid"
	}
	report.Rounds = make([]imagesSoakRound, 0, rounds)
	if code := seedImagesMemoryNegative(ctx, session.store, session.registration, 1000); code != "" {
		return code
	}
	if _, err := hooks.phase(ctx, "negative"); err != nil {
		return "soak_phase_failed"
	}
	last, err := imagesMemoryGET(ctx, session.client, session.address, session.viewer.Token, 999, 1000, "GET", "", 200)
	if err != nil {
		return "soak_initial_image_failed"
	}
	if code := checkImagesMemoryNegative(ctx, session.life, session.client, session.address, session.viewer.Token, 1000, last.etag, session.access, &report.NegativeBefore); code != "" {
		return code
	}
	if _, err := hooks.phase(ctx, "cancellation"); err != nil {
		return "soak_phase_failed"
	}
	if code := runImagesMemoryCancellation(ctx, session.store, session.applicationName, session.life, session.client, session.address, session.viewer.Token, 1000, &report.CancellationBefore); code != "" {
		return code
	}
	if session.verifySources() != nil {
		return "original_fixture_changed"
	}
	workStart := time.Now()
	report.WorkStartedNanos = workStart.Sub(hooks.origin).Nanoseconds()
	if hooks.begin(report.WorkStartedNanos) != nil {
		return "soak_work_begin_failed"
	}
	report.GCBefore, err = readScanGCBoundary(hooks.origin, time.Now)
	if err != nil {
		return "soak_gc_boundary_failed"
	}
	previousGC := report.GCBefore
	var tags [64]string
	finishHour := func(index int) string {
		after, e := readScanGCBoundary(hooks.origin, time.Now)
		if e != nil || compareScanGCBoundaries(previousGC, after) != nil {
			return "soak_gc_boundary_failed"
		}
		hour := imagesSoakHour{Index: index, GCBefore: previousGC, GCAfter: after}
		var heaps, resident [12]uint64
		for i := 0; i < 12; i++ {
			r := index*12 + i
			hour.CheckpointIndices[i] = r
			heaps[i] = report.Rounds[r].Checkpoint.Resident.HeapBytes
			resident[i] = report.Rounds[r].Checkpoint.Resident.RSSBytes
		}
		hour.HeapMedianTwiceBytes, hour.RSSMedianTwiceBytes = imagesSoakMedianTwice(heaps), imagesSoakMedianTwice(resident)
		hour.RSSMin, hour.RSSPeak, hour.HeapMin, hour.HeapPeak, e = hooks.hourRange(ctx, index)
		if e != nil || hooks.emit(ctx, hour) != nil {
			return "soak_hour_evidence_failed"
		}
		previousGC = after
		return ""
	}
	for index := 0; index < rounds; index++ {
		slot := workStart.Add(time.Duration(index) * 5 * time.Minute)
		if waitImagesSoakSlot(ctx, slot) != nil {
			return "soak_slot_cancelled"
		}
		if index > 0 && index%12 == 0 {
			if code := finishHour(index/12 - 1); code != "" {
				return code
			}
			if session.verifySources() != nil {
				return "original_fixture_changed"
			}
		}
		roundCtx, cancel := context.WithDeadline(ctx, slot.Add(5*time.Minute))
		code := func() string {
			if index == 144 {
				if _, err := hooks.phase(roundCtx, "rotate"); err != nil {
					return "soak_phase_failed"
				}
				rotation := imagesSoakRotation{AtRound: index}
				updated, evidence, e := rotateImagesSoakSession(roundCtx, session.client, session.address, *session.administrator)
				if e != nil {
					return "soak_admin_rotation_failed"
				}
				*session.administrator = updated
				rotation.Admin = evidence
				updated, evidence, e = rotateImagesSoakSession(roundCtx, session.client, session.address, *session.viewer)
				if e != nil {
					return "soak_viewer_rotation_failed"
				}
				*session.viewer = updated
				rotation.Viewer = evidence
				report.Rotation = rotation
				if hooks.emit(roundCtx, rotation) != nil {
					return "soak_rotation_evidence_failed"
				}
			}
			round := imagesSoakRound{Index: index, ScheduledNanos: slot.Sub(hooks.origin).Nanoseconds(), StartedNanos: time.Since(hooks.origin).Nanoseconds()}
			if _, err := hooks.phase(roundCtx, "scan"); err != nil {
				return "soak_phase_failed"
			}
			var code string
			round.Scan, code = runImagesSoakScan(roundCtx, session.client, session.address, session.administrator.Token, session.store, session.registration, index, fixtureBytes)
			if code != "" {
				return code
			}
			if _, err := hooks.phase(roundCtx, "cold"); err != nil {
				return "soak_phase_failed"
			}
			if code := runImagesColdSince(roundCtx, hooks.origin, session.life, session.client, session.address, session.viewer.Token, 1000, &round.Cold, &tags); code != "" {
				return code
			}
			if _, err := hooks.phase(roundCtx, "warm"); err != nil {
				return "soak_phase_failed"
			}
			if code := runImagesWarmSince(roundCtx, hooks.origin, session.life, session.client, session.address, session.viewer.Token, 1000, &round.Warm, tags); code != "" {
				return code
			}
			stats, e := imagesMemoryIdle(roundCtx, session.life)
			if e != nil {
				return "soak_checkpoint_not_idle"
			}
			resources, e := readImagesSoakResources(roundCtx, session.store, session.applicationName, session.observerName)
			if e != nil {
				return "soak_checkpoint_resources_failed"
			}
			sample, e := hooks.phase(roundCtx, "idle")
			if e != nil {
				return "soak_phase_failed"
			}
			round.Checkpoint = imagesSoakCheckpoint{Round: index, ElapsedNanos: sample.ElapsedNanos, Resident: sample, Processor: stats, Resources: resources}
			round.FinishedNanos = time.Since(hooks.origin).Nanoseconds()
			if roundCtx.Err() != nil || round.FinishedNanos >= round.ScheduledNanos+int64(5*time.Minute) {
				return "soak_round_overrun"
			}
			if hooks.emit(roundCtx, round) != nil {
				return "soak_round_evidence_failed"
			}
			report.Rounds = append(report.Rounds, round)
			return ""
		}()
		cancel()
		if code != "" {
			return code
		}
	}
	if waitImagesSoakSlot(ctx, workStart.Add(time.Duration(rounds)*5*time.Minute)) != nil {
		return "soak_slot_cancelled"
	}
	if scope == "formal" {
		if code := finishHour(23); code != "" {
			return code
		}
		report.GCAfter = previousGC
	} else {
		report.GCAfter, err = readScanGCBoundary(hooks.origin, time.Now)
		if err != nil || compareScanGCBoundaries(report.GCBefore, report.GCAfter) != nil {
			return "soak_gc_boundary_failed"
		}
	}
	report.WorkFinishedNanos = time.Since(hooks.origin).Nanoseconds()
	if hooks.emit(ctx, imagesSoakWorkEnd{report.WorkStartedNanos, report.WorkFinishedNanos, rounds}) != nil {
		return "soak_end_evidence_failed"
	}
	if _, err := hooks.phase(ctx, "negative"); err != nil {
		return "soak_phase_failed"
	}
	if code := checkImagesMemoryNegative(ctx, session.life, session.client, session.address, session.viewer.Token, 1000, tags[63], session.access, &base.Negative); code != "" {
		return code
	}
	if _, err := hooks.phase(ctx, "cancellation"); err != nil {
		return "soak_phase_failed"
	}
	return runImagesMemoryCancellation(ctx, session.store, session.applicationName, session.life, session.client, session.address, session.viewer.Token, 1000, &base.Cancellation)
}
