package jobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoExecutionFake struct {
	app.NFOExecutionRepository
	load   func(context.Context, domain.JobLease) (domain.NFOWork, error)
	begin  func(context.Context, domain.JobLease) (domain.NFOPhase, error)
	page   func(context.Context, domain.JobLease, int) (domain.NFOPage, error)
	lookup func(context.Context, domain.JobLease, domain.NFOPageToken, []domain.NFOCandidate) ([]domain.NFOLookup, error)
	commit func(context.Context, domain.JobLease, domain.NFOPageToken, []domain.NFOCompletion) (domain.NFOPhase, error)
	finish func(context.Context, domain.JobLease) (domain.NFOPhase, error)
	abort  func(context.Context, domain.JobLease, domain.NFOPhaseError) error
	claim  func(context.Context, string, bool, time.Duration, domain.ScanCapabilities) (domain.JobLease, error)
}

func (f *nfoExecutionFake) LoadNFOWork(c context.Context, l domain.JobLease) (domain.NFOWork, error) {
	return f.load(c, l)
}
func (f *nfoExecutionFake) BeginRequestedNFOPhase(c context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	return f.begin(c, l)
}
func (f *nfoExecutionFake) NextNFOPage(c context.Context, l domain.JobLease, n int) (domain.NFOPage, error) {
	return f.page(c, l, n)
}
func (f *nfoExecutionFake) LookupNFOBatch(c context.Context, l domain.JobLease, p domain.NFOPageToken, v []domain.NFOCandidate) ([]domain.NFOLookup, error) {
	return f.lookup(c, l, p, v)
}
func (f *nfoExecutionFake) CommitNFOBatch(c context.Context, l domain.JobLease, p domain.NFOPageToken, v []domain.NFOCompletion) (domain.NFOPhase, error) {
	return f.commit(c, l, p, v)
}
func (f *nfoExecutionFake) FinishNFOPhase(c context.Context, l domain.JobLease) (domain.NFOPhase, error) {
	return f.finish(c, l)
}
func (f *nfoExecutionFake) AbortNFORequest(c context.Context, l domain.JobLease, e domain.NFOPhaseError) error {
	return f.abort(c, l, e)
}
func (f *nfoExecutionFake) ClaimJobWithCapabilities(c context.Context, o string, b bool, d time.Duration, v domain.ScanCapabilities) (domain.JobLease, error) {
	return f.claim(c, o, b, d, v)
}

type nfoReaderFake struct {
	identity domain.NFOIdentity
	read     func(context.Context, domain.NFOSource) (app.NFOReadSource, error)
}

func (f *nfoReaderFake) Identity() domain.NFOIdentity { return f.identity }
func (f *nfoReaderFake) Read(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
	return f.read(c, s)
}

type nfoReadFake struct {
	stamp domain.NFOStamp
	parse func(context.Context) (domain.NFOValidationSummary, error)
}

func (f *nfoReadFake) Stamp() domain.NFOStamp { return f.stamp }
func (f *nfoReadFake) Parse(c context.Context) (domain.NFOValidationSummary, error) {
	return f.parse(c)
}
func nfoWorkerStamp() domain.NFOStamp {
	return domain.NFOStamp{Size: 7, ModifiedUnixNano: 123, SHA256: strings.Repeat("d", 64), FingerprintVersion: domain.NFOFingerprintVersion}
}

type claimStagesFake struct {
	app.JobExecutionRepository
	claim func(context.Context, domain.ScanCapabilities) (domain.JobLease, error)
}

func (f claimStagesFake) ClaimJobWithCapabilities(c context.Context, _ string, _ bool, _ time.Duration, v domain.ScanCapabilities) (domain.JobLease, error) {
	return f.claim(c, v)
}

