//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

var errImagesSoakStream = errors.New("images_soak_stream_invalid")

type imagesSoakStart struct {
	Scope                   string                    `json:"scope"`
	Runtime                 memoryRuntimeSettings     `json:"runtime"`
	Configuration           imagesMemoryConfiguration `json:"configuration"`
	CgroupBefore            memoryCgroupSnapshot      `json:"cgroupBefore"`
	BeforeReadStartedNanos  int64                     `json:"beforeReadStartedNanos"`
	BeforeReadFinishedNanos int64                     `json:"beforeReadFinishedNanos"`
	GCBefore                scanGCBoundary            `json:"gcBefore"`
}

type imagesSoakHour struct {
	Index                int            `json:"index"`
	GCBefore             scanGCBoundary `json:"gcBefore"`
	GCAfter              scanGCBoundary `json:"gcAfter"`
	CheckpointIndices    [12]int        `json:"checkpointIndices"`
	HeapMedianTwiceBytes uint64         `json:"heapMedianTwiceBytes"`
	RSSMedianTwiceBytes  uint64         `json:"rssMedianTwiceBytes"`
	RSSMin               uint64         `json:"rssMin"`
	RSSPeak              uint64         `json:"rssPeak"`
	HeapMin              uint64         `json:"heapMin"`
	HeapPeak             uint64         `json:"heapPeak"`
}

type imagesSoakWorkEnd struct {
	WorkStartedNanos  int64 `json:"workStartedNanos"`
	WorkFinishedNanos int64 `json:"workFinishedNanos"`
	CompletedRounds   int   `json:"completedRounds"`
}

type imagesSoakEnvelope struct {
	Version      int             `json:"version"`
	RunID        string          `json:"runId"`
	Seq          uint64          `json:"seq"`
	Kind         string          `json:"kind"`
	ElapsedNanos int64           `json:"elapsedNanos"`
	Data         json.RawMessage `json:"data"`
}

// A pipe/socket with real write deadlines is required. Unsupported destinations
// fail before any bytes are written rather than starting an unjoinable writer.
type imagesSoakOutput interface {
	io.WriteCloser
	SetWriteDeadline(time.Time) error
}

type imagesSoakStream struct {
	mu        sync.Mutex
	output    imagesSoakOutput
	runID     string
	seq       uint64
	bytes     int
	elapsed   int64
	err       error
	workEnded bool
	timeout   time.Duration
}

func newImagesSoakStream(output imagesSoakOutput, runID string) (*imagesSoakStream, error) {
	if output == nil || len(runID) != 32 {
		return nil, errImagesSoakStream
	}
	for _, c := range runID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, errImagesSoakStream
		}
	}
	return &imagesSoakStream{output: output, runID: runID, timeout: 5 * time.Second}, nil
}

// Only explicit evidence types can be serialized. No caller-provided map,
// token, DB handle, error string, or generic object is written to the stream.
func (s *imagesSoakStream) write(ctx context.Context, elapsed int64, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func() error { s.err = errImagesSoakStream; return s.err }
	if s.err != nil || ctx.Err() != nil || elapsed < s.elapsed || elapsed > int64(25*time.Hour) {
		return fail()
	}
	var kind string
	switch value := data.(type) {
	case imagesSoakStart:
		if s.seq != 0 || value.Scope != "formal" && value.Scope != "smoke" {
			return fail()
		}
		kind = "start"
	case imagesSoakSampleBlock:
		if len(value.Samples) < 1 || len(value.Samples) > 60 {
			return fail()
		}
		kind = "samples"
	case imagesSoakHour:
		if value.Index < 0 || value.Index >= 24 {
			return fail()
		}
		kind = "hour"
	case imagesSoakWorkEnd:
		if s.workEnded || value.WorkStartedNanos < 0 || value.WorkFinishedNanos <= value.WorkStartedNanos || value.WorkFinishedNanos > elapsed || value.CompletedRounds < 1 || value.CompletedRounds > 288 {
			return fail()
		}
		kind = "workEnd"
	default:
		return fail()
	}
	if s.seq == 0 && kind != "start" {
		return fail()
	}
	payload, err := json.Marshal(data)
	if err != nil || len(payload) > 64<<10 {
		return fail()
	}
	line, err := json.Marshal(struct {
		Event imagesSoakEnvelope `json:"imagesSoakEvent"`
	}{imagesSoakEnvelope{1, s.runID, s.seq, kind, elapsed, payload}})
	line = append(line, '\n')
	if err != nil || len(line) > 64<<10 || s.bytes+len(line) > 64<<20 {
		return fail()
	}
	deadline := time.Now().Add(s.timeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	if s.output.SetWriteDeadline(deadline) != nil {
		return fail()
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = s.output.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	n, err := s.output.Write(line)
	if err != nil || n != len(line) || ctx.Err() != nil {
		return fail()
	}
	s.seq++
	s.bytes += n
	s.elapsed = elapsed
	s.workEnded = s.workEnded || kind == "workEnd"
	return nil
}
