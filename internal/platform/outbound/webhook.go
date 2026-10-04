package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// ErrCertificate reports a TLS certificate that failed verification. Webhook
// deliveries never skip verification; a self-signed endpoint needs its CA
// configured explicitly (G12.5).
var ErrCertificate = errors.New("outbound_certificate_rejected")

// MaxPostBytes bounds a POST body.
const MaxPostBytes = 1 << 20

// maxPostTimeout caps a caller-supplied attempt timeout.
const maxPostTimeout = 60 * time.Second

// PostResult is the part of a POST response a webhook deliverer may use. The
// response body is discarded unread beyond a small drain.
type PostResult struct {
	Status     int
	RetryAfter string
}

// NewWebhookClient builds the webhook delivery client (G11.4, G12.5). hosts
// is the configured allow list (empty allows any public host). extraRoots is
// an optional PEM bundle trusted in addition to the system roots, for
// endpoints with a self-signed certificate; verification itself cannot be
// disabled. budget may be nil.
func NewWebhookClient(hosts []string, extraRoots []byte, budget app.WorkBudget) (*Client, error) {
	c, err := New(hosts)
	if err != nil {
		return nil, err
	}
	if err = c.configureWebhook(extraRoots); err != nil {
		return nil, err
	}
	c.budget = budget
	return c, nil
}

func (c *Client) configureWebhook(extraRoots []byte) error {
	// Attempt deadlines come from the caller (at most 30 s per endpoint).
	c.transport.ResponseHeaderTimeout = maxPostTimeout
	if len(extraRoots) == 0 {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(extraRoots) {
		return errors.New("webhook CA bundle holds no PEM certificate")
	}
	c.transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return nil
}

// CheckTarget applies the target policy to a webhook URL without any
// network access: HTTPS only, no credentials or fragment, a host on the
// allow list, and no literal non-public address. Host names are resolved and
// checked again on every connection.
func (c *Client) CheckTarget(rawURL string) error {
	if len(rawURL) > 2048 {
		return ErrDenied
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || c.validate(u) != nil || u.RawQuery != "" && !validQuery(u.RawQuery) {
		return ErrDenied
	}
	return nil
}

func validQuery(q string) bool {
	for i := 0; i < len(q); i++ {
		if q[i] < 0x21 || q[i] > 0x7e {
			return false
		}
	}
	return true
}

// Post sends one HTTPS POST and never follows redirects: a 3xx is returned
// as the result. Every connection, including DNS answers, goes through the
// same target policy as Fetch. Errors are ErrDenied (target policy),
// ErrCertificate, context.DeadlineExceeded (timeout elapsed),
// ErrUnavailable (any other network failure) or the parent context's error
// when it was cancelled. Raw transport errors never escape.
func (c *Client) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (PostResult, error) {
	if err := ctx.Err(); err != nil {
		return PostResult{}, err
	}
	if timeout <= 0 || timeout > maxPostTimeout || len(body) > MaxPostBytes || c.CheckTarget(rawURL) != nil {
		return PostResult{}, ErrDenied
	}
	attempt, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if c.budget != nil {
		release, err := c.budget.Acquire(attempt, app.WorkIO)
		if err != nil {
			return PostResult{}, postError(ctx, attempt, err)
		}
		defer release()
	}
	r, err := http.NewRequestWithContext(attempt, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return PostResult{}, ErrDenied
	}
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	r.Header.Set("User-Agent", "Jelee-Webhook")
	client := &http.Client{Transport: c.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		return PostResult{}, postError(ctx, attempt, err)
	}
	defer response.Body.Close()
	// Drain a little so the connection can be reused; the body is ignored.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return PostResult{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}, nil
}

func postError(parent, attempt context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if attempt.Err() != nil {
		return context.DeadlineExceeded
	}
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &verification) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return ErrCertificate
	}
	for _, known := range []error{ErrDenied, ErrRedirect} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}