func TestNFOClaimCannotConsumeUnsupportedIntent(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-reader", true: "reader-disabled"}[configured], func(t *testing.T) {
			f := newNFOWorkerFixture(t, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			claim := func(c context.Context, caps domain.ScanCapabilities) (domain.JobLease, error) {
				checkDBDeadline(t, c)
				called = true
				if caps.NFO || caps.Probe {
					t.Error("unavailable stage admitted")
				}
				cancel()
				return domain.JobLease{}, domain.ErrNotFound
			}
			var r *Runner
			if configured {
				f.repo.claim = func(c context.Context, _ string, _ bool, _ time.Duration, caps domain.ScanCapabilities) (domain.JobLease, error) {
					return claim(c, caps)
				}
				r = f.runner(t, func(o *Options) { o.NFO.Available = func() bool { return false } })
			} else {
				o := DefaultOptions()
				o.Owner = f.lease.Owner
				var err error
				r, err = New(claimStagesFake{JobExecutionRepository: f.base.repo, claim: claim}, scannerFunc(func(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
					t.Fatal("scanner used")
					return nil
				}), o, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if err != nil {
					t.Fatal(err)
				}
			}
			r.work(ctx)
			if !called {
				t.Fatal("capability claim not used")
			}
		})
	}
}

func TestNFOStorageFaultsDoNotCommitPartialResults(t *testing.T) {
	for _, fault := range []struct {
		name string
		err  error
		code domain.NFOPhaseError
	}{
		{"capacity", domain.ErrNFOCacheCapacity, domain.NFOPhaseCapacity}, {"invalidated", domain.ErrNFOInvalidated, domain.NFOPhaseInvalidated}, {"identity", domain.ErrNFOIdentityMismatch, domain.NFOPhaseIdentityMismatch}, {"database", domain.ErrDatabase, ""},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := newNFOWorkerFixture(t, 1)
			f.repo.commit = func(context.Context, domain.JobLease, domain.NFOPageToken, []domain.NFOCompletion) (domain.NFOPhase, error) {
				return domain.NFOPhase{}, fault.err
			}
			r := f.runner(t, nil)
			r.run(context.Background(), f.lease)
			if len(f.batches) != 0 || f.phase.Progress.Processed != 0 {
				t.Fatal("failed commit published progress")
			}
			if fault.code != "" && (len(f.aborts) != 1 || f.aborts[0] != fault.code) || fault.code == "" && len(f.aborts) != 0 {
				t.Fatal("incorrect persistent failure classification")
			}
			terminal := receive(t, f.base.terminal)
			if terminal.state != domain.JobFailed || terminal.code != "scan_unavailable" {
				t.Fatal("safe terminal failure not recorded")
			}
		})
	}
}
func nfoWorkerSummary() domain.NFOValidationSummary {
	return domain.NFOValidationSummary{SchemaVersion: 1, Status: domain.NFOStatusValid, Encoding: "UTF-8", Root: "movie", Entries: 1, Issues: []domain.NFOIssue{}}
}

type nfoWorkerFixture struct {
	base                                 jobFixture
	repo                                 *nfoExecutionFake
	reader                               *nfoReaderFake
	lease                                domain.JobLease
	request                              domain.NFORequest
	phase                                domain.NFOPhase
	work                                 domain.NFOWork
	entries                              []domain.NFOEntry
	kinds                                map[string]string
	batches                              [][]domain.NFOCompletion
	aborts                               []domain.NFOPhaseError
	offset, reads, parses, begins, scans int
}

