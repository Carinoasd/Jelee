package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

type familyAdmissionFake struct {
	*scanStageFake
	capabilities []IgnoreAdmissionCapabilities
	intents      []domain.ScanIntent
}

func (f *familyAdmissionFake) SubmitScanWithIgnoreFamilies(_ context.Context, _ domain.Actor, _, key, _ string, i domain.ScanIntent, _ domain.JobPolicy, _ *domain.ProbeIdentity, _ *domain.NFOIdentity, c IgnoreAdmissionCapabilities) (domain.Job, bool, error) {
	f.capabilities = append(f.capabilities, c)
	f.intents = append(f.intents, i)
	if key == "retained" {
		return domain.Job{ID: accountTargetID}, true, nil
	}
	if i.Ignore.Mode == domain.IgnoreModeFamily && !c.Family || i.Ignore.Mode == domain.IgnoreModeJeleeignore && !c.Custom {
		return domain.Job{}, false, domain.ErrIgnoreUnavailable
	}
	return domain.Job{ID: accountTargetID}, false, nil
}
func (f *familyAdmissionFake) RetryScanWithIgnoreFamilies(_ context.Context, _ domain.Actor, _, _ string, _ domain.JobPolicy, _ *domain.ProbeIdentity, _ *domain.NFOIdentity, c IgnoreAdmissionCapabilities) (domain.Job, bool, error) {
	f.capabilities = append(f.capabilities, c)
	return domain.Job{ID: accountTargetID}, true, nil
}

func TestFamilyAdmissionServiceReadinessAndReplay(t *testing.T) {
	base := &scanStageFake{}
	f := &familyAdmissionFake{scanStageFake: base}
	custom, family := false, true
	services := scanServicesForTest(base, nil, func() bool { return false })
	services.IgnoreAvailable = func() bool { return custom }
	services.FamilyIgnoreAvailable = func() bool { return family }
	j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, services)
	if err != nil {
		t.Fatal(err)
	}
	intent := domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}
	if _, replay, err := j.SubmitScanOptions(context.Background(), accountTestActor(), accountUserID, "new", domain.JobPriorityManual, false, false, intent); err != nil || replay {
		t.Fatal(err)
	}
	custom, family = true, false
	if _, _, err := j.SubmitScanOptions(context.Background(), accountTestActor(), accountUserID, "new", domain.JobPriorityManual, false, false, intent); err != domain.ErrIgnoreUnavailable {
		t.Fatal("custom readiness enabled composed mode", err)
	}
	if _, replay, err := j.SubmitScanOptions(context.Background(), accountTestActor(), accountUserID, "retained", domain.JobPriorityManual, false, false, intent); err != nil || !replay {
		t.Fatal("unavailable family replay blocked", err)
	}
	if _, replay, err := j.Retry(context.Background(), accountTestActor(), accountTargetID, "retry"); err != nil || !replay {
		t.Fatal("family retry not dispatched", err)
	}
	if len(f.capabilities) != 4 || f.capabilities[0] != (IgnoreAdmissionCapabilities{Family: true}) || f.capabilities[1] != (IgnoreAdmissionCapabilities{Custom: true}) || f.capabilities[3] != (IgnoreAdmissionCapabilities{Custom: true}) {
		t.Fatal("family readiness frozen or conflated")
	}
	for _, i := range f.intents {
		if i.Ignore != intent {
			t.Fatal("intent changed")
		}
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := j.SubmitScanOptions(c, accountTestActor(), accountUserID, "cancelled", domain.JobPriorityManual, false, false, intent); err != context.Canceled || len(f.capabilities) != 4 {
		t.Fatal("cancelled admission reached repository", err)
	}
}

func TestFamilyAdmissionServiceRequiresExplicitPort(t *testing.T) {
	base := &scanStageFake{}
	services := scanServicesForTest(base, nil, func() bool { return false })
	services.FamilyIgnoreAvailable = func() bool { return true }
	if j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), base, services); err != domain.ErrInvalid || j != nil {
		t.Fatal("incomplete family service accepted", err)
	}
	services.FamilyIgnoreAvailable = nil
	j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), base, services)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.SubmitScanOptions(context.Background(), accountTestActor(), accountUserID, "family", domain.JobPriorityManual, false, false, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}); err != domain.ErrIgnoreUnavailable {
		t.Fatal("family intent fell back to ordinary admission", err)
	}
}
