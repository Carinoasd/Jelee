//go:build jelee_probe_tests

package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestImagesSoakStreamTypedSequence(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := newImagesSoakStream(writer, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for _, event := range []struct {
			at   int64
			data any
		}{
			{0, imagesSoakStart{Scope: "smoke"}},
			{1, imagesSoakSampleBlock{Samples: []residentSample{{ElapsedNanos: 1}}}},
			{3, imagesSoakWorkEnd{WorkStartedNanos: 1, WorkFinishedNanos: 3, CompletedRounds: 2}},
			{4, imagesSoakSampleBlock{Samples: []residentSample{{ElapsedNanos: 4, Phase: "shutdown"}}}},
		} {
			if err := s.write(ctx, event.at, event.data); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	_ = reader.SetReadDeadline(time.Now().Add(time.Second))
	r := bufio.NewReader(reader)
	for i, kind := range []string{"start", "samples", "workEnd", "samples"} {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Event imagesSoakEnvelope `json:"imagesSoakEvent"`
		}
		if json.Unmarshal(line, &envelope) != nil || envelope.Event.Seq != uint64(i) || envelope.Event.Kind != kind || envelope.Event.RunID != strings.Repeat("a", 32) {
			t.Fatal("invalid envelope")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestImagesSoakStreamDeadlineAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		writer, reader := net.Pipe()
		entered := make(chan struct{})
		s, _ := newImagesSoakStream(&soakNotifyingOutput{Conn: writer, entered: entered}, strings.Repeat("b", 32))
		s.timeout = 20 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.write(ctx, 0, imagesSoakStart{Scope: "formal"}) }()
		if cancelEarly {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("write did not start")
			}
			cancel()
		}
		select {
		case err := <-done:
			if err != errImagesSoakStream {
				t.Fatal("blocked writer accepted")
			}
		case <-time.After(time.Second):
			t.Fatal("blocked writer did not terminate")
		}
		cancel()
		writer.Close()
		reader.Close()
		if s.write(context.Background(), 1, imagesSoakStart{Scope: "formal"}) == nil {
			t.Fatal("failed stream resumed")
		}
	}
}

type soakNotifyingOutput struct {
	net.Conn
	entered chan struct{}
}

func (s *soakNotifyingOutput) Write(p []byte) (int, error) {
	close(s.entered)
	return s.Conn.Write(p)
}

type soakShortOutput struct{ deadlineErr bool }

func (*soakShortOutput) Write(p []byte) (int, error) { return len(p) - 1, nil }
func (*soakShortOutput) Close() error                { return nil }
func (s *soakShortOutput) SetWriteDeadline(time.Time) error {
	if s.deadlineErr {
		return io.ErrClosedPipe
	}
	return nil
}

func TestImagesSoakStreamRejectsInvalidAndPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		data   any
		output *soakShortOutput
	}{
		{map[string]string{"token": "private"}, &soakShortOutput{}},
		{imagesSoakStart{Scope: "formal"}, &soakShortOutput{}},
		{imagesSoakStart{Scope: "formal"}, &soakShortOutput{true}},
		{imagesSoakStart{Scope: "invented"}, &soakShortOutput{}},
		{imagesSoakSampleBlock{}, &soakShortOutput{}},
	} {
		s, _ := newImagesSoakStream(tc.output, strings.Repeat("c", 32))
		if err := s.write(context.Background(), 0, tc.data); err != errImagesSoakStream || s.seq != 0 {
			t.Fatal("invalid or partial output accepted")
		}
	}
	for _, id := range []string{"", strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if _, err := newImagesSoakStream(&soakShortOutput{}, id); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}

func TestImagesSoakStreamRealLinuxPipe(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("formal workload runs with a Linux pipe")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	s, _ := newImagesSoakStream(writer, strings.Repeat("d", 32))
	if err := s.write(context.Background(), 0, imagesSoakStart{Scope: "smoke"}); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(reader).ReadBytes('\n')
	if err != nil || !json.Valid(line) {
		t.Fatal("real pipe did not carry JSONL")
	}
}

func TestImagesSoakStreamSizeAndTimeLimits(t *testing.T) {
	for _, mode := range []string{"total", "time", "line", "duplicateStart", "beforeStart"} {
		writer, reader := net.Pipe()
		s, _ := newImagesSoakStream(writer, strings.Repeat("e", 32))
		var data any = imagesSoakStart{Scope: "smoke"}
		elapsed := int64(0)
		switch mode {
		case "total":
			s.bytes = 64 << 20
		case "time":
			elapsed = -1
		case "line":
			start := imagesSoakStart{Scope: "smoke"}
			start.GCBefore.Histogram.Counts = make([]uint64, 40000)
			data = start
		case "duplicateStart":
			s.seq = 1
		case "beforeStart":
			data = imagesSoakHour{Index: 0}
		}
		if err := s.write(context.Background(), elapsed, data); err != errImagesSoakStream {
			t.Fatal("invalid stream accepted")
		}
		writer.Close()
		reader.Close()
	}
}

func TestImagesSoakStreamReadyAndFinalAreSeparateSingleRecords(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	s, _ := newImagesSoakStream(writer, strings.Repeat("f", 32))
	s.workEnded = true
	done := make(chan error, 1)
	go func() {
		if err := s.ready(context.Background()); err != nil {
			done <- err
			return
		}
		done <- s.final(context.Background(), imagesSoakAcceptanceReport{Version: 1, RunID: strings.Repeat("f", 32), Scope: "smoke", Result: "failed"})
	}()
	_ = reader.SetReadDeadline(time.Now().Add(time.Second))
	r := bufio.NewReader(reader)
	for _, key := range []string{"imagesSoakReadyForSIGTERM", "imagesSoakAcceptance"} {
		line, err := r.ReadBytes('\n')
		var record map[string]json.RawMessage
		if err != nil || json.Unmarshal(line, &record) != nil || len(record) != 1 || record[key] == nil {
			t.Fatal("invalid control record")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.seq != 0 {
		t.Fatal("control records consumed event sequence")
	}
	if s.final(context.Background(), imagesSoakAcceptanceReport{Version: 1, RunID: strings.Repeat("f", 32)}) == nil {
		t.Fatal("duplicate final accepted")
	}
}