func newNFOWorkerFixture(t *testing.T, n int) *nfoWorkerFixture {
	t.Helper()
	f := &nfoWorkerFixture{base: oneJob(t), repo: &nfoExecutionFake{}, reader: &nfoReaderFake{identity: domain.DefaultNFOIdentity()}, kinds: map[string]string{}}
	f.lease = domain.JobLease{Job: domain.Job{ID: probeJobID, LibraryID: probeLibraryID}, Owner: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Generation: 7}
	digest, _ := domain.NFOIdentityDigest(f.reader.identity)
	f.request = domain.NFORequest{JobID: probeJobID, LibraryID: probeLibraryID, Requested: true, Mode: domain.NFOModeReadOnly, Identity: f.reader.identity, IdentityDigest: digest, LibraryGeneration: 1}
	f.phase = domain.NFOPhase{JobID: probeJobID, LibraryID: probeLibraryID, Mode: domain.NFOModeReadOnly, State: domain.NFOPhaseRunning, Identity: f.reader.identity, IdentityDigest: digest, LibraryGeneration: 1, Token: domain.NFOPageToken{Revision: 1}}
	f.work = domain.NFOWork{Request: &f.request, Phase: &f.phase}
	for i := range n {
		entry := workerEntry(i)
		entry.Inventory.Kind = "nfo"
		entry.Inventory.Path = strings.TrimSuffix(entry.Inventory.Path, ".mkv") + ".nfo"
		f.entries = append(f.entries, domain.NFOEntry{Inventory: entry.Inventory, Source: domain.NFOSource{RootPath: "private-root", RelativePath: entry.Inventory.Path}})
	}
	f.repo.load = func(c context.Context, _ domain.JobLease) (domain.NFOWork, error) {
		checkDBDeadline(t, c)
		return f.work, nil
	}
	f.repo.begin = func(c context.Context, _ domain.JobLease) (domain.NFOPhase, error) {
		checkDBDeadline(t, c)
		f.begins++
		f.phase.State = domain.NFOPhaseRunning
		f.phase.Token.Revision++
		return f.phase, nil
	}
	f.repo.page = func(c context.Context, _ domain.JobLease, n int) (domain.NFOPage, error) {
		checkDBDeadline(t, c)
		if n > domain.NFOBatchMax {
			t.Error("unbounded NFO page")
		}
		if f.offset == len(f.entries) {
			return domain.NFOPage{}, domain.ErrNotFound
		}
		return domain.NFOPage{Token: f.phase.Token, Entries: f.entries[f.offset:min(f.offset+n, len(f.entries))]}, nil
	}
	f.repo.lookup = func(c context.Context, _ domain.JobLease, p domain.NFOPageToken, v []domain.NFOCandidate) ([]domain.NFOLookup, error) {
		checkDBDeadline(t, c)
		if p != f.phase.Token || len(v) != 1 {
			t.Error("lookup not bounded to current item")
		}
		kind := f.kinds[v[0].InventoryID]
		if kind == "" {
			kind = domain.NFOLookupMiss
		}
		return []domain.NFOLookup{{InventoryID: v[0].InventoryID, Kind: kind}}, nil
	}
	f.repo.commit = func(c context.Context, _ domain.JobLease, p domain.NFOPageToken, v []domain.NFOCompletion) (domain.NFOPhase, error) {
		checkDBDeadline(t, c)
		if p != f.phase.Token || domain.ValidateNFOCompletionBatch(v) != nil || len(v) != 1 || v[0].Candidate.InventoryID != f.entries[f.offset].Inventory.ID {
			t.Error("invalid NFO checkpoint")
		}
		x := v[0]
		f.batches = append(f.batches, append([]domain.NFOCompletion(nil), v...))
		f.offset++
		f.phase.Progress.Processed++
		f.phase.Token.Revision++
		f.phase.Token.AfterID = x.Candidate.InventoryID
		switch x.Kind {
		case domain.NFOCompletionHit:
			f.phase.Progress.Hits++
			f.phase.Progress.Valid++
		case domain.NFOCompletionNegativeHit:
			f.phase.Progress.NegativeHits++
			f.phase.Progress.Invalid++
		case domain.NFOCompletionParsed:
			f.phase.Progress.Parsed++
			if x.Summary.Status == domain.NFOStatusValid {
				f.phase.Progress.Valid++
			} else {
				f.phase.Progress.Invalid++
			}
			if x.Summary.WarningCount > 0 {
				f.phase.Progress.WarningFiles++
			}
		case domain.NFOCompletionChanged:
			f.phase.Progress.Changed++
		case domain.NFOCompletionUnavailable:
			f.phase.Progress.Unavailable++
		case domain.NFOCompletionRejected:
			f.phase.Progress.Rejected++
		}
		return f.phase, nil
	}
	f.repo.finish = func(c context.Context, _ domain.JobLease) (domain.NFOPhase, error) {
		checkDBDeadline(t, c)
		if f.offset != len(f.entries) {
			t.Error("premature NFO finish")
		}
		f.phase.State = domain.NFOPhaseDone
		return f.phase, nil
	}
	f.repo.abort = func(c context.Context, _ domain.JobLease, e domain.NFOPhaseError) error {
		checkDBContext(t, c)
		f.aborts = append(f.aborts, e)
		return nil
	}
	f.repo.claim = func(c context.Context, o string, b bool, d time.Duration, v domain.ScanCapabilities) (domain.JobLease, error) {
		if !v.NFO {
			return domain.JobLease{}, domain.ErrNotFound
		}
		l, e := f.base.repo.claim(c, o, b, d)
		l.Job.LibraryID = probeLibraryID
		return l, e
	}
	f.reader.read = func(c context.Context, _ domain.NFOSource) (app.NFOReadSource, error) {
		if _, ok := c.Deadline(); !ok {
			t.Error("NFO read has no per-call budget")
		}
		f.reads++
		return &nfoReadFake{stamp: nfoWorkerStamp(), parse: func(c context.Context) (domain.NFOValidationSummary, error) {
			if _, ok := c.Deadline(); !ok {
				t.Error("NFO parse has no per-call budget")
			}
			f.parses++
			return nfoWorkerSummary(), nil
		}}, nil
	}
	return f
}
func (f *nfoWorkerFixture) runner(t *testing.T, edit func(*Options)) *Runner {
	o := DefaultOptions()
	o.Workers = 1
	o.NFO = &NFOOptions{Repository: f.repo, Reader: f.reader, MaxConcurrent: 2}
	if edit != nil {
		edit(&o)
	}
	scanner := scannerFunc(func(c context.Context, d domain.ScanDirectory, emit func(domain.ScanBatch) error) error {
		f.scans++
		return doneScanner(c, d, emit)
	})
	return makeRunner(t, f.base.repo, scanner, newTestClock(), o, io.Discard)
}

