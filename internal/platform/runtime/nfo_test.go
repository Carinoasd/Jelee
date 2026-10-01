package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type nfoMaintenanceFake struct {
	ensured int
	err     error
	sweep   func(context.Context, int) (domain.NFOSweepResult, error)
}

func (f *nfoMaintenanceFake) EnsureNFOCachePolicy(_ context.Context, p domain.NFOCachePolicy) error {
	if p != domain.DefaultNFOCachePolicy() {
		panic("changed policy")
	}
	f.ensured++
	return f.err
}
func (f *nfoMaintenanceFake) SweepNFOCache(c context.Context, n int) (domain.NFOSweepResult, error) {
	return f.sweep(c, n)
}

func TestNFOServiceIndependentReaderCountersDisableAndPolicy(t *testing.T) {
	repo := &nfoMaintenanceFake{}
	reader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	s, err := newNFOService(context.Background(), repo, reader)
	if err != nil || !s.Available() || s.Identity() != domain.DefaultNFOIdentity() || repo.ensured != 1 {
		t.Fatal("local NFO service unavailable")
	}
	// No probe factory, executable, platform check or process is involved.
	root := t.TempDir()
	raw := []byte("<movie><title>sample</title></movie>")
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := s.Read(context.Background(), domain.NFOSource{RootPath: root, RelativePath: "movie.nfo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Parse(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := s.Stats(); stats.ReadCalls != 1 || stats.ParseCalls != 1 || stats.CompletedReadBytes != uint64(len(raw)) || stats.ActiveCalls != 0 {
		t.Fatal("runtime lost actual calls")
	}
	s.Disable()
	s.Disable()
	if _, err := s.Read(context.Background(), domain.NFOSource{}); err != domain.ErrNFOReaderUnavailable || s.Available() {
		t.Fatal("disabled reader invoked")
	}
	if s.Stats().ReadCalls != 1 {
		t.Fatal("disabled call changed backend counters")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Read(ctx, domain.NFOSource{}); err != context.Canceled {
		t.Fatal("parent cancellation lost priority")
	}
	if _, err := s.Read(nil, domain.NFOSource{}); err != domain.ErrInvalid {
		t.Fatal("nil context accepted")
	}
	repo.err = errors.New("private-db-secret")
	if service, err := newNFOService(context.Background(), repo, reader); service != nil || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("policy failure leaked or continued")
	}
	if _, err := newNFOService(ctx, repo, reader); err != context.Canceled {
		t.Fatal("startup cancellation lost")
	}
	if _, err := newNFOService(nil, repo, reader); err != domain.ErrInvalid {
		t.Fatal("nil startup context")
	}
	if _, err := newNFOService(context.Background(), nil, reader); err != domain.ErrInvalid {
		t.Fatal("nil repository")
	}
	missing, err := newNFOService(context.Background(), repo, nil)
	if err != nil || missing.Available() || missing.Identity() != (domain.NFOIdentity{}) || missing.Stats() != (nfo.ReaderStats{}) {
		t.Fatal("missing reader exposed capability")
	}
	missing.startMaintenance(context.Background())
	if err := missing.stopMaintenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	var absent *nfoService
	absent.Disable()
	absent.startMaintenance(context.Background())
	if absent.Available() || absent.Stats() != (nfo.ReaderStats{}) || absent.Identity() != (domain.NFOIdentity{}) || absent.stopMaintenance(context.Background()) != nil {
		t.Fatal("nil lifecycle")
	}
}

func TestNFOMaintenanceBoundedRedactedAndJoined(t *testing.T) {
	var output bytes.Buffer
	repo := &nfoMaintenanceFake{sweep: func(c context.Context, n int) (domain.NFOSweepResult, error) {
		deadline, ok := c.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second || n != domain.NFOSweepMax {
			t.Error("unbounded NFO sweep")
		}
		return domain.NFOSweepResult{}, errors.New("private-nfo-db-secret")
	}}
	reader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	s, err := newNFOService(context.Background(), repo, reader)
	if err != nil {
		t.Fatal(err)
	}
	s.logger = slog.New(slog.NewTextHandler(&output, nil))
	s.sweep(context.Background())
	if text := output.String(); !strings.Contains(text, "nfo_maintenance_failed") || strings.Contains(text, "private-") {
		t.Fatal("maintenance logging leaked or lost failure")
	}
	s.startMaintenance(context.Background())
	first := s.done
	s.startMaintenance(context.Background())
	if s.done != first {
		t.Fatal("duplicate NFO sweeper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.stopMaintenance(ctx); err != nil {
		t.Fatal("maintenance did not join")
	}
	select {
	case <-first:
	default:
		t.Fatal("returned before maintenance exit")
	}
	// A caller deadline reports that joining is incomplete; it does not pretend
	// to close a store that an in-flight maintenance query could still use.
	unjoined := &nfoService{done: make(chan struct{}), cancel: func() {}}
	stop, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := unjoined.stopMaintenance(stop); err != context.Canceled {
		t.Fatal("unfinished join not reported")
	}
}

func TestStageWorkerStopsBothMaintenancesBeforeWaitingForWorker(t *testing.T) {
	probe := &probeService{cancel: func() {}, done: make(chan struct{})}
	nfo := &nfoService{cancel: func() {}, done: make(chan struct{})}
	probeCancelled, nfoCancelled := false, false
	probe.cancel = func() {
		probeCancelled = true
		select {
		case <-probe.done:
		default:
			close(probe.done)
		}
	}
	nfo.cancel = func() {
		nfoCancelled = true
		select {
		case <-nfo.done:
		default:
			close(nfo.done)
		}
	}
	w := &testStopWorker{stop: func(context.Context) error {
		if !probeCancelled || !nfoCancelled {
			t.Error("maintenance not cancelled before worker join")
		}
		return nil
	}}
	stages := &probeWorker{worker: w, probe: probe, nfo: nfo}
	if err := stages.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type testStopWorker struct{ stop func(context.Context) error }

func (*testStopWorker) Start(context.Context) error    { return nil }
func (w *testStopWorker) Stop(c context.Context) error { return w.stop(c) }
