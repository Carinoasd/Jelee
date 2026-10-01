package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type scanStageFake struct {
	ScanJobRepository
	NFOAdminRepository
	NFOQueryRepository
	ImageQueryRepository
	submit       func(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
	retry        func(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
	policy       func(context.Context, domain.Actor, string) (domain.NFOLibraryPolicy, error)
	setPolicy    func(context.Context, domain.Actor, string, string, int64, string) (domain.NFOLibraryPolicy, bool, error)
	summary      func(context.Context, domain.Actor, string) (domain.NFOJobSummary, error)
	observations func(context.Context, domain.Actor, string, string, int, domain.NFOIdentity) (domain.NFOObservationPage, error)
	issues       func(context.Context, domain.Actor, string, string, int, int, domain.NFOIdentity) (domain.NFOIssuesPage, error)
	images       func(context.Context, domain.Actor, string) (domain.ImageJobSummary, error)
}

func (f *scanStageFake) SubmitScanWithStages(c context.Context, a domain.Actor, l, k, p string, i domain.ScanIntent, policy domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
	return f.submit(c, a, l, k, p, i, policy, probe, nfo)
}
func (f *scanStageFake) RetryScanWithStages(c context.Context, a domain.Actor, j, k string, p domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
	return f.retry(c, a, j, k, p, probe, nfo)
}
func (f *scanStageFake) GetNFOLibraryPolicy(c context.Context, a domain.Actor, l string) (domain.NFOLibraryPolicy, error) {
	return f.policy(c, a, l)
}
func (f *scanStageFake) SetNFOLibraryPolicy(c context.Context, a domain.Actor, l, k string, g int64, m string) (domain.NFOLibraryPolicy, bool, error) {
	return f.setPolicy(c, a, l, k, g, m)
}
func (f *scanStageFake) GetNFOJobSummary(c context.Context, a domain.Actor, j string) (domain.NFOJobSummary, error) {
	return f.summary(c, a, j)
}
func (f *scanStageFake) ListNFOObservations(c context.Context, a domain.Actor, l, after string, limit int, i domain.NFOIdentity) (domain.NFOObservationPage, error) {
	return f.observations(c, a, l, after, limit, i)
}
func (f *scanStageFake) GetNFOObservationIssues(c context.Context, a domain.Actor, l, o string, offset, limit int, i domain.NFOIdentity) (domain.NFOIssuesPage, error) {
	return f.issues(c, a, l, o, offset, limit, i)
}
func (f *scanStageFake) GetImageJobSummary(c context.Context, a domain.Actor, j string) (domain.ImageJobSummary, error) {
	return f.images(c, a, j)
}

func scanServicesForTest(f *scanStageFake, identity *domain.NFOIdentity, available func() bool) ScanServices {
	return ScanServices{NFOAdmin: f, NFOQueries: f, Images: f, NFOIdentity: identity, NFOAvailable: available}
}

func TestScanServicesRejectPartialConfiguration(t *testing.T) {
	f := &scanStageFake{}
	identity := domain.DefaultNFOIdentity()
	for name, change := range map[string]func(*ScanServices){
		"missing admin":              func(s *ScanServices) { s.NFOAdmin = nil },
		"missing queries":            func(s *ScanServices) { s.NFOQueries = nil },
		"missing images":             func(s *ScanServices) { s.Images = nil },
		"missing health":             func(s *ScanServices) { s.NFOAvailable = nil },
		"available without identity": func(s *ScanServices) { s.NFOIdentity = nil },
		"wrong parser":               func(s *ScanServices) { bad := identity; bad.ParserVersion = "unsupported"; s.NFOIdentity = &bad },
		"probe health without repository": func(s *ScanServices) {
			s.ProbeCapability = func() domain.ProbeCapability { return domain.ProbeCapability{} }
		},
	} {
		t.Run(name, func(t *testing.T) {
			services := scanServicesForTest(f, &identity, func() bool { return true })
			change(&services)
			if got, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, services); err != domain.ErrInvalid || got != nil {
				t.Fatal("partial scan configuration accepted", err)
			}
		})
	}
	services := scanServicesForTest(f, &identity, func() bool { return true })
	if got, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), nil, services); err != domain.ErrInvalid || got != nil {
		t.Fatal("missing stage repository accepted", err)
	}
	// An unavailable local reader still permits policy and retained history.
	services.NFOIdentity, services.NFOAvailable = nil, func() bool { return false }
	if _, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, services); err != nil {
		t.Fatal("unavailable reader prevents administrative startup", err)
	}
}

