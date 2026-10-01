package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type maintenanceFake struct {
	ensured int
	err     error
	sweep   func(context.Context, int) (domain.ProbeSweepResult, error)
}

func (f *maintenanceFake) EnsureProbePolicy(_ context.Context, p domain.ProbeCachePolicy) error {
	f.ensured++
	if p != domain.DefaultProbeCachePolicy() {
		panic("policy changed")
	}
	return f.err
}
func (f *maintenanceFake) SweepProbeCache(ctx context.Context, n int) (domain.ProbeSweepResult, error) {
	return f.sweep(ctx, n)
}

type runtimeProber struct {
	digest string
	calls  int
}

func (p *runtimeProber) IdentityDigest() string { return p.digest }
func (p *runtimeProber) Inspect(context.Context, domain.ProbeSource) (domain.ProbeStamp, error) {
	p.calls++
	return domain.ProbeStamp{}, domain.ErrProbeInputUnavailable
}
func (p *runtimeProber) Probe(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
	p.calls++
	return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
}
func runtimeIdentity() domain.ProbeIdentity {
	return domain.ProbeIdentity{Platform: "linux-amd64", VendorVersion: "vendor-1", UpstreamVersion: "9.0.2", SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("b", 64), RuntimeSHA256: strings.Repeat("c", 64), ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion, ArgumentsSHA256: strings.Repeat("d", 64), SandboxVersion: "sandbox-v1", FingerprintVersion: domain.ProbeFingerprintVersion}
}

func TestProbeDisabledDoesNotInitializeStorageOrRuntime(t *testing.T) {
	p, err := newProbeService(nil, false, nil, func(context.Context) (preparedProbe, string, error) {
		t.Fatal("disabled factory invoked")
		return preparedProbe{}, "", nil
	})
	if err != nil || p.Available() || p.Capability().Enabled || p.IdentityDigest() != "" {
		t.Fatal("disabled state")
	}
	p.startMaintenance(context.Background())
	if err = p.stopMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Inspect(context.Background(), domain.ProbeSource{}); err != domain.ErrProbeRuntimeUnavailable {
		t.Fatal("disabled Inspect")
	}
	if _, err = p.Probe(context.Background(), domain.ProbeSource{}); err != domain.ErrProbeRuntimeUnavailable {
		t.Fatal("disabled Probe")
	}
	if p.probeCalls.Load() != 0 || p.inspectCalls.Load() != 0 {
		t.Fatal("disabled backend invoked")
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeDegradationUsesOnlyFixedReasonsAndCleansPreparedResources(t *testing.T) {
	for _, reason := range []string{"platform_unsupported", "health_check_failed", "/private/path", "temporary_unavailable"} {
		repo := &maintenanceFake{}
		closed := 0
		p, err := newProbeService(context.Background(), true, repo, func(context.Context) (preparedProbe, string, error) {
			return preparedProbe{close: func() error { closed++; return nil }}, reason, errors.New("private-runtime-error")
		})
		want := reason
		if reason == "/private/path" {
			want = "runtime_unavailable"
		}
		if err != nil || p.Available() || !p.Capability().Enabled || p.Capability().Reason != want || closed != 1 || repo.ensured != 0 {
			t.Fatal("runtime degradation")
		}
	}
	repo := &maintenanceFake{}
	p, err := newProbeService(context.Background(), true, repo, func(context.Context) (preparedProbe, string, error) {
		return preparedProbe{close: func() error { return errors.New("private-cleanup") }}, "health_check_failed", errors.New("private")
	})
	if err != nil || p.Capability().Reason != "cleanup_failed" {
		t.Fatal("cleanup fault discarded")
	}
}

func TestProbeVerifiedIdentityPolicyDisableAndCleanup(t *testing.T) {
	identity := runtimeIdentity()
	digest, _ := domain.ProbeIdentityDigest(identity)
	backend := &runtimeProber{digest: digest}
	repo := &maintenanceFake{}
	closed := 0
	factory := func(context.Context) (preparedProbe, string, error) {
		return preparedProbe{prober: backend, identity: identity, close: func() error { closed++; return nil }}, "", nil
	}
	p, err := newProbeService(context.Background(), true, repo, factory)
	if err != nil || !p.Available() || p.IdentityDigest() != digest || repo.ensured != 1 {
		t.Fatal("verified runtime unavailable")
	}
	identity.VendorVersion = "mutated"
	if p.identity.VendorVersion == identity.VendorVersion {
		t.Fatal("runtime identity mutable")
	}
	_, _ = p.Inspect(context.Background(), domain.ProbeSource{})
	_, _ = p.Probe(context.Background(), domain.ProbeSource{})
	p.Disable()
	_, _ = p.Inspect(context.Background(), domain.ProbeSource{})
	_, _ = p.Probe(context.Background(), domain.ProbeSource{})
	if backend.calls != 2 || p.probeCalls.Load() != 1 || p.inspectCalls.Load() != 1 || p.Available() {
		t.Fatal("disable did not stop calls")
	}
	if p.Close() != nil || p.Close() != nil || closed != 1 {
		t.Fatal("close not exactly once")
	}
	identity = runtimeIdentity()
	repo.err = errors.New("private db secret")
	if p, err = newProbeService(context.Background(), true, repo, factory); p != nil || err == nil || strings.Contains(err.Error(), "secret") || closed != 2 {
		t.Fatal("policy failure not cleaned/redacted")
	}
	repo.err = nil
	backend.digest = strings.Repeat("f", 64)
	if p, err = newProbeService(context.Background(), true, repo, factory); err != nil || p.Available() || closed != 3 || repo.ensured != 2 {
		t.Fatal("mismatched identity admitted")
	}
}

func TestProbeMaintenanceIsBoundedCancelledAndRedacted(t *testing.T) {
	var output bytes.Buffer
	repo := &maintenanceFake{sweep: func(ctx context.Context, n int) (domain.ProbeSweepResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second || n != domain.ProbeSweepMax {
			t.Fatal("unbounded maintenance")
		}
		return domain.ProbeSweepResult{}, errors.New("private-database-path")
	}}
	p := &probeService{backend: &runtimeProber{}, repository: repo, logger: slog.New(slog.NewTextHandler(&output, nil))}
	p.sweep(context.Background())
	if !strings.Contains(output.String(), "probe_maintenance_failed") || strings.Contains(output.String(), "private") {
		t.Fatal("maintenance failure lost/leaked")
	}
	p.startMaintenance(context.Background())
	first := p.done
	p.startMaintenance(context.Background())
	if first != p.done {
		t.Fatal("duplicate sweeper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.stopMaintenance(ctx); err != nil {
		t.Fatal("maintenance did not join", err)
	}
}