func TestNFOWorkerColdWarmInvalidAndFinalRead(t *testing.T) {
	f := newNFOWorkerFixture(t, 5)
	f.kinds[f.entries[0].Inventory.ID] = domain.NFOLookupHit
	f.kinds[f.entries[1].Inventory.ID] = domain.NFOLookupNegativeHit
	read := f.reader.read
	f.reader.read = func(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
		v, e := read(c, s)
		if s.RelativePath == f.entries[3].Source.RelativePath {
			v.(*nfoReadFake).parse = func(context.Context) (domain.NFOValidationSummary, error) {
				f.parses++
				return domain.NFOValidationSummary{SchemaVersion: 1, Status: domain.NFOStatusInvalid, Encoding: "unknown", Root: "unknown", FailureCode: domain.NFOFailureInvalidXML, Issues: []domain.NFOIssue{}}, nil
			}
		}
		if s.RelativePath == f.entries[4].Source.RelativePath && f.reads%2 == 0 {
			v.(*nfoReadFake).stamp.SHA256 = strings.Repeat("e", 64)
		}
		return v, e
	}
	r := f.runner(t, nil)
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got != (terminal{domain.JobSucceeded, ""}) {
		t.Fatal(got)
	}
	if f.scans != 0 || f.reads != 10 || f.parses != 3 || len(f.batches) != 5 || f.phase.Progress.Hits != 1 || f.phase.Progress.NegativeHits != 1 || f.phase.Progress.Parsed != 2 || f.phase.Progress.Invalid != 2 || f.phase.Progress.Changed != 1 {
		t.Fatalf("NFO work calls reads=%d parses=%d progress=%+v", f.reads, f.parses, f.phase.Progress)
	}
	if last := f.batches[4][0]; last.Summary != nil || last.Candidate.Stamp != (domain.NFOStamp{}) {
		t.Fatal("changed source persisted stale parsed data")
	}
}

