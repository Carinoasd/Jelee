package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicAddressPolicy(t *testing.T) {
	denied := []string{"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.1.1", "127.0.0.1", "169.254.169.254", "172.16.1.1", "192.0.0.9", "192.0.2.1", "192.31.196.1", "192.52.193.1", "192.88.99.1", "192.168.1.1", "192.175.48.1", "198.18.1.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.1.2.3", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1", "fe80::1%eth0", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:1::1", "2001:db8::1", "2002:7f00:1::1", "2620:4f:8000::1", "3fff::1", "5f00::1"}
	for _, raw := range denied {
		t.Run(raw, func(t *testing.T) {
			if publicAddress(netip.MustParseAddr(raw)) {
				t.Fatal("special address permitted")
			}
		})
	}
	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "::ffff:8.8.8.8", "2606:4700:4700::1111"} {
		t.Run(raw, func(t *testing.T) {
			if !publicAddress(netip.MustParseAddr(raw)) {
				t.Fatal("public address rejected")
			}
		})
	}
	if publicAddress(netip.Addr{}) {
		t.Fatal("invalid address permitted")
	}
}

func mappedClient(t *testing.T, server *httptest.Server, lookup lookupFunc) (*Client, *atomic.Int32) {
	t.Helper()
	dials := new(atomic.Int32)
	c, err := newClient(nil, lookup, func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "93.184.216.34" {
			return nil, fmt.Errorf("unexpected dial destination")
		}
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c, dials
}

func publicLookup(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func TestFetchValidatesURLBeforeNetwork(t *testing.T) {
	lookups := 0
	c, err := newClient([]string{"fetch.example"}, func(context.Context, string, string) ([]netip.Addr, error) { lookups++; return nil, nil }, func(context.Context, string, string) (net.Conn, error) { t.Fatal("unexpected dial"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"file:///secret", "http://user:secret@fetch.example/a", "http://fetch.example:0/a", "http://fetch.example:65536/a", "http://fetch.example:/a", "http://fetch.example/a#secret", "http://other.example/a", "http://127.0.0.1/a", "http://[fe80::1%25eth0]/a", "http://fetch.example./a", "http://fetch.example\\evil/a", "//fetch.example/a", "http://fetch.example:notport/a"} {
		t.Run(raw, func(t *testing.T) {
			_, err := c.Fetch(context.Background(), raw, 128)
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("URL leaked")
			}
		})
	}
	if lookups != 0 {
		t.Fatal("invalid targets reached resolver")
	}
	for _, hosts := range [][]string{{"bad host"}, {"*"}, {"example.com."}, make([]string, 65)} {
		if _, err := New(hosts); !errors.Is(err, ErrDenied) {
			t.Fatal("invalid host policy permitted")
		}
	}
}

func TestDNSAnswersRejectedBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []netip.Addr
	}{
		{"private", []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{"mixed", []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("169.254.169.254")}},
		{"mapped", []netip.Addr{netip.MustParseAddr("::ffff:10.0.0.1")}},
		{"empty", nil}, {"invalid", []netip.Addr{{}}}, {"too_many", make([]netip.Addr, 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(nil, func(context.Context, string, string) ([]netip.Addr, error) { return tc.answers, nil }, func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("DNS guard bypassed")
				return nil, nil
			})
			_, err := c.Fetch(context.Background(), "http://fetch.example/?secret=token", 128)
			if !errors.Is(err, ErrDenied) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPinnedDialAndFreshConnectionRebinding(t *testing.T) {
	requests := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != "fetch.example" {
			t.Error("original host changed")
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	lookups := 0
	c, dials := mappedClient(t, srv, func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return publicLookup(nil, "", "")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	r, err := c.Fetch(context.Background(), "http://fetch.example/", 128)
	if err != nil || r.Status != 200 || string(r.Body) != "ok" {
		t.Fatalf("response=%v error=%v", r, err)
	}
	c.CloseIdleConnections()
	_, err = c.Fetch(context.Background(), "http://fetch.example/", 128)
	if !errors.Is(err, ErrDenied) || lookups != 2 || dials.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("error=%v lookups=%d dials=%d requests=%d", err, lookups, dials.Load(), requests.Load())
	}
}

func TestRedirectsNeverContactSecondTarget(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/?secret=token", "http://169.254.169.254/", "http://fetch.example/next", "http://other.example/next"} {
		t.Run(target, func(t *testing.T) {
			requests := new(atomic.Int32)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Redirect(w, r, target, 302) }))
			defer srv.Close()
			c, dials := mappedClient(t, srv, publicLookup)
			_, err := c.Fetch(context.Background(), "http://fetch.example/?api_key=secret", 128)
			if !(errors.Is(err, ErrDenied) || errors.Is(err, ErrRedirect)) || requests.Load() != 1 || dials.Load() != 1 {
				t.Fatalf("error=%v requests=%d dials=%d", err, requests.Load(), dials.Load())
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "fetch.example") {
				t.Fatal("redirect error leaked URL")
			}
		})
	}
}

func TestBodyBoundAndSlowBodyCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 129)) }))
	defer srv.Close()
	c, _ := mappedClient(t, srv, publicLookup)
	if _, err := c.Fetch(context.Background(), "http://fetch.example/", 128); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error=%v", err)
	}
	if _, err := c.Fetch(context.Background(), "http://fetch.example/", 0); !errors.Is(err, ErrDenied) {
		t.Fatal("zero bound accepted")
	}
	if _, err := c.Fetch(context.Background(), "http://fetch.example/", (16<<20)+1); !errors.Is(err, ErrDenied) {
		t.Fatal("excessive bound accepted")
	}
	done := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer slow.Close()
	sc, _ := mappedClient(t, slow, publicLookup)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := sc.Fetch(ctx, "http://fetch.example/", 128); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("body cancellation did not reach server")
	}
}

