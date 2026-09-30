package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const probeJobID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
const probeLibraryID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
const probeRootID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
const probeToolID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

var probeDigest = strings.Repeat("a", 64)

type probeExecutionFake struct {
	app.ProbeExecutionRepository
	claim   func(context.Context, string, bool, time.Duration, bool) (domain.JobLease, error)
	load    func(context.Context, domain.JobLease) (domain.ProbeWork, error)
	begin   func(context.Context, domain.JobLease) (domain.ProbePhase, error)
	page    func(context.Context, domain.JobLease, int) (domain.ProbePage, error)
	lookup  func(context.Context, domain.JobLease, domain.ProbePageToken, []domain.ProbeCandidate) ([]domain.ProbeLookup, error)
	acquire func(context.Context, domain.JobLease, domain.ProbePageToken, domain.ProbeCandidate) (domain.ProbeLease, error)
	commit  func(context.Context, domain.JobLease, domain.ProbePageToken, []domain.ProbeCompletion) (domain.ProbePhase, error)
	release func(context.Context, domain.JobLease, domain.ProbeLease) error
	finish  func(context.Context, domain.JobLease) (domain.ProbePhase, error)
	abort   func(context.Context, domain.JobLease, domain.ProbePhaseError) error
}

func (f *probeExecutionFake) ClaimJobWithProbe(c context.Context, o string, b bool, d time.Duration, cap bool) (domain.JobLease, error) {
	return f.claim(c, o, b, d, cap)
}
func (f *probeExecutionFake) LoadProbeWork(c context.Context, l domain.JobLease) (domain.ProbeWork, error) {
	return f.load(c, l)
}
func (f *probeExecutionFake) BeginRequestedProbePhase(c context.Context, l domain.JobLease) (domain.ProbePhase, error) {
	return f.begin(c, l)
}
func (f *probeExecutionFake) NextProbePage(c context.Context, l domain.JobLease, n int) (domain.ProbePage, error) {
	return f.page(c, l, n)
}
func (f *probeExecutionFake) LookupProbeBatch(c context.Context, l domain.JobLease, p domain.ProbePageToken, v []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
	return f.lookup(c, l, p, v)
}
func (f *probeExecutionFake) AcquireProbe(c context.Context, l domain.JobLease, p domain.ProbePageToken, v domain.ProbeCandidate) (domain.ProbeLease, error) {
	return f.acquire(c, l, p, v)
}
func (f *probeExecutionFake) CommitProbeBatch(c context.Context, l domain.JobLease, p domain.ProbePageToken, v []domain.ProbeCompletion) (domain.ProbePhase, error) {
	return f.commit(c, l, p, v)
}
func (f *probeExecutionFake) ReleaseProbeLease(c context.Context, l domain.JobLease, v domain.ProbeLease) error {
	return f.release(c, l, v)
}
func (f *probeExecutionFake) FinishProbePhase(c context.Context, l domain.JobLease) (domain.ProbePhase, error) {
	return f.finish(c, l)
}
func (f *probeExecutionFake) AbortProbeRequest(c context.Context, l domain.JobLease, e domain.ProbePhaseError) error {
	return f.abort(c, l, e)
}

type metadataProberFake struct {
	digest  string
	inspect func(context.Context, domain.ProbeSource) (domain.ProbeStamp, error)
	probe   func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error)
}

func (f *metadataProberFake) IdentityDigest() string { return f.digest }
func (f *metadataProberFake) Inspect(c context.Context, s domain.ProbeSource) (domain.ProbeStamp, error) {
	return f.inspect(c, s)
}
func (f *metadataProberFake) Probe(c context.Context, s domain.ProbeSource) (domain.ProbeObservation, error) {
	return f.probe(c, s)
}