func TestNFOWorkerSourceFailuresAreNotInvalidXML(t *testing.T) {
	for _, stage := range []string{"initial", "parse", "final-hit", "final-miss"} {
		for _, tc := range []struct {
			name string
			err  error
			kind string
		}{{"changed", domain.ErrNFOSourceChanged, domain.NFOCompletionChanged}, {"unavailable", domain.ErrNFOInputUnavailable, domain.NFOCompletionUnavailable}, {"timeout", context.DeadlineExceeded, domain.NFOCompletionUnavailable}, {"large", domain.ErrNFOSourceLimit, domain.NFOCompletionRejected}} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				f := newNFOWorkerFixture(t, 1)
				read := f.reader.read
				if stage == "final-hit" {
					f.kinds[f.entries[0].Inventory.ID] = domain.NFOLookupHit
				}
				f.reader.read = func(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
					v, e := read(c, s)
					if stage == "initial" || strings.HasPrefix(stage, "final") && f.reads == 2 {
						return nil, tc.err
					}
					if stage == "parse" {
						v.(*nfoReadFake).parse = func(context.Context) (domain.NFOValidationSummary, error) { return nfoWorkerSummary(), tc.err }
					}
					return v, e
				}
				r := f.runner(t, nil)
				r.run(context.Background(), f.lease)
				if got := receive(t, f.base.terminal); got.state != domain.JobSucceeded {
					t.Fatal(got)
				}
				if len(f.batches) != 1 || f.batches[0][0].Kind != tc.kind || f.batches[0][0].Summary != nil || f.batches[0][0].Candidate.Stamp != (domain.NFOStamp{}) || f.phase.Progress.Invalid != 0 {
					t.Fatal("source error became XML negative result")
				}
			})
		}
	}
}

func TestNFOWorkerResumeAndLegacyIntent(t *testing.T) {
	for _, mode := range []string{"legacy", "off", "waiting", "running", "done", "orphan", "aborted-request", "aborted-phase", "identity", "wrong-phase"} {
		t.Run(mode, func(t *testing.T) {
			f := newNFOWorkerFixture(t, 0)
			scans, begins := 0, 0
			want := domain.JobSucceeded
			switch mode {
			case "legacy":
				f.work = domain.NFOWork{}
				scans = 1
			case "off":
				f.request.Requested = false
				f.request.Mode = domain.NFOModeOff
				f.phase.Mode = domain.NFOModeOff
				f.phase.State = domain.NFOPhaseAborted
				f.phase.ErrorCode = domain.NFOPhaseDisabled
				scans = 1
			case "waiting":
				f.phase.State = domain.NFOPhaseWaiting
				scans, begins = 1, 1
			case "done":
				f.phase.State = domain.NFOPhaseDone
			case "orphan":
				f.work.Request = nil
				want = domain.JobFailed
			case "aborted-request":
				f.request.ErrorCode = domain.NFOPhaseCapacity
				want = domain.JobFailed
			case "aborted-phase":
				f.phase.State = domain.NFOPhaseAborted
				f.phase.ErrorCode = domain.NFOPhaseInvalidated
				want = domain.JobFailed
			case "identity":
				f.reader.identity.MaxSourceBytes--
				want = domain.JobFailed
			case "wrong-phase":
				f.phase.LibraryGeneration++
				want = domain.JobFailed
			}
			r := f.runner(t, nil)
			r.run(context.Background(), f.lease)
			if got := receive(t, f.base.terminal); got.state != want {
				t.Fatalf("terminal %+v", got)
			}
			if f.scans != scans || f.begins != begins || f.reads != 0 {
				t.Fatal("resume rescanned or changed frozen intent")
			}
			if mode == "aborted-request" && len(f.aborts) != 0 {
				t.Fatal("persisted abort repeated")
			}
		})
	}
}