func TestNFOAdmissionCopiesIdentityAndKeepsReplayWithoutReader(t *testing.T) {
	identity := domain.DefaultNFOIdentity()
	trusted := identity
	available := true
	calls := 0
	f := &scanStageFake{}
	f.submit = func(_ context.Context, a domain.Actor, l, k, p string, i domain.ScanIntent, policy domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
		calls++
		if a != accountTestActor() || l != accountUserID || p != "manual" || policy != jobTestPolicy() || probe != nil {
			t.Fatal("public admission changed")
		}
		if k == "plain" {
			if i.NFO || nfo != nil {
				t.Fatal("plain scan gained NFO")
			}
			return domain.Job{ID: accountTargetID}, false, nil
		}
		if !i.NFO || i.Probe.Scope != "" {
			t.Fatal("NFO-only changed probe intent")
		}
		if available {
			if nfo == nil || *nfo != trusted {
				t.Fatal("trusted identity changed")
			}
			nfo.ParserVersion = "repository mutation"
		} else if nfo != nil {
			t.Fatal("unavailable reader supplied authority")
		}
		if k == "replay" {
			return domain.Job{ID: accountTargetID}, true, nil
		}
		if !available {
			return domain.Job{ID: accountTargetID}, true, domain.ErrNFODisabled
		}
		return domain.Job{ID: accountTargetID}, false, nil
	}
	j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, scanServicesForTest(f, &identity, func() bool { return available }))
	if err != nil {
		t.Fatal(err)
	}
	identity.ParserVersion = "caller mutation"
	for range 2 {
		if _, _, err = j.SubmitScanStages(context.Background(), accountTestActor(), accountUserID, "new", "manual", false, true); err != nil {
			t.Fatal(err)
		}
	}
	available = false
	if job, replay, err := j.SubmitScanStages(context.Background(), accountTestActor(), accountUserID, "replay", "manual", false, true); err != nil || !replay || job.ID != accountTargetID {
		t.Fatal("retained replay blocked")
	}
	if job, replay, err := j.SubmitScanStages(context.Background(), accountTestActor(), accountUserID, "new", "manual", false, true); err != domain.ErrNFOReaderUnavailable || replay || job.ID != "" {
		t.Fatal("failed request exposed a job")
	}
	if _, _, err = j.SubmitScan(context.Background(), accountTestActor(), accountUserID, "plain", "manual", false); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatal("wrong admission count")
	}
}

func TestScanRetryAndRebuildUseAtomicStages(t *testing.T) {
	identity := domain.DefaultNFOIdentity()
	probe := appProbeIdentity()
	f := &scanStageFake{}
	retryCalls := 0
	rebuildCalls := 0
	f.retry = func(_ context.Context, _ domain.Actor, j, k string, _ domain.JobPolicy, p *domain.ProbeIdentity, n *domain.NFOIdentity) (domain.Job, bool, error) {
		retryCalls++
		if j != accountTargetID || k != "retry" || p == nil || *p != probe || n == nil || *n != identity {
			t.Fatal("retry lost trusted authority")
		}
		return domain.Job{ID: j}, true, nil
	}
	f.submit = func(_ context.Context, _ domain.Actor, l, k, _ string, i domain.ScanIntent, _ domain.JobPolicy, p *domain.ProbeIdentity, n *domain.NFOIdentity) (domain.Job, bool, error) {
		rebuildCalls++
		if i.NFO || n != nil || p == nil || *p != probe {
			t.Fatal("rebuild gained NFO")
		}
		if k == "item" {
			if l != "" || i.Probe.Scope != domain.ProbeScopeItemRebuild || i.Probe.TargetItemID != accountUserID {
				t.Fatal("item intent")
			}
		} else if l != accountUserID || i.Probe.Scope != domain.ProbeScopeLibraryRebuild {
			t.Fatal("library intent")
		}
		return domain.Job{ID: accountTargetID}, false, nil
	}
	services := scanServicesForTest(f, &identity, func() bool { return true })
	services.Probes = probeJobFake{}
	services.ProbeIdentity = &probe
	services.ProbeCapability = func() domain.ProbeCapability { return domain.ProbeCapability{Enabled: true, Available: true} }
	j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, services)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = j.Retry(context.Background(), accountTestActor(), accountTargetID, "retry"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []bool{false, true} {
		key := "library"
		if item {
			key = "item"
		}
		if _, _, err = j.RebuildProbe(context.Background(), accountTestActor(), accountUserID, key, "manual", item); err != nil {
			t.Fatal(err)
		}
	}
	if retryCalls != 1 || rebuildCalls != 2 {
		t.Fatal("legacy repository bypassed atomic stages")
	}
}

