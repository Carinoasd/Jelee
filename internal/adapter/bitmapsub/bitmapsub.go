package bitmapsub

import (
	"errors"
	"time"
)

var (
	// ErrInvalid reports a malformed or truncated stream.
	ErrInvalid = errors.New("bitmap_subtitle_invalid")
	// ErrTooLarge reports that a bound of this package was exceeded.
	ErrTooLarge = errors.New("bitmap_subtitle_too_large")
)

// Bounds applied to every track.
const (
	// MaxCanvas bounds the width and height of a picture or PGS object.
	MaxCanvas = 4096
	// MaxPixels bounds the area of a single picture or object.
	MaxPixels = 4096 * 2304
	// MaxEvents bounds the pictures (decoded or skipped) of one track.
	MaxEvents = 50000
	// MaxIndexBytes bounds a VobSub .idx file.
	MaxIndexBytes = 4 << 20
	// MaxObjectBytes bounds the accumulated ODS fragments of one PGS object.
	MaxObjectBytes = 16 << 20
	// MaxEpochBytes bounds all PGS object data held at once; a real decoder
	// buffer is 4 MiB.
	MaxEpochBytes = 32 << 20
	// MaxEpochObjects bounds the distinct PGS objects held at once (the
	// specification allows 64 per epoch).
	MaxEpochObjects = 64
	// MaxObjectsPerComposition is the PGS limit of objects on one screen.
	MaxObjectsPerComposition = 2
	// DefaultDuration is used for an event whose end is unknown.
	DefaultDuration = 5 * time.Second
	// MaxDuration clamps the length of any event.
	MaxDuration = 30 * time.Second
	// MaxTrackPixels bounds the pixels decoded for one track. A tiny
	// composition may redraw a large cached object, so without it a small
	// file could demand MaxEvents full-size pictures. A feature film
	// decodes well under 1<<30 pixels.
	MaxTrackPixels = 1 << 32
)

// maxEvents and maxTrackPixels are the bounds above; in-package tests lower
// them to exercise the checks.
var (
	maxEvents      = MaxEvents
	maxTrackPixels = int64(MaxTrackPixels)
)

// Bitmap is one decoded picture: per-pixel luma (0..255, full range) and
// alpha (0..255), row-major, each of length Width*Height.
type Bitmap struct {
	Width, Height int
	Luma, Alpha   []uint8
}

// Event is one displayed picture with its presentation time. The bitmap is
// freshly allocated per event and owned by the receiver.
type Event struct {
	Start, End time.Duration
	Forced     bool
	Bitmap     Bitmap
}

// Stats counts delivered and skipped pictures. A picture is skipped when its
// pixel data is corrupt, it references missing data, or its start time runs
// backwards.
type Stats struct {
	Events, Skipped int
}

// emitter queues one event until its end is known and enforces the timing
// rules shared by both formats.
type emitter struct {
	visit     func(Event) error
	pictures  int
	pixels    int64
	stats     Stats
	pending   Event
	endKnown  bool
	has       bool
	started   bool
	lastStart time.Duration
}

// picture counts one decode attempt; skipped pictures count as well, so a
// small file cannot request unbounded composition work.
func (e *emitter) picture() error {
	e.pictures++
	if e.pictures > maxEvents {
		return ErrTooLarge
	}
	return nil
}

// end closes the queued event at t unless it already knows its end.
func (e *emitter) end(t time.Duration) error {
	if !e.has {
		return nil
	}
	ev := e.pending
	if !e.endKnown {
		ev.End = t
	}
	e.has, e.pending = false, Event{}
	ev.End = clampEnd(ev.Start, ev.End)
	e.stats.Events++
	return e.visit(ev)
}

// push closes the queued event and queues ev. A start earlier than the last
// queued start is dropped so delivered events stay monotonic.
func (e *emitter) push(ev Event, endKnown bool) error {
	if e.started && ev.Start < e.lastStart {
		e.stats.Skipped++
		return nil
	}
	if err := e.end(ev.Start); err != nil {
		return err
	}
	e.pending, e.endKnown, e.has = ev, endKnown, true
	e.started, e.lastStart = true, ev.Start
	return nil
}

// finish delivers the last event; without a known end it gets the default.
func (e *emitter) finish() error {
	return e.end(0)
}

func clampEnd(start, end time.Duration) time.Duration {
	if end <= start {
		return start + DefaultDuration
	}
	if end-start > MaxDuration {
		return start + MaxDuration
	}
	return end
}

// clampByte converts a value that is in 0..255 by construction; the checks
// keep a mistake from wrapping around.
func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// newBitmap allocates a picture after checking its size (errSkip) and the
// track pixel budget (ErrTooLarge).
func (e *emitter) newBitmap(w, h int) (Bitmap, error) {
	if w <= 0 || h <= 0 || w > MaxCanvas || h > MaxCanvas || w*h > MaxPixels {
		return Bitmap{}, errSkip
	}
	e.pixels += int64(w * h)
	if e.pixels > maxTrackPixels {
		return Bitmap{}, ErrTooLarge
	}
	return Bitmap{Width: w, Height: h, Luma: make([]uint8, w*h), Alpha: make([]uint8, w*h)}, nil
}
