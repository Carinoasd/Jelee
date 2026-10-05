// Package subtitleocr turns the bitmap subtitle tracks of Matroska sources
// (PGS, VobSub) into additional SRT tracks in a rebuildable cache (G15.6).
// It is off by default, runs in the background with a bounded queue, a
// per-minute picture limit and 1..N concurrent recognitions, and never
// changes the original file or its bitmap tracks, which are still only
// delivered as they are (G15.5). Nothing here starts a process itself: the
// recognizer is the isolated Tesseract runner registered by ocrruntime.
package subtitleocr

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Clock is the time source of the rate limiter; tests use a fake.
type Clock interface {
	Now() time.Time
	// After delivers once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Rate bounds.
const (
	MinPicturesPerMinute = 1
	MaxPicturesPerMinute = 6000
	rateWindow           = time.Minute
)

// ErrInvalidRate rejects a limit outside the supported bounds.
var ErrInvalidRate = errors.New("subtitle_ocr_rate_invalid")

// RateLimiter admits at most a fixed number of pictures in any rolling
// one-minute window. It keeps the admission times of the last perMinute
// pictures in a ring; a caller waits until the oldest of them leaves the
// window. A strict window, unlike a token bucket, never lets a burst exceed
// the configured per-minute figure.
type RateLimiter struct {
	clock Clock
	mu    sync.Mutex
	ring  []time.Time
	next  int
	count int
	// waits counts admissions that had to wait for the window.
	waits uint64
}

// NewRateLimiter returns a limiter of perMinute pictures per rolling minute.
func NewRateLimiter(perMinute int, clock Clock) (*RateLimiter, error) {
	if perMinute < MinPicturesPerMinute || perMinute > MaxPicturesPerMinute {
		return nil, ErrInvalidRate
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &RateLimiter{clock: clock, ring: make([]time.Time, perMinute)}, nil
}

// Wait blocks until one more picture fits in the window or ctx ends.
func (l *RateLimiter) Wait(ctx context.Context) error {
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		now := l.clock.Now()
		if l.count < len(l.ring) {
			l.admit(now, waited)
			l.mu.Unlock()
			return nil
		}
		// The ring is full: next is the oldest admission.
		delay := l.ring[l.next].Add(rateWindow).Sub(now)
		if delay <= 0 {
			l.admit(now, waited)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		waited = true
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.clock.After(delay):
		}
	}
}

func (l *RateLimiter) admit(now time.Time, waited bool) {
	l.ring[l.next] = now
	l.next = (l.next + 1) % len(l.ring)
	if l.count < len(l.ring) {
		l.count++
	}
	if waited {
		l.waits++
	}
}

// Waits returns how many admissions had to wait for the window.
func (l *RateLimiter) Waits() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waits
}
