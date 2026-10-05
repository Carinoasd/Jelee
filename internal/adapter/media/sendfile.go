package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"
)

// testHookZeroCopy, when set by tests, runs each time a stream takes the
// zero-copy path.
var testHookZeroCopy func()

// minProgressInterval bounds how often the watchdog polls the file offset.
const minProgressInterval = 10 * time.Millisecond

// zeroCopy hands a complete or single-Range response to the connection's
// io.ReaderFrom so net/http can use sendfile (G10.8). It reports handled as
// false, having written nothing, when the stream must use the buffered copy:
// a bandwidth limit, a multipart Range (which ServeContent feeds through a
// pipe), an error response, TLS, or a writer without ReaderFrom or deadlines.
//
// Cancellation and revocation do not depend on this path: ending the stream
// context expires the write deadline, which stops a blocked sendfile, and
// closes the file. The write timeout keeps its meaning, the time a stalled
// client may hold the stream, through a watchdog that extends the deadline
// while the file offset advances.
func (w *streamWriter) zeroCopy(reader io.Reader) (written int64, handled bool, err error) {
	if !zeroCopySupported || w.throttle != nil || w.rejected || w.request.TLS != nil {
		return 0, false, nil
	}
	limited, ok := reader.(*io.LimitedReader)
	if !ok || limited.N <= 0 {
		return 0, false, nil
	}
	source, ok := limited.R.(*contextFile)
	if !ok {
		return 0, false, nil
	}
	to, ok := w.ResponseWriter.(io.ReaderFrom)
	if !ok {
		return 0, false, nil
	}
	ctx := w.context()
	if err := ctx.Err(); err != nil {
		return 0, true, err
	}
	start, err := source.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, true, err
	}
	watch := newProgressWatch(w.timeout, start)
	if err := w.controller.SetWriteDeadline(watch.deadline(time.Now())); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			return 0, false, nil
		}
		return 0, true, err
	}
	// Cancellation may have expired the deadline just before the refresh above.
	if err := ctx.Err(); err != nil {
		_ = w.controller.SetWriteDeadline(time.Now())
		return 0, true, err
	}
	if testHookZeroCopy != nil {
		testHookZeroCopy()
	}
	// Send the header first so ReadFrom does not copy its first 512 bytes
	// for content sniffing through net/http's shared, uncleared buffer.
	w.Header().Del("Content-Disposition")
	if err := w.controller.Flush(); err != nil {
		return 0, true, err
	}
	stop := w.watchProgress(ctx, source.file, watch)
	written, err = to.ReadFrom(&io.LimitedReader{R: source.file, N: limited.N})
	stop()
	limited.N -= written
	return written, true, err
}

// progressWatch decides the write deadline of one zero-copy transfer from the
// file offset, which Linux sendfile advances as the kernel accepts bytes.
type progressWatch struct {
	timeout  time.Duration
	interval time.Duration
	last     int64
}

func newProgressWatch(timeout time.Duration, offset int64) *progressWatch {
	return &progressWatch{timeout: timeout, interval: max(timeout/4, minProgressInterval), last: offset}
}

// deadline is the write deadline for progress observed at now. The offset is
// only polled every interval, so progress is seen up to one interval after
// the kernel accepted the bytes; the deadline keeps that interval as headroom.
// Without it, progress made shortly before the previous deadline was noticed
// only after the deadline had cut the transfer, and a client was cut after
// three quarters of the write timeout without progress instead of the full
// timeout the buffered path allows.
func (p *progressWatch) deadline(now time.Time) time.Time {
	return now.Add(p.timeout + p.interval)
}

// poll records the offset seen at now and reports the extended deadline when
// the offset advanced since the previous poll.
func (p *progressWatch) poll(now time.Time, offset int64) (time.Time, bool) {
	if offset == p.last {
		return time.Time{}, false
	}
	p.last = offset
	return p.deadline(now), true
}

// watchProgress extends the write deadline whenever the file offset has
// advanced since the previous poll, so one long sendfile call is cut only
// after the client has made no progress for at least the write timeout (and
// at most one poll interval more). The returned function stops the watchdog
// and waits for it.
func (w *streamWriter) watchProgress(ctx context.Context, file *os.File, watch *progressWatch) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(watch.interval)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			offset, err := file.Seek(0, io.SeekCurrent)
			if err != nil {
				// The file was closed because the stream ended.
				return
			}
			deadline, advanced := watch.poll(time.Now(), offset)
			if !advanced {
				continue
			}
			_ = w.controller.SetWriteDeadline(deadline)
			// If the stream ended around the refresh, expire the deadline
			// again so the refresh cannot undo the cancellation.
			if ctx.Err() != nil {
				_ = w.controller.SetWriteDeadline(time.Now())
				return
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}