func TestTLSValidationAndEnvironmentProxyIsolation(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	srv.Config.ErrorLog = nil
	host := "example.com"
	if len(srv.Certificate().DNSNames) == 0 {
		t.Fatal("test certificate has no DNS name")
	}
	host = srv.Certificate().DNSNames[0]
	c, _ := mappedClient(t, srv, publicLookup)
	if c.transport.Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
	if _, err := c.Fetch(context.Background(), "https://"+host+"/?secret=token", 128); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid certificate error=%v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c.transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	r, err := c.Fetch(context.Background(), "https://"+host+"/", 128)
	if err != nil || string(r.Body) != "ok" {
		t.Fatalf("TLS response=%v error=%v", r, err)
	}
	if _, err := c.Fetch(context.Background(), "https://wrong.example/", 128); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("hostname verification bypassed: %v", err)
	}
}

func TestResolverCancellationAndSafeErrors(t *testing.T) {
	c, _ := newClient(nil, func(ctx context.Context, _, _ string) ([]netip.Addr, error) { <-ctx.Done(); return nil, ctx.Err() }, func(context.Context, string, string) (net.Conn, error) { t.Fatal("unexpected dial"); return nil, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx, "http://fetch.example/?secret=token", 128); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.Fetch(cancelled, "http://fetch.example/", 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	c, _ = newClient(nil, func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("private-dns secret 10.0.0.1")
	}, func(context.Context, string, string) (net.Conn, error) { return nil, nil })
	if _, err := c.Fetch(context.Background(), "http://fetch.example/?secret=token", 128); err != ErrUnavailable {
		t.Fatalf("unsafe resolver error=%v", err)
	}
}

// cancelAtEOFBody plays a peer that answers the client's cancellation by
// terminating the body cleanly: the context ends during the read that
// returns io.EOF, before the caller can observe it.
type cancelAtEOFBody struct {
	data   []byte
	cancel context.CancelFunc
}

func (b *cancelAtEOFBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	if b.cancel != nil {
		b.cancel()
	}
	return 0, io.EOF
}

func (b *cancelAtEOFBody) Close() error { return nil }

func TestStreamBodyEOFAfterCancellationIsNotCompletion(t *testing.T) {
	// The cancellation lands while the final read is in flight.
	cancelled, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The attempt deadline has already passed when the peer ends the body.
	expired, stop := context.WithDeadline(context.Background(), time.Now())
	defer stop()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		peer *cancelAtEOFBody
		want error
	}{
		{"cancelled during read", cancelled, &cancelAtEOFBody{data: []byte("partial"), cancel: cancel}, context.Canceled},
		{"deadline passed", expired, &cancelAtEOFBody{data: []byte("partial")}, context.DeadlineExceeded},
	} {
		body := &streamBody{ctx: tc.ctx, body: tc.peer, limit: 64, done: func() {}}
		data, err := io.ReadAll(body)
		if !errors.Is(err, tc.want) || string(data) != "partial" {
			t.Fatalf("%s: data=%q error=%v want %v", tc.name, data, err, tc.want)
		}
		// The failure sticks: a later read cannot turn into a clean end.
		if _, err := body.Read(make([]byte, 8)); !errors.Is(err, tc.want) {
			t.Fatalf("%s: second read error=%v", tc.name, err)
		}
	}
	// A body that ends while the context is live still reports io.EOF.
	body := &streamBody{ctx: context.Background(), body: &cancelAtEOFBody{data: []byte("complete")}, limit: 64, done: func() {}}
	if data, err := io.ReadAll(body); err != nil || string(data) != "complete" {
		t.Fatalf("data=%q error=%v", data, err)
	}
}