func TestNFOWorkerStagesOrderAndCompletedProbeCannotBypassNFO(t *testing.T) {
	f := newNFOWorkerFixture(t, 1)
	f.phase.State = domain.NFOPhaseWaiting
	probe := newProbeWorkerFixture(t, 0)
	probe.work.Phase = nil
	begin := probe.repo.begin
	probe.repo.begin = func(c context.Context, l domain.JobLease) (domain.ProbePhase, error) {
		if f.phase.State != domain.NFOPhaseDone {
			t.Error("probe began before NFO finished")
		}
		return begin(c, l)
	}
	r := f.runner(t, func(o *Options) {
		o.Probe = &ProbeOptions{Repository: probe.repo, Prober: probe.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1}
	})
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got.state != domain.JobSucceeded || f.scans != 1 || f.begins != 1 || probe.begins != 1 || f.parses != 1 {
		t.Fatal("inventory/NFO/probe ordering")
	}
	f = newNFOWorkerFixture(t, 0)
	f.phase.State = domain.NFOPhaseWaiting
	probe = newProbeWorkerFixture(t, 0)
	probe.phase.State = domain.ProbePhaseDone
	r = f.runner(t, func(o *Options) {
		o.Probe = &ProbeOptions{Repository: probe.repo, Prober: probe.prober, LeaseDuration: 10 * time.Second, MaxConcurrent: 1}
	})
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got.state != domain.JobFailed || f.scans != 0 {
		t.Fatal("completed probe bypassed unfinished NFO")
	}
}

func TestNFOWorkerRuntimeFaultDisablesAdmissionAndRedactsPanic(t *testing.T) {
	for _, stage := range []string{"read", "parse", "panic", "nil-source", "bad-summary", "callback-panic"} {
		t.Run(stage, func(t *testing.T) {
			f := newNFOWorkerFixture(t, 1)
			read := f.reader.read
			f.reader.read = func(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
				if stage == "panic" {
					panic("private-reader-secret")
				}
				if stage == "nil-source" {
					return nil, nil
				}
				if stage == "read" || stage == "callback-panic" {
					return nil, errors.New("private-reader-secret")
				}
				v, e := read(c, s)
				v.(*nfoReadFake).parse = func(context.Context) (domain.NFOValidationSummary, error) {
					if stage == "parse" {
						return domain.NFOValidationSummary{}, errors.New("private-parser-secret")
					}
					return domain.NFOValidationSummary{Status: "wrong"}, nil
				}
				return v, e
			}
			callbacks := 0
			r := f.runner(t, func(o *Options) {
				o.NFO.OnRuntimeUnavailable = func() {
					callbacks++
					if stage == "callback-panic" {
						panic("private-callback-secret")
					}
				}
			})
			var output bytes.Buffer
			r.logger = slog.New(slog.NewJSONHandler(&output, nil))
			r.run(context.Background(), f.lease)
			if got := receive(t, f.base.terminal); got.state != domain.JobFailed || r.nfoAvailable() || callbacks != 1 || len(f.batches) != 0 || len(f.aborts) != 1 || f.aborts[0] != domain.NFOPhaseUnavailable {
				t.Fatal("runtime fault admitted result or future execution")
			}
			if strings.Contains(output.String(), "private-") || stage == "callback-panic" && !strings.Contains(output.String(), "nfo_callback_failed") {
				t.Fatal("callback panic was swallowed or leaked")
			}
		})
	}
}

