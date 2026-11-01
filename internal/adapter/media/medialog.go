package media

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Media log (G46.3). Each direct delivery that passed authentication writes
// one record in the media scope when Options.Logger is set: how it ended,
// whether it was a seek (a Range answered 206), the first requested offset,
// whether an If-Range validator matched, and the error code of a refused
// request. The record names the source by its ID only; the file path, the
// root and the request headers are never logged. Ordinary completions,
// seeks and client disconnects are DEBUG (raise the media scope to see
// them), write timeouts and revoked sessions INFO, read failures and server
// side refusals WARN.

// Stream outcomes.
const (
	streamCompleted      = "completed"
	streamClientAborted  = "client_aborted"
	streamWriteTimeout   = "write_timeout"
	streamReadError      = "read_error"
	streamSessionRevoked = "session_revoked"
	streamRejected       = "rejected"
)

// streamRecord collects the media record of one delivery.
type streamRecord struct {
	started time.Time
	outcome string
	code    string
	status  int
	bytes   int64
	seek    bool
	offset  int64
	ranged  bool
	ifRange string
}

// mediaCodes are the error codes a refused delivery is logged with.
var mediaCodes = []error{ErrNotFound, ErrBusy, ErrUserStreamLimit, ErrDeviceStreamLimit, ErrLookupTimeout, ErrInvalidRange,
	ErrPreconditionFailed, ErrIO, ErrMethodNotAllowed, ErrInvalidRequest, ErrTranscodeDisabled, ErrPlaybackDenied, ErrUnauthenticated}

// mediaCode maps a delivery error onto a fixed code; the error text itself
// may carry wrapped detail and is never logged.
func mediaCode(err error) string {
	for _, known := range mediaCodes {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return ErrNotFound.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "media_cancelled"
	}
	return "media_failed"
}

// finish classifies how the copy ended. The stream context ends with the
// request or a revocation; a write error after it is the cut, not its cause.
func (rec *streamRecord) finish(streamCtx, requestCtx context.Context, w *streamWriter, f *contextFile, size int64) {
	rec.status, rec.bytes = w.status, w.written.Load()
	if rec.status == 0 && rec.bytes > 0 {
		rec.status = http.StatusOK
	}
	if rec.ranged {
		rec.seek = rec.status == http.StatusPartialContent
		if start, ok := firstRangeStart(w.request.Header.Get("Range"), size); ok {
			rec.offset = start
		}
		if rec.ifRange != "" {
			rec.ifRange = "miss"
			if rec.seek {
				rec.ifRange = "hit"
			}
		}
	}
	writeErr := w.failure()
	switch {
	case errors.Is(context.Cause(streamCtx), ErrSessionRevoked):
		rec.outcome = streamSessionRevoked
	case f.failure() != nil:
		rec.outcome = streamReadError
	case errors.Is(writeErr, os.ErrDeadlineExceeded) && !time.Now().Before(time.Unix(0, w.deadline.Load())):
		// The write deadline this stream set passed: the client stalled.
		// A deadline cut short by the stream context ending is a
		// disconnect or a revocation instead.
		rec.outcome = streamWriteTimeout
	case writeErr != nil, requestCtx.Err() != nil:
		rec.outcome = streamClientAborted
	case w.rejected:
		rec.outcome = streamRejected
	default:
		rec.outcome = streamCompleted
	}
}

// logStream writes the record. A delivery that ended before the copy
// either wrote an error (rejected) or lost its client.
func (h *Handler) logStream(r *http.Request, sourceID string, rec *streamRecord) {
	logger := h.options.Logger
	if logger == nil {
		return
	}
	if rec.outcome == "" {
		rec.outcome = streamRejected
		if rec.code == "" && r.Context().Err() != nil {
			rec.outcome = streamClientAborted
		}
	}
	level, message := slog.LevelDebug, "media stream completed"
	switch rec.outcome {
	case streamClientAborted:
		message = "media stream interrupted"
	case streamWriteTimeout, streamSessionRevoked:
		level, message = slog.LevelInfo, "media stream interrupted"
	case streamReadError:
		level, message = slog.LevelWarn, "media stream failed"
	case streamRejected:
		message = "media request rejected"
		switch rec.code {
		case ErrIO.Error(), ErrLookupTimeout.Error(), "media_failed":
			level = slog.LevelWarn
		}
	}
	ctx := r.Context()
	if !logger.Enabled(ctx, level) {
		return
	}
	attrs := make([]slog.Attr, 0, 12)
	attrs = append(attrs, slog.String("component", "media"), slog.String("method", r.Method), slog.String("outcome", rec.outcome),
		slog.Int64("durationMs", time.Since(rec.started).Milliseconds()))
	if sourceID != "" {
		attrs = append(attrs, slog.String("sourceId", sourceID))
	}
	if rec.code != "" {
		attrs = append(attrs, slog.String("code", rec.code))
	}
	if rec.status != 0 {
		attrs = append(attrs, slog.Int("status", rec.status), slog.Int64("bytes", rec.bytes))
	}
	if rec.ranged && rec.status != 0 && rec.status < 400 {
		attrs = append(attrs, slog.Bool("seek", rec.seek), slog.Int64("offset", rec.offset))
		if rec.ifRange != "" {
			attrs = append(attrs, slog.String("ifRange", rec.ifRange))
		}
	}
	logger.LogAttrs(ctx, level, message, attrs...)
}

// failure is the first error a write returned.
func (w *streamWriter) failure() error {
	if p := w.err.Load(); p != nil {
		return *p
	}
	return nil
}

func (w *streamWriter) noteError(err error) {
	if err != nil {
		w.err.CompareAndSwap(nil, &err)
	}
}

// noteDeadline records a write deadline the stream set itself.
func (w *streamWriter) noteDeadline(at time.Time) { w.deadline.Store(at.UnixNano()) }

// failure is the first read or seek error of the file while the stream ran;
// errors caused by the stream ending (the file closed under a read) are not
// failures of the file.
func (f *contextFile) failure() error {
	if p := f.err.Load(); p != nil {
		return *p
	}
	return nil
}

func (f *contextFile) note(err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || f.ctx.Err() != nil {
		return
	}
	f.err.CompareAndSwap(nil, &err)
}
