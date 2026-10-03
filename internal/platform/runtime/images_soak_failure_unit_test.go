//go:build jelee_probe_tests

package runtime

import (
	"context"
	"errors"
	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestImagesColdFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		cancel  error
		request bool
		idle    error
		valid   bool
		want    string
	}{
		{nil, false, nil, true, ""},
		{context.Canceled, true, errors.New("private"), false, "context_finished"},
		{nil, true, errors.New("private"), false, "http_request_failed"},
		{nil, false, errors.New("private"), false, "processor_not_idle"},
		{nil, false, nil, false, "counter_mismatch"},
	} {
		if got := imagesColdFailure(tc.cancel, tc.request, tc.idle, tc.valid); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestImagesColdHTTPFailureRetainsStatistics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A Windows clock tick can outlast this local HTTP request. Keep the
		// fixture measurable without changing acceptance timing assertions.
		started := time.Now()
		for time.Since(started) <= 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	life := &lifetime{imageStats: func() imageadapter.Stats { return imageadapter.Stats{Admitted: 9, Failed: 1} }}
	var report imagesMemoryPhaseResult
	var tags [64]string
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	code := runImagesColdSince(ctx, time.Now(), life, server.Client(), server.URL, "fixture-token", 1, &report, &tags)
	if code != "cold_image_processing_failed" || report.FailureCode != "http_request_failed" {
		t.Fatalf("lost bounded diagnostic: %q %+v", code, report)
	}
	if report.FailedRequest == nil || report.FailedRequest.Status != 503 || report.FailedRequest.Index != 0 {
		t.Fatal("HTTP status/index not retained")
	}
	if report.Before.Admitted != 9 || report.After.Admitted != 9 || report.Get200 != 0 || report.FinishedNanos <= report.StartedNanos {
		t.Fatalf("phase evidence: before=%d after=%d get200=%d start=%d finish=%d elapsed=%d", report.Before.Admitted, report.After.Admitted, report.Get200, report.StartedNanos, report.FinishedNanos, report.ElapsedNanos)
	}
}
