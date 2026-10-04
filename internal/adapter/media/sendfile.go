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
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
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
	stop := w.watchProgress(ctx, source.file, start)
	written, err = to.ReadFrom(&io.LimitedReader{R: source.file, N: limited.N})
	stop()
	limited.N -= written
	return written, true, err
}

// watchProgress extends the write deadline whenever the file offset has
// advanced since the previous poll, so one long sendfile call is cut only
// after the client has made no progress for about the write timeout. The
// returned function stops the watchdog and waits for it.
func (w *streamWriter) watchProgress(ctx context.Context, file *os.File, last int64) func() {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(max(w.timeout/4, minProgressInterval))
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
			if offset == last {
				continue
			}
			last = offset
			_ = w.controller.SetWriteDeadline(time.Now().Add(w.timeout))
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