func workerStamp() domain.ProbeStamp {
	return domain.ProbeStamp{Size: 7, ModifiedUnixNano: 123, Fingerprint: strings.Repeat("d", 64), FingerprintVersion: domain.ProbeFingerprintVersion}
}
func workerObservation() domain.ProbeObservation {
	return domain.ProbeObservation{Stamp: workerStamp(), IdentityDigest: probeDigest, Metadata: domain.MediaMetadata{Streams: []domain.MediaStream{{Index: 0, Kind: "video", Video: &domain.MediaVideo{}}}}}
}
func workerRequest(l domain.JobLease) domain.ProbeRequest {
	return domain.ProbeRequest{JobID: l.Job.ID, LibraryID: l.Job.LibraryID, Identity: domain.ProbeIdentityRef{ID: probeToolID, Digest: probeDigest}, Intent: domain.ProbeIntent{Scope: domain.ProbeScopeIncremental}, LibraryGeneration: 1}
}
func workerPhase(request domain.ProbeRequest) domain.ProbePhase {
	return domain.ProbePhase{JobID: request.JobID, LibraryID: request.LibraryID, Start: domain.ProbePhaseStart{Identity: request.Identity, Scope: request.Intent.Scope, TargetItemID: request.Intent.TargetItemID}, LibraryGeneration: request.LibraryGeneration, TargetItemGeneration: request.TargetItemGeneration, State: domain.ProbePhaseRunning, Token: domain.ProbePageToken{Revision: 1}}
}
func workerEntry(i int) domain.ProbeEntry {
	path := fmt.Sprintf("entry-%02d.mkv", i)
	return domain.ProbeEntry{Inventory: domain.InventoryEntry{ID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1), RootID: probeRootID, Path: path, Kind: "video", Size: 7, ModifiedUnixNano: 123}, Source: domain.ProbeSource{RootPath: "private-root", RelativePath: path}}
}
func workerChild(l domain.JobLease, c domain.ProbeCandidate, generation int64) domain.ProbeLease {
	return domain.ProbeLease{InventoryID: c.InventoryID, RootID: probeRootID, Path: fmt.Sprintf("entry-%02d.mkv", mustWorkerIndex(c.InventoryID)), LibraryID: l.Job.LibraryID, LibraryGeneration: 1, RootGeneration: 1, Identity: domain.ProbeIdentityRef{ID: probeToolID, Digest: probeDigest}, Stamp: c.Stamp, Owner: l.Owner, Generation: generation, JobID: l.Job.ID, JobGeneration: l.Generation, ExpiresAt: time.Now().Add(time.Minute)}
}
func mustWorkerIndex(id string) int { var i int; _, _ = fmt.Sscanf(id[:8], "%d", &i); return i - 1 }

type probeWorkerFixture struct {
	base                                                   jobFixture
	repo                                                   *probeExecutionFake
	prober                                                 *metadataProberFake
	lease                                                  domain.JobLease
	request                                                domain.ProbeRequest
	phase                                                  domain.ProbePhase
	work                                                   domain.ProbeWork
	entries                                                []domain.ProbeEntry
	kinds                                                  map[string]string
	batches                                                [][]domain.ProbeCompletion
	offset                                                 int
	acquires, releases, probes, inspections, begins, scans int
	aborts                                                 []domain.ProbePhaseError
}

