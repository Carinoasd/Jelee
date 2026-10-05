package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
)

// G45.4 relax_ssrf_strict: private and loopback targets are admitted only
// while the switch is on; link-local (cloud metadata) never is, and the
// strict policy returns as soon as the switch ends.
func TestPrivateTargetsFollowTheDeveloperSwitch(t *testing.T) {
	var relaxed atomic.Bool
	var dialed atomic.Int32
	answer := netip.MustParseAddr("127.0.0.1")
	c, err := newClient(nil, func(context.Context, string, string) ([]netip.Addr, error) { return []netip.Addr{answer}, nil }, func(context.Context, string, string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("dial refused by test")
	})
	if err != nil {
		t.Fatal(err)
	}
	c.AllowPrivateTargets(relaxed.Load)
	fetch := func() error {
		_, err := c.Fetch(context.Background(), "http://fetch.example/", 128)
		return err
	}
	if err = fetch(); !errors.Is(err, ErrDenied) || dialed.Load() != 0 {
		t.Fatalf("strict: %v dialed=%d", err, dialed.Load())
	}
	relaxed.Store(true)
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.5", "100.64.0.1", "fd00::1", "::1"} {
		answer = netip.MustParseAddr(a)
		before := dialed.Load()
		if err = fetch(); errors.Is(err, ErrDenied) || dialed.Load() != before+1 {
			t.Fatalf("relaxed %s: %v", a, err)
		}
	}
	for _, a := range []string{"169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1", "192.0.2.1"} {
		answer = netip.MustParseAddr(a)
		if err = fetch(); !errors.Is(err, ErrDenied) {
			t.Fatalf("relaxed admitted %s: %v", a, err)
		}
	}
	relaxed.Store(false)
	answer = netip.MustParseAddr("10.1.2.3")
	if err = fetch(); !errors.Is(err, ErrDenied) {
		t.Fatalf("strict again: %v", err)
	}
	if err = c.CheckTarget("https://10.1.2.3/hook"); !errors.Is(err, ErrDenied) {
		t.Fatalf("literal private target: %v", err)
	}
	relaxed.Store(true)
	if err = c.CheckTarget("https://10.1.2.3/hook"); err != nil {
		t.Fatalf("relaxed literal private target: %v", err)
	}
}
