package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// This runs on both platforms: the repository is controlled, but file opening,
// full hashes, XML parsing, summary projection and worker calls are real.
func TestNFOOnlyWorkerUsesRealReaderWithoutProbe(t *testing.T) {
	f := newNFOWorkerFixture(t, 2)
	root := t.TempDir()
	for i, body := range []string{`<movie><title>private fixture</title><year>2024</year></movie>`, `<movie><title>broken</movie>`} {
		name := f.entries[i].Inventory.Path
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		f.entries[i].Inventory.Size = info.Size()
		f.entries[i].Inventory.ModifiedUnixNano = info.ModTime().UnixNano()
		f.entries[i].Source.RootPath = root
	}
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := nfo.NewObservedReader(reader)
	if err != nil {
		t.Fatal(err)
	}
	r := f.runner(t, func(o *Options) { o.NFO.Reader = observed })
	r.run(context.Background(), f.lease)
	result := receive(t, f.base.terminal)
	if result.state != domain.JobSucceeded || f.phase.Progress.Valid != 1 || f.phase.Progress.Invalid != 1 || f.phase.Progress.Parsed != 2 {
		t.Fatal("real NFO-only results missing")
	}
	if len(f.batches) != 2 {
		t.Fatal("missing checkpoints")
	}
	for _, b := range f.batches {
		if b[0].Summary == nil || domain.ValidateNFOSummary(*b[0].Summary) != nil || domain.ValidateNFOStamp(b[0].Candidate.Stamp) != nil {
			t.Fatal("invalid real summary or full-content stamp")
		}
	}
	stats := observed.Stats()
	if stats.ReadCalls != 4 || stats.HashCompletions != 4 || stats.ParseCalls != 2 || stats.ActiveCalls != 0 || r.probeAvailable() {
		t.Fatalf("actual NFO stats/probe availability: %+v", stats)
	}
}
