package outbound

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// MaxStreamBytes bounds any streamed body regardless of caller policy.
const MaxStreamBytes = 64 << 20

// MaxStreamRedirects bounds redirect chains a streaming caller may allow.
const MaxStreamRedirects = 5

// StreamOptions describes one streamed HTTPS GET.
type StreamOptions struct {
	MaxBytes     int64
	MaxRedirects int
	Accept       string
}

// StreamResponse hands a bounded body to the caller. Body must be closed; the
// shared I/O permit and the request deadline are held until then.
type StreamResponse struct {
	Status        int
	ContentType   string
	ContentLength int64
	RetryAfter    string
	Body          io.ReadCloser
}

// Stream issues an HTTPS-only GET and returns before reading the body, so
// large payloads never need to be buffered. Every redirect hop is checked by
// the same target policy (HTTPS, host list, literal address) before it is
// requested and its DNS answers are pinned by the guarded dialer. Raw
// transport errors never escape, neither here nor from Body reads.
func (c *Client) Stream(ctx context.Context, rawURL string, o StreamOptions) (*StreamResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.MaxBytes < 1 || o.MaxBytes > MaxStreamBytes || o.MaxRedirects < 0 || o.MaxRedirects > MaxStreamRedirects || len(rawURL) > 8192 || !validAccept(o.Accept) {
		return nil, ErrDenied
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || c.validate(u) != nil {
		return nil, ErrDenied
	}
	// Hard ceiling for the whole exchange including the body; callers set
	// tighter deadlines through ctx.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	release := func() {}
	if c.budget != nil {
		r, err := c.budget.Acquire(ctx, app.WorkIO)
		if err != nil {
			cancel()
			return nil, err
		}
		release = r
	}
	done := func() { release(); cancel() }
	client := &http.Client{Transport: c.transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || c.validate(r.URL) != nil {
			return ErrDenied
		}
		if len(via) > o.MaxRedirects {
			return ErrRedirect
		}
		// The previous URL may carry NFO-supplied query data.
		r.Header.Del("Referer")
		return nil
	}}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		done()
		return nil, ErrDenied
	}
	r.Header.Set("User-Agent", "Jelee")
	if o.Accept != "" {
		r.Header.Set("Accept", o.Accept)
	}
	response, err := client.Do(r)
	if err != nil {
		err = safeError(ctx, err)
		done()
		return nil, err
	}
	if response.ContentLength > o.MaxBytes {
		response.Body.Close()
		done()
		return nil, ErrTooLarge
	}
	body := &streamBody{ctx: ctx, body: response.Body, limit: o.MaxBytes, done: done}
	return &StreamResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), ContentLength: response.ContentLength,
		RetryAfter: response.Header.Get("Retry-After"), Body: body}, nil
}

func validAccept(v string) bool {
	if len(v) > 256 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// streamBody enforces the byte bound while reading: the first byte past the
// limit fails the read and nothing more is pulled from the connection. A body
// only ends with io.EOF while the request context is still live.
type streamBody struct {
	ctx   context.Context
	body  io.ReadCloser
	limit int64
	read  int64
	err   error
	once  sync.Once
	done  func()
}

func (b *streamBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if remaining := b.limit - b.read + 1; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.body.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		b.err = ErrTooLarge
		return 0, b.err
	}
	if err != nil {
		if err != io.EOF {
			err = safeError(b.ctx, err)
		} else if ctxErr := b.ctx.Err(); ctxErr != nil {
			// Ending the context closes the connection, and a peer that stops
			// on that signal can still terminate the body cleanly before the
			// close lands. An EOF observed after the deadline or cancellation
			// therefore does not prove the source sent a complete body.
			err = ctxErr
		}
		b.err = err
	}
	return n, err
}

func (b *streamBody) Close() error {
	b.once.Do(func() {
		b.body.Close()
		b.done()
	})
	return nil
}