// One coordinator owns this fixture's mutable state. Concurrent worker tests
// below use dedicated atomic/channel fakes instead of this single-job model.
func newProbeWorkerFixture(t *testing.T, n int) *probeWorkerFixture {
	t.Helper()
	f := &probeWorkerFixture{base: oneJob(t), repo: &probeExecutionFake{}, prober: &metadataProberFake{digest: probeDigest}, kinds: map[string]string{}}
	f.lease = domain.JobLease{Job: domain.Job{ID: probeJobID, LibraryID: probeLibraryID}, Owner: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Generation: 7}
	f.request = workerRequest(f.lease)
	f.phase = workerPhase(f.request)
	f.work = domain.ProbeWork{Request: &f.request, Phase: &f.phase}
	for i := 0; i < n; i++ {
		f.entries = append(f.entries, workerEntry(i))
	}
	f.repo.load = func(c context.Context, _ domain.JobLease) (domain.ProbeWork, error) {
		checkDBDeadline(t, c)
		return f.work, nil
	}
	f.repo.begin = func(c context.Context, _ domain.JobLease) (domain.ProbePhase, error) {
		checkDBDeadline(t, c)
		f.begins++
		return f.phase, nil
	}
	f.repo.page = func(c context.Context, _ domain.JobLease, n int) (domain.ProbePage, error) {
		checkDBDeadline(t, c)
		if n > 16 {
			t.Error("unbounded page")
		}
		if f.offset == len(f.entries) {
			return domain.ProbePage{}, domain.ErrNotFound
		}
		end := min(f.offset+n, len(f.entries))
		return domain.ProbePage{Token: f.phase.Token, Entries: append([]domain.ProbeEntry(nil), f.entries[f.offset:end]...)}, nil
	}
	f.repo.lookup = func(c context.Context, _ domain.JobLease, p domain.ProbePageToken, v []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
		checkDBDeadline(t, c)
		if p != f.phase.Token || len(v) > 16 {
			t.Error("lookup checkpoint/bound")
		}
		out := make([]domain.ProbeLookup, len(v))
		for i, x := range v {
			kind := f.kinds[x.InventoryID]
			if kind == "" {
				kind = domain.ProbeLookupMiss
			}
			out[i] = domain.ProbeLookup{InventoryID: x.InventoryID, Kind: kind}
		}
		return out, nil
	}
	f.repo.acquire = func(c context.Context, l domain.JobLease, p domain.ProbePageToken, v domain.ProbeCandidate) (domain.ProbeLease, error) {
		checkDBDeadline(t, c)
		if p != f.phase.Token || v.InventoryID != f.entries[f.offset].Inventory.ID {
			t.Error("non-head reservation")
		}
		f.acquires++
		return workerChild(l, v, int64(f.acquires)), nil
	}
	f.repo.commit = func(c context.Context, _ domain.JobLease, p domain.ProbePageToken, v []domain.ProbeCompletion) (domain.ProbePhase, error) {
		checkDBDeadline(t, c)
		if p != f.phase.Token || domain.ValidateProbeCompletionBatch(v) != nil {
			t.Error("invalid commit")
		}
		for i, x := range v {
			if x.Candidate.InventoryID != f.entries[f.offset+i].Inventory.ID {
				t.Error("skipped prefix")
			}
			switch x.Kind {
			case domain.ProbeCompletionHit:
				f.phase.Progress.Hits++
			case domain.ProbeCompletionNegativeHit:
				f.phase.Progress.NegativeHits++
			case domain.ProbeCompletionSucceeded:
				f.phase.Progress.Succeeded++
			case domain.ProbeCompletionFailed:
				f.phase.Progress.Failed++
			case domain.ProbeCompletionChanged:
				f.phase.Progress.Changed++
			case domain.ProbeCompletionUnavailable:
				f.phase.Progress.Unavailable++
			}
		}
		f.batches = append(f.batches, append([]domain.ProbeCompletion(nil), v...))
		f.offset += len(v)
		f.phase.Progress.Processed += int64(len(v))
		f.phase.Token.Revision++
		f.phase.Token.AfterID = v[len(v)-1].Candidate.InventoryID
		return f.phase, nil
	}
	f.repo.release = func(c context.Context, _ domain.JobLease, _ domain.ProbeLease) error {
		checkDBDeadline(t, c)
		f.releases++
		return nil
	}
	f.repo.finish = func(c context.Context, _ domain.JobLease) (domain.ProbePhase, error) {
		checkDBDeadline(t, c)
		if f.offset != len(f.entries) {
			t.Error("finish skipped remaining inventory")
		}
		f.phase.State = domain.ProbePhaseDone
		return f.phase, nil
	}
	f.repo.abort = func(c context.Context, _ domain.JobLease, e domain.ProbePhaseError) error {
		checkDBContext(t, c)
		f.aborts = append(f.aborts, e)
		return nil
	}
	f.repo.claim = func(c context.Context, o string, b bool, d time.Duration, cap bool) (domain.JobLease, error) {
		if !cap {
			return domain.JobLease{}, domain.ErrNotFound
		}
		l, err := f.base.repo.claim(c, o, b, d)
		l.Job.LibraryID = probeLibraryID
		return l, err
	}
	f.prober.inspect = func(c context.Context, _ domain.ProbeSource) (domain.ProbeStamp, error) {
		if _, ok := c.Deadline(); !ok {
			t.Error("Inspect has no independent deadline")
		}
		f.inspections++
		return workerStamp(), nil
	}
	f.prober.probe = func(c context.Context, _ domain.ProbeSource) (domain.ProbeObservation, error) {
		if _, ok := c.Deadline(); !ok {
			t.Error("Probe has no independent deadline")
		}
		f.probes++
		return workerObservation(), nil
	}
	return f
}
func (f *probeWorkerFixture) runner(t *testing.T, clock *testClock, edit func(*Options)) *Runner {
	t.Helper()
	o := DefaultOptions()
	o.Workers = 1
	o.Probe = &ProbeOptions{Repository: f.repo, Prober: f.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1}
	if edit != nil {
		edit(&o)
	}
	scan := scannerFunc(func(c context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		f.scans++
		return doneScanner(c, d, emit)
	})
	return makeRunner(t, f.base.repo, scan, clock, o, io.Discard)
}

func TestProbeWorkerResumesDurablePhaseBeforeInventory(t *testing.T) {
	for _, mode := range []string{"plain", "waiting", "running", "done", "aborted_request", "aborted_phase", "unexpected_phase", "wrong_identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 0)
			want := terminal{domain.JobSucceeded, ""}
			scans, begins := 0, 0
			switch mode {
			case "plain":
				f.work = domain.ProbeWork{}
				scans = 1
			case "waiting":
				f.work.Phase = nil
				scans, begins = 1, 1
			case "done":
				f.phase.State = domain.ProbePhaseDone
			case "aborted_request":
				f.request.ErrorCode = domain.ProbePhaseCapacity
				want = terminal{domain.JobFailed, "scan_unavailable"}
			case "aborted_phase":
				f.phase.State = domain.ProbePhaseAborted
				f.phase.ErrorCode = domain.ProbePhaseInvalidated
				want = terminal{domain.JobFailed, "scan_unavailable"}
			case "unexpected_phase":
				f.work.Request = nil
				want = terminal{domain.JobFailed, "scan_unavailable"}
			case "wrong_identity":
				f.request.Identity.Digest = strings.Repeat("b", 64)
				f.phase.Start.Identity = f.request.Identity
				want = terminal{domain.JobFailed, "scan_unavailable"}
			}
			r := f.runner(t, newTestClock(), nil)
			r.run(context.Background(), f.lease)
			if got := receive(t, f.base.terminal); got != want {
				t.Fatalf("terminal=%v", got)
			}
			if f.scans != scans || f.begins != begins || f.probes != 0 {
				t.Fatalf("resume scans=%d begins=%d probes=%d", f.scans, f.begins, f.probes)
			}
			if mode == "aborted_request" && len(f.aborts) != 0 {
				t.Fatal("persisted abort was rewritten")
			}
		})
	}
}