func TestNFOPolicyHistoryAndQueriesKeepSeparateAvailability(t *testing.T) {
	identity := domain.DefaultNFOIdentity()
	available := false
	f := &scanStageFake{}
	calls := 0
	check := func(c context.Context, a domain.Actor, id string) {
		calls++
		if c == nil || a != accountTestActor() || id != accountUserID {
			t.Fatal("query authority")
		}
	}
	f.policy = func(c context.Context, a domain.Actor, id string) (domain.NFOLibraryPolicy, error) {
		check(c, a, id)
		return domain.NFOLibraryPolicy{LibraryID: id, Mode: domain.NFOModeOff, Generation: 1}, nil
	}
	f.setPolicy = func(c context.Context, a domain.Actor, id, k string, g int64, m string) (domain.NFOLibraryPolicy, bool, error) {
		check(c, a, id)
		if k != "mode" || g != 1 || m != domain.NFOModeReadOnly {
			t.Fatal("policy body")
		}
		return domain.NFOLibraryPolicy{LibraryID: id, Mode: m, Generation: 2}, true, nil
	}
	f.summary = func(c context.Context, a domain.Actor, id string) (domain.NFOJobSummary, error) {
		check(c, a, id)
		return domain.NFOJobSummary{JobID: id, LibraryID: id, Mode: domain.NFOModeOff, Phase: domain.NFOSummaryDisabled}, nil
	}
	f.images = func(c context.Context, a domain.Actor, id string) (domain.ImageJobSummary, error) {
		check(c, a, id)
		return domain.ImageJobSummary{JobID: id, LibraryID: id}, nil
	}
	f.observations = func(c context.Context, a domain.Actor, id, after string, limit int, i domain.NFOIdentity) (domain.NFOObservationPage, error) {
		check(c, a, id)
		if after != accountTargetID || limit != 20 || i != identity {
			t.Fatal("untrusted current identity")
		}
		return domain.NFOObservationPage{Items: []domain.NFOObservation{}}, nil
	}
	f.issues = func(c context.Context, a domain.Actor, id, observation string, offset, limit int, i domain.NFOIdentity) (domain.NFOIssuesPage, error) {
		check(c, a, id)
		if observation != accountTargetID || offset != 32 || limit != 32 || i != identity {
			t.Fatal("issue page")
		}
		return domain.NFOIssuesPage{ObservationID: observation, Offset: offset, Issues: []domain.NFOIssue{}}, nil
	}
	j, err := NewJobsWithScanStages(jobRepositoryFake{}, jobTestPolicy(), f, scanServicesForTest(f, &identity, func() bool { return available }))
	if err != nil {
		t.Fatal(err)
	}
	c := context.Background()
	a := accountTestActor()
	if _, err = j.NFOPolicy(c, a, accountUserID); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := j.SetNFOPolicy(c, a, accountUserID, "mode", 1, domain.NFOModeReadOnly); err != nil || !replay {
		t.Fatal("policy requires reader")
	}
	if _, err = j.NFOSummary(c, a, accountUserID); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Images(c, a, accountUserID); err != nil {
		t.Fatal(err)
	}
	if _, err = j.NFOObservations(c, a, accountUserID, "", 20); err != domain.ErrNFOReaderUnavailable {
		t.Fatal("current query lacked reader")
	}
	if _, err = j.NFOIssues(c, a, accountUserID, accountTargetID, 0, 32); err != domain.ErrNFOReaderUnavailable {
		t.Fatal("issues query lacked reader")
	}
	if calls != 4 {
		t.Fatal("unavailable query reached storage")
	}
	available = true
	if _, err = j.NFOObservations(c, a, accountUserID, accountTargetID, 20); err != nil {
		t.Fatal(err)
	}
	if _, err = j.NFOIssues(c, a, accountUserID, accountTargetID, 32, 32); err != nil {
		t.Fatal(err)
	}
	if calls != 6 {
		t.Fatal("query calls")
	}
	for _, invalid := range []func() error{
		func() error { _, e := j.NFOPolicy(nil, a, accountUserID); return e }, func() error { _, _, e := j.SetNFOPolicy(c, a, accountUserID, "mode", 1, "read-write"); return e },
		func() error { _, e := j.NFOSummary(c, a, "invalid"); return e }, func() error { _, e := j.Images(c, a, "invalid"); return e },
		func() error { _, e := j.NFOObservations(c, a, accountUserID, "invalid", 20); return e }, func() error { _, e := j.NFOObservations(c, a, accountUserID, "", 51); return e },
		func() error { _, e := j.NFOIssues(c, a, accountUserID, accountTargetID, 65, 32); return e }, func() error { _, e := j.NFOIssues(c, a, accountUserID, accountTargetID, 0, 33); return e },
	} {
		if invalid() != domain.ErrInvalid {
			t.Fatal("invalid query accepted")
		}
	}
	cancelled, cancel := context.WithCancel(c)
	cancel()
	for _, call := range []func() error{
		func() error { _, e := j.NFOPolicy(cancelled, a, accountUserID); return e }, func() error { _, _, e := j.SetNFOPolicy(cancelled, a, accountUserID, "mode", 1, "off"); return e },
		func() error { _, e := j.NFOSummary(cancelled, a, accountUserID); return e }, func() error { _, e := j.Images(cancelled, a, accountUserID); return e },
		func() error { _, e := j.NFOObservations(cancelled, a, accountUserID, "", 20); return e }, func() error { _, e := j.NFOIssues(cancelled, a, accountUserID, accountTargetID, 0, 32); return e },
	} {
		if !errors.Is(call(), context.Canceled) {
			t.Fatal("query cancellation lost")
		}
	}
	if calls != 6 {
		t.Fatal("invalid or cancelled query reached storage")
	}
}
