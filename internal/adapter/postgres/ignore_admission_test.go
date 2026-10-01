package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestIgnoreAdmissionAvailabilityReplayAndRetry(t *testing.T) {
	f := newJobFixture(t)
	submit := func(key string, available bool) (domain.Job, bool, error) {
		return f.s.SubmitScanWithIgnoreCapability(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, domain.ScanIntent{Ignore: ignoreTestIntent()}, f.policy, nil, nil, available)
	}
	before := ignoreSnapshot(t, f)
	if j, replay, err := submit("blocked", false); err != domain.ErrIgnoreUnavailable || j != (domain.Job{}) || replay || ignoreSnapshot(t, f) != before {
		t.Fatal("unavailable admission mutated state", err)
	}
	j, _, err := submit("retained", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, replay, err := submit("retained", false); err != nil || !replay || got.ID != j.ID {
		t.Fatal("retained replay blocked by availability", err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
	before = ignoreSnapshot(t, f)
	if got, replay, err := f.s.RetryScanWithIgnoreCapability(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, false); err != domain.ErrIgnoreUnavailable || got != (domain.Job{}) || replay || ignoreSnapshot(t, f) != before {
		t.Fatal("unavailable retry mutated state", err)
	}
	next, _, err := f.s.RetryScanWithIgnoreCapability(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, replay, err := f.s.RetryScanWithIgnoreCapability(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, false); err != nil || !replay || got.ID != next.ID {
		t.Fatal("retry replay blocked by availability", err)
	}
	if ignoreRequest(t, f, next.ID).Intent != ignoreTestIntent() {
		t.Fatal("retry lost intent")
	}
}