func TestProbeWorkerMixedPrefixAndBoundedHits(t *testing.T) {
	t.Run("mixed", func(t *testing.T) {
		f := newProbeWorkerFixture(t, 5)
		f.kinds[f.entries[0].Inventory.ID] = domain.ProbeLookupHit
		f.kinds[f.entries[1].Inventory.ID] = domain.ProbeLookupNegativeHit
		f.kinds[f.entries[3].Inventory.ID] = domain.ProbeLookupHit
		inspect := f.prober.inspect
		f.prober.inspect = func(c context.Context, s domain.ProbeSource) (domain.ProbeStamp, error) {
			v, e := inspect(c, s)
			if s.RelativePath == "entry-04.mkv" {
				v.Size++
			}
			return v, e
		}
		r := f.runner(t, newTestClock(), nil)
		r.run(context.Background(), f.lease)
		if receive(t, f.base.terminal).state != domain.JobSucceeded {
			t.Fatal("mixed job failed")
		}
		if f.probes != 1 || f.inspections != 6 || len(f.batches) != 4 || len(f.batches[0]) != 2 || f.batches[1][0].Kind != domain.ProbeCompletionSucceeded || f.batches[3][0].Kind != domain.ProbeCompletionChanged {
			t.Fatal("mixed prefix reordered or repeated work")
		}
	})
	t.Run("33_hits", func(t *testing.T) {
		f := newProbeWorkerFixture(t, 33)
		for _, e := range f.entries {
			f.kinds[e.Inventory.ID] = domain.ProbeLookupHit
		}
		r := f.runner(t, newTestClock(), nil)
		r.run(context.Background(), f.lease)
		receive(t, f.base.terminal)
		if f.probes != 0 || f.acquires != 0 || f.inspections != 33 || len(f.batches) != 3 || len(f.batches[0]) != 16 || len(f.batches[1]) != 16 || len(f.batches[2]) != 1 {
			t.Fatal("hit batching is not bounded or called a process")
		}
	})
}

func TestProbeWorkerNegativeRequiresStableFinalInspection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		code    domain.ProbeFailureCode
	}{{"media", domain.ErrProbeFailed, domain.ProbeFailureMedia}, {"metadata", domain.ErrProbeMetadataInvalid, domain.ProbeFailureMetadataInvalid}, {"metadata_limit", domain.ErrProbeMetadataLimit, domain.ProbeFailureMetadataLimit}, {"output_limit", domain.ErrProbeOutputLimit, domain.ProbeFailureOutputLimit}, {"timeout", context.DeadlineExceeded, domain.ProbeFailureTimeout}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
				return workerObservation(), tc.failure
			}
			r := f.runner(t, newTestClock(), nil)
			r.run(context.Background(), f.lease)
			if receive(t, f.base.terminal).state != domain.JobSucceeded {
				t.Fatal("one media error aborted job")
			}
			if len(f.batches) != 1 || f.batches[0][0].FailureCode != tc.code || f.batches[0][0].Metadata != nil || f.inspections != 2 {
				t.Fatal("failure was not re-inspected or leaked its observation")
			}
		})
	}
	for _, mode := range []string{"changed", "unavailable", "inspect_timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			calls := 0
			f.prober.inspect = func(context.Context, domain.ProbeSource) (domain.ProbeStamp, error) {
				calls++
				if calls == 1 {
					return workerStamp(), nil
				}
				if mode == "unavailable" {
					return domain.ProbeStamp{}, domain.ErrProbeInputUnavailable
				}
				if mode == "inspect_timeout" {
					return domain.ProbeStamp{}, context.DeadlineExceeded
				}
				s := workerStamp()
				s.Fingerprint = strings.Repeat("e", 64)
				return s, nil
			}
			f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
				return domain.ProbeObservation{}, domain.ErrProbeFailed
			}
			r := f.runner(t, newTestClock(), nil)
			r.run(context.Background(), f.lease)
			receive(t, f.base.terminal)
			b := f.batches[0][0]
			want := domain.ProbeCompletionUnavailable
			if mode == "changed" {
				want = domain.ProbeCompletionChanged
			}
			if b.Kind != want || b.FailureCode != "" || b.Metadata != nil {
				t.Fatal("unstable source was negative cached")
			}
		})
	}
}

