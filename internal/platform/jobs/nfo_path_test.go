package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// nfoPathReaderFake is a reader that can also classify locations.
type nfoPathReaderFake struct {
	*nfoReaderFake
	inspect func(context.Context, domain.NFOSource) (string, error)
}

func (f nfoPathReaderFake) InspectNFOPath(c context.Context, s domain.NFOSource) (string, error) {
	return f.inspect(c, s)
}

func TestNFOWorkerReportsAnIllegalLocationInsteadOfReadingIt(t *testing.T) {
	f := newNFOWorkerFixture(t, 4)
	codes := map[string]string{f.entries[1].Source.RelativePath: domain.NFOPathSymlink, f.entries[2].Source.RelativePath: domain.NFOPathCaseCollision}
	inspected := 0
	f.reader.read = func(_ context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
		if codes[s.RelativePath] != "" {
			t.Error("a file at an illegal location was read")
		}
		f.reads++
		return &nfoReadFake{stamp: nfoWorkerStamp(), parse: func(context.Context) (domain.NFOValidationSummary, error) {
			f.parses++
			return nfoWorkerSummary(), nil
		}}, nil
	}
	reader := nfoPathReaderFake{f.reader, func(c context.Context, s domain.NFOSource) (string, error) {
		if _, ok := c.Deadline(); !ok {
			t.Error("the inspection has no per-call budget")
		}
		inspected++
		return codes[s.RelativePath], nil
	}}
	r := f.runner(t, func(o *Options) { o.NFO.Reader = reader })
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got != (terminal{domain.JobSucceeded, ""}) {
		t.Fatal(got)
	}
	if inspected != 4 || len(f.batches) != 4 || f.reads != 4 || f.parses != 2 {
		t.Fatalf("inspected %d, committed %d, reads %d, parses %d", inspected, len(f.batches), f.reads, f.parses)
	}
	for i, want := range []string{"", domain.NFOPathSymlink, domain.NFOPathCaseCollision, ""} {
		c := f.batches[i][0]
		if want == "" {
			if c.Summary == nil || len(c.Summary.Issues) != 0 {
				t.Fatalf("entry %d: %+v", i, c)
			}
			continue
		}
		if c.Kind != domain.NFOCompletionParsed || c.Summary == nil || !domain.IsNFOPathSummary(*c.Summary) || c.Summary.Issues[0].Code != want {
			t.Fatalf("entry %d: %+v", i, c)
		}
		// The stored stamp is the inventory's size and time with a digest of the code alone.
		entry := f.entries[i].Inventory
		if c.Candidate.Stamp.Size != entry.Size || c.Candidate.Stamp.ModifiedUnixNano != entry.ModifiedUnixNano || domain.ValidateNFOStamp(c.Candidate.Stamp) != nil || strings.Repeat("d", 64) == c.Candidate.Stamp.SHA256 {
			t.Fatalf("entry %d: stamp %+v", i, c.Candidate.Stamp)
		}
	}
	// Two path issues of different codes never share a digest; equal ones do.
	a, _ := nfoPathCompletion(f.entries[1].Inventory, domain.NFOPathSymlink, domain.NFODefaultSourceBytes)
	b, _ := nfoPathCompletion(f.entries[1].Inventory, domain.NFOPathNotRegular, domain.NFODefaultSourceBytes)
	c, _ := nfoPathCompletion(f.entries[1].Inventory, domain.NFOPathSymlink, domain.NFODefaultSourceBytes)
	if a.Candidate.Stamp.SHA256 == b.Candidate.Stamp.SHA256 || a.Candidate.Stamp != c.Candidate.Stamp {
		t.Fatal("path stamps are not a function of the code")
	}
	// An oversize file keeps the ordinary outcome and an unknown code is no completion.
	big := f.entries[1].Inventory
	big.Size = domain.NFODefaultSourceBytes + 1
	if _, ok := nfoPathCompletion(big, domain.NFOPathSymlink, domain.NFODefaultSourceBytes); ok {
		t.Fatal("an oversize file got a path summary")
	}
	if _, ok := nfoPathCompletion(f.entries[1].Inventory, "nfo_invalid_xml", domain.NFODefaultSourceBytes); ok {
		t.Fatal("a code that is not a path code got a path summary")
	}
}

func TestNFOWorkerPathInspectionFailuresAreRuntimeFaults(t *testing.T) {
	for _, stage := range []string{"error", "panic", "unknown-code"} {
		t.Run(stage, func(t *testing.T) {
			f := newNFOWorkerFixture(t, 1)
			reader := nfoPathReaderFake{f.reader, func(context.Context, domain.NFOSource) (string, error) {
				switch stage {
				case "panic":
					panic("private-inspector-secret")
				case "unknown-code":
					return "nfo_path_made_up", nil
				}
				return "", errors.New("private-inspector-secret")
			}}
			r := f.runner(t, func(o *Options) { o.NFO.Reader = reader })
			var output bytes.Buffer
			r.logger = slog.New(slog.NewJSONHandler(&output, nil))
			r.run(context.Background(), f.lease)
			if got := receive(t, f.base.terminal); got.state != domain.JobFailed || r.nfoAvailable() || len(f.batches) != 0 || len(f.aborts) != 1 || f.aborts[0] != domain.NFOPhaseUnavailable {
				t.Fatal("an inspection fault admitted a result")
			}
			if strings.Contains(output.String(), "private-") {
				t.Fatal("the inspector's text leaked")
			}
		})
	}
}

func TestNFOWorkerWithoutAnInspectorBehavesAsBefore(t *testing.T) {
	f := newNFOWorkerFixture(t, 2)
	r := f.runner(t, nil)
	r.run(context.Background(), f.lease)
	if got := receive(t, f.base.terminal); got.state != domain.JobSucceeded || f.reads != 4 || f.parses != 2 {
		t.Fatalf("%+v reads %d parses %d", got, f.reads, f.parses)
	}
}
