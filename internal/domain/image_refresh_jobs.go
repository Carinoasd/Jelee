package domain

import (
	"strconv"
	"time"
)

// Image refresh (G40.9, G40.10): one image_refresh job fetches the remote
// artwork of one library that has no stored content yet, in ascending image
// row order, and records a resumable cursor with its counters. Locked and
// manual rows are never candidates and never written.

// JobImageRefresh is the job kind of an image refresh.
const JobImageRefresh = "image_refresh"

// ImageRefreshBatchSize is how many candidate rows one read covers. Every
// fetch is checkpointed on its own, so this only bounds a read.
const ImageRefreshBatchSize = 32

// ImageRefreshCounts classifies the rows a run finished with. Fetched rows
// received content; Failed rows recorded a fetch failure; Deferred rows
// were still inside the retry delay of an earlier failure and were not
// fetched; Skipped rows changed while they were fetched (locked, manual,
// another URL, content already present or deleted) and were left alone.
type ImageRefreshCounts struct {
	Fetched  int
	Failed   int
	Deferred int
	Skipped  int
}

// ImageRefreshJobState is the public progress of an image_refresh job.
// CursorImageID is null until the first checkpoint.
type ImageRefreshJobState struct {
	JobID         string    `json:"jobId"`
	LibraryID     string    `json:"libraryId"`
	CursorImageID *string   `json:"cursorImageId"`
	Fetched       int       `json:"fetched"`
	Failed        int       `json:"failed"`
	Deferred      int       `json:"deferred"`
	Skipped       int       `json:"skipped"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ImageRefreshCandidate is one remote row without content. Failures counts
// the earlier failed fetches of the same URL; Deferred is true while the
// retry delay of the last one has not passed.
type ImageRefreshCandidate struct {
	ImageID  string
	ItemID   string
	URL      string
	Failures int
	Deferred bool
}

// String keeps the URL out of logs and errors.
func (ImageRefreshCandidate) String() string { return "image refresh candidate (redacted)" }

// GoString keeps the URL out of %#v output.
func (ImageRefreshCandidate) GoString() string { return "image refresh candidate (redacted)" }

// ImageRefreshResult is the outcome of one candidate. Exactly one of Content
// (fetched), Failure (failed) or Deferred is set. RetryDelay is how long a
// failure defers the next attempt and Failures the failure count to store.
type ImageRefreshResult struct {
	Candidate  ImageRefreshCandidate
	Content    *ItemImageContent
	Failure    *ImageFetchFailure
	Deferred   bool
	Failures   int
	RetryDelay time.Duration
}

// ValidImageRefreshResult checks one result before it reaches storage.
func ValidImageRefreshResult(r ImageRefreshResult) bool {
	c := r.Candidate
	if !ValidID(c.ImageID) || !ValidID(c.ItemID) || !ValidItemImageRemoteURL(c.URL) {
		return false
	}
	set := 0
	for _, present := range []bool{r.Content != nil, r.Failure != nil, r.Deferred} {
		if present {
			set++
		}
	}
	if set != 1 {
		return false
	}
	switch {
	case r.Content != nil:
		return validItemImageContent(r.Content)
	case r.Failure != nil:
		return ValidImageFetchReason(r.Failure.Reason) && (r.Failure.Status == 0 || r.Failure.Status >= 100 && r.Failure.Status <= 599) &&
			r.Failures >= 1 && r.Failures <= ImageFetchMaxFailures && r.RetryDelay > 0 && r.RetryDelay <= ImageFetchMaxRetryDelay
	}
	return true
}

// ImageFetchFailure is a per-row fetch failure with a stable, log-safe
// reason. It never carries the URL, host, headers or response bytes.
// Permanent failures (the source answered and refused, or sent something
// that is not a usable image) wait longer before the next attempt than
// transient ones (rate limits, outages, timeouts, a busy processor).
type ImageFetchFailure struct {
	Reason    string
	Status    int
	Permanent bool
}

func (e *ImageFetchFailure) Error() string {
	text := "image fetch failed: " + e.Reason
	if e.Status != 0 {
		text += " status=" + strconv.Itoa(e.Status)
	}
	return text
}

// Image fetch failure reasons stored with a failed row.
const (
	ImageFetchURLRejected         = "url_rejected"
	ImageFetchTargetDenied        = "target_denied"
	ImageFetchRedirectDenied      = "redirect_denied"
	ImageFetchTooLarge            = "too_large"
	ImageFetchContentTypeRejected = "content_type_rejected"
	ImageFetchFormatRejected      = "format_rejected"
	ImageFetchNotFound            = "not_found"
	ImageFetchStatusRejected      = "status_rejected"
	ImageFetchRateLimited         = "rate_limited"
	ImageFetchUnavailable         = "unavailable"
	ImageFetchTimeout             = "timeout"
	ImageFetchStoreFailed         = "store_failed"
	ImageFetchUnsupported         = "unsupported"
	ImageFetchBusy                = "busy"
	ImageFetchLocalOnly           = "local_only"
)

var imageFetchReasons = map[string]bool{ImageFetchURLRejected: true, ImageFetchTargetDenied: true, ImageFetchRedirectDenied: true,
	ImageFetchTooLarge: true, ImageFetchContentTypeRejected: true, ImageFetchFormatRejected: true, ImageFetchNotFound: true,
	ImageFetchStatusRejected: true, ImageFetchRateLimited: true, ImageFetchUnavailable: true, ImageFetchTimeout: true,
	ImageFetchStoreFailed: true, ImageFetchUnsupported: true, ImageFetchBusy: true, ImageFetchLocalOnly: true}

// ValidImageFetchReason reports whether reason is a stored failure reason.
func ValidImageFetchReason(reason string) bool { return imageFetchReasons[reason] }

// Retry delays after a failed fetch: transient failures start at one hour
// and permanent ones at one day; each further failure of the same URL
// doubles the delay up to seven and thirty days.
const (
	ImageFetchTransientRetryDelay = time.Hour
	ImageFetchPermanentRetryDelay = 24 * time.Hour
	ImageFetchTransientMaxDelay   = 7 * 24 * time.Hour
	ImageFetchMaxRetryDelay       = 30 * 24 * time.Hour
	ImageFetchMaxFailures         = 1000000
)

// ImageFetchRetryDelay is the delay after the failures-th failed fetch of
// one URL (failures counts from one).
func ImageFetchRetryDelay(failures int, permanent bool) time.Duration {
	base, limit := ImageFetchTransientRetryDelay, ImageFetchTransientMaxDelay
	if permanent {
		base, limit = ImageFetchPermanentRetryDelay, ImageFetchMaxRetryDelay
	}
	delay := base
	for i := 1; i < failures && delay < limit; i++ {
		delay *= 2
	}
	return min(delay, limit)
}