func TestProbeWorkerRuntimeFailureLatchesCapability(t *testing.T) {
	for _, mode := range []string{"unavailable", "unknown", "panic", "identity", "invalid_metadata"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
				switch mode {
				case "panic":
					panic("private root and diagnostic")
				case "identity":
					v := workerObservation()
					v.IdentityDigest = strings.Repeat("b", 64)
					return v, nil
				case "invalid_metadata":
					v := workerObservation()
					v.Metadata = domain.MediaMetadata{}
					return v, nil
				case "unknown":
					return workerObservation(), errors.New("private process stderr")
				}
				return workerObservation(), domain.ErrProbeRuntimeUnavailable
			}
			var disabled atomic.Int32
			r := f.runner(t, newTestClock(), func(o *Options) { o.Probe.OnRuntimeUnavailable = func() { disabled.Add(1) } })
			r.run(context.Background(), f.lease)
			if receive(t, f.base.terminal) != (terminal{domain.JobFailed, "scan_unavailable"}) || disabled.Load() != 1 || r.probeAvailable() || len(f.batches) != 0 || len(f.aborts) != 1 {
				t.Fatal("runtime failure was cached or left capability enabled")
			}
			want := domain.ProbePhaseRuntimeUnavailable
			if mode == "identity" {
				want = domain.ProbePhaseIdentityMismatch
			}
			if f.aborts[0] != want {
				t.Fatal("wrong durable failure code")
			}
		})
	}
}

func TestProbeWorkerBusyReleasesBeforeBackoff(t *testing.T) {
	for _, where := range []string{"lookup", "acquire", "process"} {
		t.Run(where, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			clock := newTestClock()
			calls := 0
			switch where {
			case "lookup":
				old := f.repo.lookup
				f.repo.lookup = func(c context.Context, l domain.JobLease, p domain.ProbePageToken, v []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
					calls++
					if calls == 1 {
						return []domain.ProbeLookup{{InventoryID: v[0].InventoryID, Kind: domain.ProbeLookupBusy}}, nil
					}
					return old(c, l, p, v)
				}
			case "acquire":
				old := f.repo.acquire
				f.repo.acquire = func(c context.Context, l domain.JobLease, p domain.ProbePageToken, v domain.ProbeCandidate) (domain.ProbeLease, error) {
					calls++
					if calls == 1 {
						return domain.ProbeLease{}, domain.ErrProbeBusy
					}
					return old(c, l, p, v)
				}
			case "process":
				old := f.prober.probe
				f.prober.probe = func(c context.Context, s domain.ProbeSource) (domain.ProbeObservation, error) {
					calls++
					if calls == 1 {
						return domain.ProbeObservation{}, domain.ErrProbeBusy
					}
					return old(c, s)
				}
			}
			r := f.runner(t, clock, nil)
			done := make(chan struct{})
			go func() { defer close(done); r.run(context.Background(), f.lease) }()
			clock.waitFor(t, DefaultOptions().PollInterval, 1)
			if len(r.probeGate) != 0 {
				t.Error("gate held during backoff")
			}
			if where == "process" && f.releases != 1 {
				t.Error("child retained during process backoff")
			}
			clock.fire(DefaultOptions().PollInterval)
			receive(t, done)
			if receive(t, f.base.terminal).state != domain.JobSucceeded || len(f.batches) != 1 {
				t.Fatal("busy retry did not complete once")
			}
		})
	}
}

func TestProbeWorkerOptionsAndHeartbeat(t *testing.T) {
	f := newProbeWorkerFixture(t, 0)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := DefaultOptions()
	opts.Probe = &ProbeOptions{Repository: f.repo, Prober: f.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 2}
	for _, mutate := range []func(*ProbeOptions){func(p *ProbeOptions) { p.Repository = nil }, func(p *ProbeOptions) { p.Prober = nil }, func(p *ProbeOptions) { p.MaxConcurrent = 0 }, func(p *ProbeOptions) { p.MaxConcurrent = 9 }, func(p *ProbeOptions) { p.LeaseDuration = time.Second }, func(p *ProbeOptions) { p.FileTimeout = time.Millisecond }, func(p *ProbeOptions) { p.FileTimeout = 6 * time.Minute }} {
		p := *opts.Probe
		mutate(&p)
		bad := opts
		bad.Probe = &p
		if _, err := New(f.base.repo, scannerFunc(doneScanner), bad, logger); err != domain.ErrInvalid {
			t.Fatal("invalid probe options accepted")
		}
	}
	bad := opts
	bad.DBOperationTimeout = 4 * time.Second
	if _, err := New(f.base.repo, scannerFunc(doneScanner), bad, logger); err != domain.ErrInvalid {
		t.Fatal("DB timeout exceeded child heartbeat")
	}
	r, err := New(f.base.repo, scannerFunc(doneScanner), opts, logger)
	if err != nil {
		t.Fatal(err)
	}
	opts.Probe.MaxConcurrent = 8
	if cap(r.probeGate) != 2 || r.options.Probe.FileTimeout != 30*time.Second || r.heartbeatInterval() != 10*time.Second/3 {
		t.Fatal("probe options not copied or heartbeat ignored child lease")
	}
}

