package domain

import (
	"strings"
	"testing"
	"time"
)

func TestImageFetchRetryDelayDoublesToCap(t *testing.T) {
	for _, c := range []struct {
		failures  int
		permanent bool
		want      time.Duration
	}{
		{1, false, time.Hour}, {2, false, 2 * time.Hour}, {4, false, 8 * time.Hour}, {8, false, 128 * time.Hour}, {9, false, 7 * 24 * time.Hour}, {1000000, false, 7 * 24 * time.Hour},
		{1, true, 24 * time.Hour}, {2, true, 48 * time.Hour}, {5, true, 16 * 24 * time.Hour}, {6, true, 30 * 24 * time.Hour}, {0, true, 24 * time.Hour},
	} {
		if got := ImageFetchRetryDelay(c.failures, c.permanent); got != c.want {
			t.Fatalf("delay(%d,%t)=%s want %s", c.failures, c.permanent, got, c.want)
		}
	}
}

func TestValidImageRefreshResult(t *testing.T) {
	candidate := ImageRefreshCandidate{ImageID: "00000000-0000-4000-8000-000000000001", ItemID: "00000000-0000-4000-8000-000000000002", URL: "https://img.example/a.jpg"}
	content := &ItemImageContent{SHA256: make([]byte, 32), Format: "png", Bytes: 10, FetchedAt: time.Now()}
	failure := &ImageFetchFailure{Reason: ImageFetchNotFound, Status: 404, Permanent: true}
	valid := []ImageRefreshResult{
		{Candidate: candidate, Content: content},
		{Candidate: candidate, Deferred: true},
		{Candidate: candidate, Failure: failure, Failures: 1, RetryDelay: time.Hour},
	}
	for _, r := range valid {
		if !ValidImageRefreshResult(r) {
			t.Fatalf("rejected %+v", r)
		}
	}
	bad := candidate
	bad.URL = "http://img.example/a.jpg"
	invalid := []ImageRefreshResult{
		{Candidate: candidate},
		{Candidate: candidate, Content: content, Deferred: true},
		{Candidate: bad, Deferred: true},
		{Candidate: candidate, Content: &ItemImageContent{Format: "png"}},
		{Candidate: candidate, Failure: &ImageFetchFailure{Reason: "boom"}, Failures: 1, RetryDelay: time.Hour},
		{Candidate: candidate, Failure: &ImageFetchFailure{Reason: ImageFetchTimeout, Status: 42}, Failures: 1, RetryDelay: time.Hour},
		{Candidate: candidate, Failure: failure, Failures: 0, RetryDelay: time.Hour},
		{Candidate: candidate, Failure: failure, Failures: 1},
		{Candidate: candidate, Failure: failure, Failures: 1, RetryDelay: 31 * 24 * time.Hour},
	}
	for i, r := range invalid {
		if ValidImageRefreshResult(r) {
			t.Fatalf("accepted invalid result %d", i)
		}
	}
	if text := failure.Error(); !strings.Contains(text, "not_found") || !strings.Contains(text, "404") || candidate.String() != candidate.GoString() || strings.Contains(candidate.String(), "img.example") {
		t.Fatal("unsafe text", text)
	}
}
