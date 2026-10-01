// Package outbound owns the transport used by server-side remote fetches.
package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrDenied      = errors.New("outbound_target_denied")
	ErrUnavailable = errors.New("outbound_unavailable")
	ErrTooLarge    = errors.New("outbound_response_too_large")
	ErrRedirect    = errors.New("outbound_redirect_denied")
)

type lookupFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

// Client's transport cannot be replaced by a consuming adapter.
type Client struct {
	http      *http.Client
	transport *http.Transport
	hosts     map[string]bool
	lookup    lookupFunc
	dial      dialFunc
}

type Response struct {
	Status     int
	Body       []byte
	RetryAfter string
}

// New accepts exact host names. An empty list allows any public target;
// capability-specific adapters should supply their own restricted list.
func New(hosts []string) (*Client, error) {
	return newClient(hosts, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
}

func newClient(hosts []string, lookup lookupFunc, dial dialFunc) (*Client, error) {
	if len(hosts) > 64 || lookup == nil || dial == nil {
		return nil, ErrDenied
	}
	c := &Client{hosts: make(map[string]bool), lookup: lookup, dial: dial}
	for _, h := range hosts {
		if !validHost(h) {
			return nil, ErrDenied
		}
		c.hosts[strings.ToLower(h)] = true
	}
	c.transport = &http.Transport{Proxy: nil, DialContext: c.connect, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16,
		MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, MaxResponseHeaderBytes: 32 << 10, ForceAttemptHTTP2: true}
	c.http = &http.Client{Transport: c.transport, Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if err := c.validate(r.URL); err != nil {
			return err
		}
		// Credential preflight has no redirect use case. Fail closed before
		// sending any second request or forwarding a query credential.
		return ErrRedirect
	}}
	return c, nil
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || strings.Contains(h, "%") {
		return false
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func (c *Client) validate(u *url.URL) error {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Opaque != "" || u.Fragment != "" || !validHost(u.Hostname()) || strings.HasSuffix(u.Host, ":") {
		return ErrDenied
	}
	h := strings.ToLower(u.Hostname())
	if len(c.hosts) > 0 && !c.hosts[h] {
		return ErrDenied
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ErrDenied
		}
	}
	if a, err := netip.ParseAddr(h); err == nil && !publicAddress(a) {
		return ErrDenied
	}
	return nil
}

// Fetch buffers a bounded GET response and owns body closure and cancellation.
// Raw transport errors (including url.Error and DNS addresses) never escape.
func (c *Client) Fetch(ctx context.Context, rawURL string, maxBytes int64) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if maxBytes < 1 || maxBytes > 16<<20 || len(rawURL) > 8192 {
		return Response{}, ErrDenied
	}
	u, err := url.Parse(rawURL)
	if err != nil || c.validate(u) != nil {
		return Response{}, ErrDenied
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Response{}, ErrDenied
	}
	r.Header.Set("User-Agent", "Jelee")
	r.Header.Set("Accept", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	if int64(len(data)) > maxBytes {
		return Response{}, ErrTooLarge
	}
	return Response{Status: response.StatusCode, Body: data, RetryAfter: response.Header.Get("Retry-After")}, nil
}

func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrDenied, ErrRedirect} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}

func (c *Client) connect(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !validHost(host) {
		return nil, ErrDenied
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, ErrDenied
	}
	if len(c.hosts) > 0 && !c.hosts[strings.ToLower(host)] {
		return nil, ErrDenied
	}
	budget, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var addresses []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{a}
	} else {
		addresses, err = c.lookup(budget, "ip", host)
		if err != nil {
			return nil, safeError(budget, err)
		}
	}
	if len(addresses) == 0 || len(addresses) > 64 {
		return nil, ErrDenied
	}
	for _, a := range addresses {
		if !publicAddress(a) {
			return nil, ErrDenied
		}
	}
	for _, a := range addresses {
		// No second hostname resolution is possible in the actual dial.
		conn, err := c.dial(budget, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		if budget.Err() != nil {
			return nil, budget.Err()
		}
	}
	return nil, ErrUnavailable
}

func (c *Client) CloseIdleConnections() { c.transport.CloseIdleConnections() }

// Conservative fetch policy, checked against IANA special-purpose registries.
// Special assignments are excluded even where a more specific public-purpose
// exception exists. IPv6 is restricted to current global unicast allocation.
func publicAddress(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	var ranges []string
	if a.Is4() {
		ranges = []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"}
	} else {
		if !netip.MustParsePrefix("2000::/3").Contains(a) {
			return false
		}
		ranges = []string{"2001::/23", "2001:db8::/32", "2002::/16", "2620:4f:8000::/48", "3fff::/20"}
	}
	for _, r := range ranges {
		if netip.MustParsePrefix(r).Contains(a) {
			return false
		}
	}
	return true
}