// Two coordinators deliberately overlap. Channels make a pending gate waiter
// observable without sleeps, and Stop must join the synchronous prober first.
func TestProbeWorkerGateAndRuntimeStopAcrossWorkers(t *testing.T) {
	for _, mode := range []string{"shutdown", "runtime_fault"} {
		t.Run(mode, func(t *testing.T) {
			clock := newTestClock()
			var claims, acquires, probes, active, callbacks atomic.Int32
			inspected := make(chan struct{}, 2)
			entered := make(chan struct{}, 1)
			allow := make(chan struct{})
			released := make(chan struct{}, 2)
			finished := make(chan struct{}, 2)
			base := &executionFake{heartbeat: func(context.Context, domain.JobLease, time.Duration) (bool, error) { return false, nil }, release: func(context.Context, domain.JobLease) error {
				if active.Load() != 0 {
					t.Error("parent released before process join")
				}
				released <- struct{}{}
				return nil
			}, finish: func(context.Context, domain.JobLease, string, string) error { finished <- struct{}{}; return nil }}
			repo := &probeExecutionFake{}
			repo.claim = func(_ context.Context, o string, _ bool, _ time.Duration, cap bool) (domain.JobLease, error) {
				if !cap {
					return domain.JobLease{}, domain.ErrNotFound
				}
				n := claims.Add(1)
				if n > 2 {
					return domain.JobLease{}, domain.ErrNotFound
				}
				return domain.JobLease{Job: domain.Job{ID: fmt.Sprintf("%08d-bbbb-4bbb-8bbb-bbbbbbbbbbbb", n), LibraryID: fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", n)}, Owner: o, Generation: 1}, nil
			}
			repo.load = func(_ context.Context, l domain.JobLease) (domain.ProbeWork, error) {
				q := workerRequest(l)
				p := workerPhase(q)
				return domain.ProbeWork{Request: &q, Phase: &p}, nil
			}
			repo.page = func(_ context.Context, l domain.JobLease, _ int) (domain.ProbePage, error) {
				return domain.ProbePage{Token: workerPhase(workerRequest(l)).Token, Entries: []domain.ProbeEntry{workerEntry(0)}}, nil
			}
			repo.lookup = func(_ context.Context, _ domain.JobLease, _ domain.ProbePageToken, v []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
				return []domain.ProbeLookup{{InventoryID: v[0].InventoryID, Kind: domain.ProbeLookupMiss}}, nil
			}
			repo.acquire = func(_ context.Context, l domain.JobLease, _ domain.ProbePageToken, v domain.ProbeCandidate) (domain.ProbeLease, error) {
				acquires.Add(1)
				return workerChild(l, v, 1), nil
			}
			repo.abort = func(context.Context, domain.JobLease, domain.ProbePhaseError) error { return nil }
			prober := &metadataProberFake{digest: probeDigest, inspect: func(context.Context, domain.ProbeSource) (domain.ProbeStamp, error) {
				inspected <- struct{}{}
				return workerStamp(), nil
			}, probe: func(c context.Context, _ domain.ProbeSource) (domain.ProbeObservation, error) {
				probes.Add(1)
				active.Add(1)
				defer active.Add(-1)
				entered <- struct{}{}
				select {
				case <-c.Done():
					return domain.ProbeObservation{}, c.Err()
				case <-allow:
					return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
				}
			}}
			opts := DefaultOptions()
			opts.Probe = &ProbeOptions{Repository: repo, Prober: prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1, OnRuntimeUnavailable: func() { callbacks.Add(1) }}
			r := makeRunner(t, base, scannerFunc(doneScanner), clock, opts, io.Discard)
			startRunner(t, r)
			receive(t, inspected)
			receive(t, inspected)
			receive(t, entered)
			if acquires.Load() != 1 || probes.Load() != 1 {
				t.Fatal("child lease acquired before gate")
			}
			if mode == "shutdown" {
				stopRunner(t, r)
				receive(t, released)
				receive(t, released)
				if callbacks.Load() != 0 {
					t.Fatal("shutdown mislabeled runtime fault")
				}
			} else {
				close(allow)
				receive(t, finished)
				receive(t, finished)
				stopRunner(t, r)
				if callbacks.Load() != 1 || r.probeAvailable() {
					t.Fatal("runtime fault was not latched once")
				}
			}
			if acquires.Load() != 1 || probes.Load() != 1 || active.Load() != 0 || len(r.probeGate) != 0 {
				t.Fatal("waiting worker started after disable/cancel or retained a slot")
			}
		})
	}
}

type probeClaimOnlyFake struct {
	*executionFake
	capability chan bool
}

func (f *probeClaimOnlyFake) ClaimJobWithProbe(_ context.Context, _ string, _ bool, _ time.Duration, available bool) (domain.JobLease, error) {
	f.capability <- available
	return domain.JobLease{}, domain.ErrNotFound
}

func TestProbeWorkerDisabledClaimsAreExplicit(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			clock := newTestClock()
			f := newProbeWorkerFixture(t, 0)
			capabilities := make(chan bool, 1)
			opts := DefaultOptions()
			opts.Workers = 1
			var base app.JobExecutionRepository = &probeClaimOnlyFake{executionFake: f.base.repo, capability: capabilities}
			if configured {
				opts.Probe = &ProbeOptions{Repository: f.repo, Prober: f.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1, Available: func() bool { return false }}
				f.repo.claim = func(_ context.Context, _ string, _ bool, _ time.Duration, cap bool) (domain.JobLease, error) {
					capabilities <- cap
					return domain.JobLease{}, domain.ErrNotFound
				}
			}
			r := makeRunner(t, base, scannerFunc(doneScanner), clock, opts, io.Discard)
			startRunner(t, r)
			if receive(t, capabilities) {
				t.Fatal("disabled worker advertised probe capability")
			}
			clock.waitFor(t, opts.PollInterval, 1)
			stopRunner(t, r)
		})
	}
}

