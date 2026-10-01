package app

import (
	"context"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type probeJobFake struct {
	ProbeJobRepository
	submit func(context.Context, domain.Actor, string, string, string, domain.ProbeIntent, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error)
	retry  func(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error)
}

func (f probeJobFake) SubmitScanJob(c context.Context, a domain.Actor, l, k, p string, i domain.ProbeIntent, policy domain.JobPolicy, id *domain.ProbeIdentity) (domain.Job, bool, error) {
	return f.submit(c, a, l, k, p, i, policy, id)
}
func (f probeJobFake) RetryScanJob(c context.Context, a domain.Actor, j, k string, policy domain.JobPolicy, id *domain.ProbeIdentity) (domain.Job, bool, error) {
	return f.retry(c, a, j, k, policy, id)
}
func appProbeIdentity() domain.ProbeIdentity {
	return domain.ProbeIdentity{Platform: "linux-amd64", VendorVersion: "vendor-1", UpstreamVersion: "9.0.2", SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("b", 64), RuntimeSHA256: strings.Repeat("c", 64), ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion, ArgumentsSHA256: strings.Repeat("d", 64), SandboxVersion: "sandbox-v1", FingerprintVersion: domain.ProbeFingerprintVersion}
}

func TestProbeAdmissionCopiesIdentityAndRetainsReplayWhenUnavailable(t *testing.T) {
	cap := domain.ProbeCapability{Enabled: true, Available: true}
	identity := appProbeIdentity()
	trusted := identity
	calls := 0
	repo := probeJobFake{submit: func(ctx context.Context, a domain.Actor, l, k, p string, intent domain.ProbeIntent, policy domain.JobPolicy, id *domain.ProbeIdentity) (domain.Job, bool, error) {
		calls++
		if a != accountTestActor() || l != accountUserID || intent.Scope != domain.ProbeScopeIncremental || p != "manual" || policy != jobTestPolicy() {
			t.Fatal("intent/actor changed")
		}
		if cap.Available {
			if id == nil || *id != trusted {
				t.Fatal("trusted identity mutated")
			}
			id.VendorVersion = "repository mutation"
		} else if id != nil {
			t.Fatal("unavailable runtime supplied identity")
		}
		if k == "replay" {
			return domain.Job{ID: accountTargetID}, true, nil
		}
		if id == nil {
			return domain.Job{}, false, domain.ErrProbeDisabled
		}
		return domain.Job{ID: accountTargetID}, false, nil
	}}
	jobs, err := NewJobsWithProbe(jobRepositoryFake{}, jobTestPolicy(), repo, &identity, func() domain.ProbeCapability { return cap })
	if err != nil {
		t.Fatal(err)
	}
	identity.VendorVersion = "caller mutation"
	for range 2 {
		if _, _, err = jobs.SubmitScan(context.Background(), accountTestActor(), accountUserID, "new", "manual", true); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []domain.ProbeCapability{{Enabled: true}, {}} {
		cap = state
		if job, replay, err := jobs.SubmitScan(context.Background(), accountTestActor(), accountUserID, "replay", "manual", true); err != nil || !replay || job.ID != accountTargetID {
			t.Fatal("unavailable runtime blocked persisted replay")
		}
		want := domain.ErrProbeDisabled
		if cap.Enabled {
			want = domain.ErrProbeRuntimeUnavailable
		}
		if _, _, err := jobs.SubmitScan(context.Background(), accountTestActor(), accountUserID, "new", "manual", true); err != want {
			t.Fatalf("admission error=%v want=%v", err, want)
		}
	}
	if calls != 6 {
		t.Fatal("repository count")
	}
}

func TestProbeRebuildRetryAndPlainIntent(t *testing.T) {
	calls := 0
	repo := probeJobFake{submit: func(_ context.Context, _ domain.Actor, l, k, p string, intent domain.ProbeIntent, _ domain.JobPolicy, id *domain.ProbeIdentity) (domain.Job, bool, error) {
		calls++
		if id != nil {
			t.Fatal("disabled instance supplied identity")
		}
		switch k {
		case "plain":
			if l != accountUserID || intent != (domain.ProbeIntent{}) {
				t.Fatal("plain scan gained probe intent")
			}
		case "library":
			if l != accountUserID || intent.Scope != domain.ProbeScopeLibraryRebuild || intent.TargetItemID != "" {
				t.Fatal("library rebuild changed")
			}
		case "item":
			if l != "" || intent.Scope != domain.ProbeScopeItemRebuild || intent.TargetItemID != accountUserID {
				t.Fatal("item rebuild changed")
			}
		default:
			t.Fatal("unexpected request")
		}
		return domain.Job{ID: accountTargetID}, true, nil
	}, retry: func(_ context.Context, _ domain.Actor, j, k string, _ domain.JobPolicy, id *domain.ProbeIdentity) (domain.Job, bool, error) {
		calls++
		if j != accountTargetID || id != nil {
			t.Fatal("retry target/runtime changed")
		}
		if k == "new-probe" {
			return domain.Job{}, false, domain.ErrProbeDisabled
		}
		return domain.Job{ID: j}, true, nil
	}}
	j, err := NewJobsWithProbe(jobRepositoryFake{}, jobTestPolicy(), repo, nil, func() domain.ProbeCapability { return domain.ProbeCapability{Enabled: true} })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = j.Submit(context.Background(), accountTestActor(), accountUserID, "plain", "manual"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key  string
		item bool
	}{{"library", false}, {"item", true}} {
		if _, replay, err := j.RebuildProbe(context.Background(), accountTestActor(), accountUserID, tc.key, "manual", tc.item); err != nil || !replay {
			t.Fatal("rebuild replay rejected")
		}
	}
	if _, _, err = j.Retry(context.Background(), accountTestActor(), accountTargetID, "plain-retry"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = j.Retry(context.Background(), accountTestActor(), accountTargetID, "new-probe"); err != domain.ErrProbeRuntimeUnavailable {
		t.Fatal("new retry did not fail closed")
	}
	if calls != 5 {
		t.Fatal("call count")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = j.SubmitScan(ctx, accountTestActor(), accountUserID, "x", "manual", true); err != context.Canceled {
		t.Fatal("cancellation lost")
	}
	if _, _, err = j.RebuildProbe(ctx, accountTestActor(), accountUserID, "x", "manual", false); err != context.Canceled {
		t.Fatal("cancellation lost")
	}
	if _, _, err = j.RebuildProbe(context.Background(), accountTestActor(), "bad", "x", "manual", true); err != domain.ErrInvalid {
		t.Fatal("invalid item accepted")
	}
	if calls != 5 {
		t.Fatal("invalid request reached repository")
	}
}

func TestLegacyJobsCannotAdmitProbeAndSummarizeDisabled(t *testing.T) {
	j := newJobService(t, jobRepositoryFake{get: func(context.Context, domain.Actor, string) (domain.Job, error) {
		return domain.Job{ID: accountTargetID, LibraryID: accountUserID}, nil
	}})
	if _, _, err := j.SubmitScan(context.Background(), accountTestActor(), accountUserID, "x", "manual", true); err != domain.ErrProbeDisabled {
		t.Fatal("legacy admitted probe")
	}
	if _, _, err := j.RebuildProbe(context.Background(), accountTestActor(), accountUserID, "x", "manual", false); err != domain.ErrProbeDisabled {
		t.Fatal("legacy admitted rebuild")
	}
	summary, err := j.ProbeSummary(context.Background(), accountTestActor(), accountTargetID)
	if err != nil || summary.Enabled || summary.Phase != domain.ProbeSummaryDisabled || summary.JobID != accountTargetID || summary.LibraryID != accountUserID {
		t.Fatal("legacy summary")
	}
	if _, err = NewJobsWithProbe(jobRepositoryFake{}, jobTestPolicy(), probeJobFake{}, nil, func() domain.ProbeCapability { return domain.ProbeCapability{Enabled: true, Available: true} }); err != domain.ErrInvalid {
		t.Fatal("available runtime lacks identity")
	}
}