func TestNFOWorkerGateCancellationJoinsReadCallsAndReleasesSlots(t *testing.T) {
	f := newNFOWorkerFixture(t, 1)
	entered := make(chan struct{}, 2)
	var reads atomic.Int32
	f.reader.read = func(c context.Context, _ domain.NFOSource) (app.NFOReadSource, error) {
		reads.Add(1)
		entered <- struct{}{}
		<-c.Done()
		return nil, c.Err()
	}
	r := f.runner(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 3)
	for range 3 {
		go func() { _, err, _ := r.nfoItem(ctx, f.lease, f.request, f.phase.Token, f.entries[0]); results <- err }()
	}
	for range 2 {
		receive(t, entered)
	}
	cancel()
	for range 3 {
		if err := receive(t, results); err != context.Canceled {
			t.Fatal("parent cancellation was not preserved")
		}
	}
	if reads.Load() != 2 || len(r.nfoGate) != 0 || len(f.batches) != 0 {
		t.Fatal("gate/cancellation admitted extra reads or leaked a slot")
	}
}

func TestNFOWorkerParentCancelNeverBecomesPerFileFailure(t *testing.T) {
	f := newNFOWorkerFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read := f.reader.read
	f.reader.read = func(c context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
		v, e := read(c, s)
		cancel()
		return v, e
	}
	r := f.runner(t, nil)
	r.run(ctx, f.lease)
	receive(t, f.base.released)
	if len(f.batches) != 0 || len(f.aborts) != 0 {
		t.Fatal("shutdown cancellation persisted file/phase failure")
	}
	select {
	case <-f.base.terminal:
		t.Fatal("shutdown finalized parent")
	default:
	}
}

func TestNFOWorkerConflictReloadsWithoutRepeatingCommittedCounters(t *testing.T) {
	f := newNFOWorkerFixture(t, 1)
	commit := f.repo.commit
	attempts := 0
	f.repo.commit = func(c context.Context, l domain.JobLease, p domain.NFOPageToken, v []domain.NFOCompletion) (domain.NFOPhase, error) {
		attempts++
		if attempts == 1 {
			return domain.NFOPhase{}, domain.ErrConflict
		}
		return commit(c, l, p, v)
	}
	r := f.runner(t, nil)
	r.options.Clock = realClock{}
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got.state != domain.JobSucceeded || attempts != 2 || f.reads != 4 || f.parses != 2 || f.phase.Progress.Processed != 1 {
		t.Fatal("conflict reused stale observation or counted twice")
	}
}

func TestNFOWorkerOptionsAndFinishMappingDrift(t *testing.T) {
	for _, change := range []func(*NFOOptions){func(o *NFOOptions) { o.Repository = nil }, func(o *NFOOptions) { o.Reader = nil }, func(o *NFOOptions) { o.MaxConcurrent = 0 }, func(o *NFOOptions) { o.MaxConcurrent = 3 }, func(o *NFOOptions) { o.FileTimeout = time.Millisecond }, func(o *NFOOptions) { o.FileTimeout = 6 * time.Minute }} {
		f := newNFOWorkerFixture(t, 0)
		o := DefaultOptions()
		o.NFO = &NFOOptions{Repository: f.repo, Reader: f.reader, MaxConcurrent: 2}
		change(o.NFO)
		if _, e := New(f.base.repo, scannerFunc(doneScanner), o, slog.New(slog.NewTextHandler(io.Discard, nil))); e != domain.ErrInvalid {
			t.Fatal("invalid NFO budget accepted")
		}
	}
	f := newNFOWorkerFixture(t, 0)
	f.phase.State = domain.NFOPhaseDone
	attempts := 0
	f.base.repo.finish = func(c context.Context, _ domain.JobLease, state, code string) error {
		checkDBContext(t, c)
		attempts++
		if attempts == 1 {
			if state != domain.JobSucceeded {
				t.Error("first finish not success")
			}
			return domain.ErrInventoryInvalidated
		}
		f.base.terminal <- terminal{state, code}
		return nil
	}
	r := f.runner(t, nil)
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got != (terminal{domain.JobFailed, "scan_unavailable"}) || attempts != 2 {
		t.Fatal("mapping drift stranded/retried parent")
	}
}