func TestProbeWorkerInitialInspectionIsNotMediaFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{{"missing", domain.ErrProbeInputUnavailable, domain.ProbeCompletionUnavailable}, {"changed", domain.ErrProbeSourceChanged, domain.ProbeCompletionChanged}, {"timeout", context.DeadlineExceeded, domain.ProbeCompletionUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			f.prober.inspect = func(context.Context, domain.ProbeSource) (domain.ProbeStamp, error) {
				return domain.ProbeStamp{}, tc.err
			}
			f.repo.lookup = func(context.Context, domain.JobLease, domain.ProbePageToken, []domain.ProbeCandidate) ([]domain.ProbeLookup, error) {
				t.Error("invalid stamp was looked up")
				return nil, domain.ErrInvalid
			}
			r := f.runner(t, newTestClock(), nil)
			r.run(context.Background(), f.lease)
			receive(t, f.base.terminal)
			if len(f.batches) != 1 || f.batches[0][0].Kind != tc.kind || f.batches[0][0].Candidate.Stamp != (domain.ProbeStamp{}) || f.acquires != 0 || f.probes != 0 {
				t.Fatal("source error was probed or negative cached")
			}
		})
	}
}

func TestProbeWorkerRepositoryFailuresDoNotBecomeBadMedia(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		code     domain.ProbePhaseError
		terminal bool
	}{{"capacity", domain.ErrProbeCacheCapacity, domain.ProbePhaseCapacity, true}, {"scope", domain.ErrProbeInvalidated, domain.ProbePhaseInvalidated, true}, {"identity", domain.ErrProbeIdentityMismatch, domain.ProbePhaseIdentityMismatch, true}, {"database", domain.ErrDatabase, "", true}, {"db_timeout", context.DeadlineExceeded, "", true}, {"parent_lost", domain.ErrJobLeaseLost, "", false}, {"child_lost", domain.ErrProbeLeaseLost, "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			f.repo.acquire = func(context.Context, domain.JobLease, domain.ProbePageToken, domain.ProbeCandidate) (domain.ProbeLease, error) {
				return domain.ProbeLease{}, tc.err
			}
			r := f.runner(t, newTestClock(), nil)
			r.run(context.Background(), f.lease)
			if tc.terminal {
				if receive(t, f.base.terminal) != (terminal{domain.JobFailed, "scan_unavailable"}) {
					t.Fatal("storage failure mislabeled as media/outer timeout")
				}
			} else {
				select {
				case <-f.base.terminal:
					t.Fatal("lost ownership finalized")
				default:
				}
			}
			if tc.code != "" {
				if len(f.aborts) != 1 || f.aborts[0] != tc.code {
					t.Fatal("wrong durable phase reason")
				}
			} else if len(f.aborts) != 0 {
				t.Fatal("database/lease error fabricated probe abort")
			}
			if len(f.batches) != 0 || f.probes != 0 {
				t.Fatal("storage failure produced media outcome")
			}
		})
	}
	t.Run("abort_persistence_failure", func(t *testing.T) {
		f := newProbeWorkerFixture(t, 1)
		f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
			return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
		}
		f.repo.abort = func(context.Context, domain.JobLease, domain.ProbePhaseError) error { return domain.ErrDatabase }
		r := f.runner(t, newTestClock(), nil)
		r.run(context.Background(), f.lease)
		select {
		case <-f.base.terminal:
			t.Fatal("failed durable abort was finalized")
		default:
		}
	})
}

