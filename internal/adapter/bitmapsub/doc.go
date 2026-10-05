// Package bitmapsub decodes bitmap subtitle tracks into pictures for OCR.
//
// Two formats are supported, both as Matroska extraction writes them:
// HDMV PGS (S_HDMV/PGS, a .sup segment stream) and DVD VobSub (S_VOBSUB, an
// .idx text index plus an MPEG-2 program stream .sub). DVB subtitles are not
// supported.
//
// The input is untrusted. Every length, offset, size and count is checked
// before it is used; a picture is only allocated after its dimensions passed
// MaxCanvas and MaxPixels, and every buffer is bounded by a constant of this
// package. A structurally broken stream fails with ErrInvalid, an exceeded
// bound with ErrTooLarge; a single picture whose run-length data is corrupt
// is skipped and counted in Stats.Skipped instead. Nothing here opens files:
// callers hand in readers.
//
// Timing. Events are delivered in order of start time, each one as soon as
// its end is known (PGS: the next composition; VobSub: its stop command, or
// the next event). An event without a known end lasts DefaultDuration, and
// End is always clamped into (Start, Start+MaxDuration]. A picture that
// starts before the previously queued one is dropped and counted as skipped,
// so callers never see time run backwards. Identical consecutive PGS
// compositions (acquisition points that repeat the screen for seeking) are
// merged into one event.
//
// Cancellation. Decoding is synchronous; visit may return any error (for
// example ctx.Err()) and decoding stops at once with that error.
package bitmapsub
