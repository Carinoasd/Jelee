package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestFamilyAdmissionAvailabilityReplayAndRetry(t *testing.T) {
	for _, caseMode := range []string{domain.IgnoreCaseSensitive, domain.IgnoreCaseASCIIInsensitive} {
		t.Run(caseMode, func(t *testing.T) {
			f := newJobFixture(t)
			intent := domain.ScanIntent{Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: caseMode}}
			submit := func(key string, c app.IgnoreAdmissionCapabilities) (domain.Job, bool, error) {
				return f.s.SubmitScanWithIgnoreFamilies(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, intent, f.policy, nil, nil, c)
			}
			before := ignoreSnapshot(t, f)
			for _, c := range []app.IgnoreAdmissionCapabilities{{}, {Custom: true}} {
				if j, replay, err := submit("blocked", c); err != domain.ErrIgnoreUnavailable || j != (domain.Job{}) || replay || ignoreSnapshot(t, f) != before {
					t.Fatal("wrong family capability admitted or mutated", err)
				}
			}
			j, replay, err := submit("retained", app.IgnoreAdmissionCapabilities{Family: true})
			if err != nil || replay {
				t.Fatal("new composed admission failed", err)
			}
			if got, replay, err := submit("retained", app.IgnoreAdmissionCapabilities{}); err != nil || !replay || got.ID != j.ID {
				t.Fatal("retained replay depended on readiness", err)
			}
			read := func(id string) domain.IgnoreRequest {
				tx, err := f.s.Pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				r, err := loadExecutionIgnoreRequest(f.ctx, tx, id, true)
				if err != nil || r == nil {
					t.Fatal("request missing", err)
				}
				if r.Intent != intent.Ignore || r.Identity != domain.DefaultFamilyIgnoreIdentity() || domain.ValidateFamilyIgnoreRequest(*r) != nil {
					t.Fatal("server identity or intent lost")
				}
				if _, err = loadIgnoreRequest(f.ctx, tx, id); err != domain.ErrConflict {
					t.Fatal("old request reader admitted family", err)
				}
				return *r
			}
			_ = read(j.ID)
			if _, err = f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
				t.Fatal(err)
			}
			before = ignoreSnapshot(t, f)
			if got, replay, err := f.s.RetryScanWithIgnoreFamilies(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, app.IgnoreAdmissionCapabilities{Custom: true}); err != domain.ErrIgnoreUnavailable || got != (domain.Job{}) || replay || ignoreSnapshot(t, f) != before {
				t.Fatal("unavailable retry changed state", err)
			}
			if _, _, err = f.s.RetryScanWithIgnoreCapability(f.ctx, f.a, j.ID, "old-retry", f.policy, nil, nil, true); err != domain.ErrConflict {
				t.Fatal("old retry admitted family", err)
			}
			next, replay, err := f.s.RetryScanWithIgnoreFamilies(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, app.IgnoreAdmissionCapabilities{Family: true})
			if err != nil || replay {
				t.Fatal(err)
			}
			_ = read(next.ID)
			if got, replay, err := f.s.RetryScanWithIgnoreFamilies(f.ctx, f.a, j.ID, "retry", f.policy, nil, nil, app.IgnoreAdmissionCapabilities{}); err != nil || !replay || got.ID != next.ID {
				t.Fatal("retry replay depended on readiness", err)
			}
		})
	}
}

func TestFamilyAdmissionContractsAndIsolation(t *testing.T) {
	f := newJobFixture(t)
	family := domain.ScanIntent{Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}}
	if _, _, err := f.s.SubmitScanWithIgnoreCapability(f.ctx, f.a, f.registration.Library.ID, "old", domain.JobPriorityManual, family, f.policy, nil, nil, true); err != domain.ErrInvalid {
		t.Fatal("old public contract widened", err)
	}
	for _, intent := range []domain.ScanIntent{{Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeFamily}}, {Ignore: domain.IgnoreIntent{Mode: "unknown", CaseMode: domain.IgnoreCaseSensitive}}, {Ignore: family.Ignore, Probe: domain.ProbeIntent{Scope: domain.ProbeScopeLibraryRebuild}}} {
		before := ignoreSnapshot(t, f)
		if _, _, err := f.s.SubmitScanWithIgnoreFamilies(f.ctx, f.a, f.registration.Library.ID, "invalid", domain.JobPriorityManual, intent, f.policy, nil, nil, app.IgnoreAdmissionCapabilities{Custom: true, Family: true}); err != domain.ErrInvalid || ignoreSnapshot(t, f) != before {
			t.Fatal("invalid contract accepted", err)
		}
	}
	custom := domain.ScanIntent{Ignore: ignoreTestIntent()}
	if _, _, err := f.s.SubmitScanWithIgnoreFamilies(f.ctx, f.a, f.registration.Library.ID, "custom", domain.JobPriorityManual, custom, f.policy, nil, nil, app.IgnoreAdmissionCapabilities{Family: true}); err != domain.ErrIgnoreUnavailable {
		t.Fatal("family readiness admitted custom mode", err)
	}
	j, _, err := f.s.SubmitScanWithIgnoreFamilies(f.ctx, f.a, f.registration.Library.ID, "same", domain.JobPriorityManual, family, f.policy, nil, nil, app.IgnoreAdmissionCapabilities{Family: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range []domain.ScanIntent{custom, {}, {Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseASCIIInsensitive}}, {Ignore: family.Ignore, NFO: true}} {
		if _, _, err := f.s.SubmitScanWithIgnoreFamilies(f.ctx, f.a, f.registration.Library.ID, "same", domain.JobPriorityManual, intent, f.policy, nil, nil, app.IgnoreAdmissionCapabilities{}); err != domain.ErrConflict {
			t.Fatal("changed replay intent admitted", err)
		}
	}
	if _, err := f.s.CancelJob(f.ctx, f.a, j.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFamilyAdmissionLiveAuthorization(t *testing.T) {
	f := newJobFixture(t)
	intent := domain.ScanIntent{Ignore: domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}}
	submit := func(a domain.Actor, c app.IgnoreAdmissionCapabilities) (domain.Job, bool, error) {
		return f.s.SubmitScanWithIgnoreFamilies(f.ctx, a, f.registration.Library.ID, "same", domain.JobPriorityManual, intent, f.policy, nil, nil, c)
	}
	if _, _, err := submit(domain.Actor{}, app.IgnoreAdmissionCapabilities{Family: true}); err == nil {
		t.Fatal("anonymous admission")
	}
	if _, _, err := submit(f.a, app.IgnoreAdmissionCapabilities{Family: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := submit(f.a, app.IgnoreAdmissionCapabilities{}); err != domain.ErrForbidden {
		t.Fatal("demoted replay", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := submit(f.a, app.IgnoreAdmissionCapabilities{}); err != domain.ErrUnauthenticated {
		t.Fatal("revoked replay", err)
	}
}