func TestProbeWorkerParentCancellationWinsOverItemTimeout(t *testing.T) {
	for _, mode := range []string{"job_timeout", "heartbeat_lost", "final_inspect_timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newProbeWorkerFixture(t, 1)
			clock := newTestClock()
			entered := make(chan struct{}, 1)
			exited := make(chan struct{})
			done := make(chan struct{})
			block := func(c context.Context) { entered <- struct{}{}; <-c.Done(); close(exited) }
			if mode == "final_inspect_timeout" {
				inspections := 0
				f.prober.inspect = func(c context.Context, _ domain.ProbeSource) (domain.ProbeStamp, error) {
					inspections++
					if inspections == 1 {
						return workerStamp(), nil
					}
					block(c)
					return domain.ProbeStamp{}, c.Err()
				}
			} else {
				f.prober.probe = func(c context.Context, _ domain.ProbeSource) (domain.ProbeObservation, error) {
					block(c)
					return domain.ProbeObservation{}, context.DeadlineExceeded
				}
			}
			if mode == "heartbeat_lost" {
				f.base.repo.heartbeat = func(context.Context, domain.JobLease, time.Duration) (bool, error) {
					return false, domain.ErrJobLeaseLost
				}
			}
			r := f.runner(t, clock, nil)
			go func() { defer close(done); r.run(context.Background(), f.lease) }()
			receive(t, entered)
			if mode == "heartbeat_lost" {
				clock.fire(r.heartbeatInterval())
			} else {
				clock.fire(r.options.MaxJobRuntime)
			}
			receive(t, exited)
			receive(t, done)
			if mode != "heartbeat_lost" {
				if receive(t, f.base.terminal) != (terminal{domain.JobFailed, "job_timeout"}) {
					t.Fatal("outer timeout was negative cached")
				}
			} else {
				select {
				case <-f.base.terminal:
					t.Fatal("lost parent finalized")
				default:
				}
			}
			if len(f.batches) != 0 || len(f.aborts) != 0 {
				t.Fatal("parent cancellation generated media result or runtime abort")
			}
		})
	}
}

func TestProbeWorkerObservationAndHealthRecheckedBeforeCommit(t *testing.T) {
	t.Run("observation_changed", func(t *testing.T) {
		f := newProbeWorkerFixture(t, 1)
		f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
			v := workerObservation()
			v.Stamp.ModifiedUnixNano++
			return v, nil
		}
		r := f.runner(t, newTestClock(), nil)
		r.run(context.Background(), f.lease)
		receive(t, f.base.terminal)
		if f.batches[0][0].Kind != domain.ProbeCompletionChanged || f.batches[0][0].Metadata != nil {
			t.Fatal("different source observation was cached")
		}
	})
	t.Run("health_changed_during_acquire", func(t *testing.T) {
		f := newProbeWorkerFixture(t, 1)
		var disabled atomic.Bool
		old := f.repo.acquire
		f.repo.acquire = func(c context.Context, l domain.JobLease, p domain.ProbePageToken, v domain.ProbeCandidate) (domain.ProbeLease, error) {
			child, err := old(c, l, p, v)
			disabled.Store(true)
			return child, err
		}
		r := f.runner(t, newTestClock(), func(o *Options) { o.Probe.Available = func() bool { return !disabled.Load() } })
		r.run(context.Background(), f.lease)
		receive(t, f.base.terminal)
		if f.probes != 0 || len(f.batches) != 0 || len(f.aborts) != 1 {
			t.Fatal("process started after runtime became unavailable")
		}
	})
}

func TestProbeWorkerCapabilityCallbackPanicIsSafelyReported(t *testing.T) {
	f := newProbeWorkerFixture(t, 1)
	f.prober.probe = func(context.Context, domain.ProbeSource) (domain.ProbeObservation, error) {
		return domain.ProbeObservation{}, domain.ErrProbeRuntimeUnavailable
	}
	var output bytes.Buffer
	opts := DefaultOptions()
	opts.Workers = 1
	opts.Probe = &ProbeOptions{Repository: f.repo, Prober: f.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1, OnRuntimeUnavailable: func() { panic("private-root credential and callback detail") }}
	r := makeRunner(t, f.base.repo, scannerFunc(doneScanner), newTestClock(), opts, &output)
	r.run(context.Background(), f.lease)
	if receive(t, f.base.terminal) != (terminal{domain.JobFailed, "scan_unavailable"}) || r.probeAvailable() || len(f.aborts) != 1 {
		t.Fatal("callback failure bypassed runtime disable or durable abort")
	}
	if !strings.Contains(output.String(), "probe_callback_failed") || strings.Contains(output.String(), "private-root") || strings.Contains(output.String(), "credential") {
		t.Fatal("callback panic was hidden or sensitive payload was logged")
	}
}
