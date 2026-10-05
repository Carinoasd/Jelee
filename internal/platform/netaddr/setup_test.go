package netaddr_test

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/netaddr"
)

var _ app.SetupAddressParser = netaddr.Setup{}

func TestSetupAddressParser(t *testing.T) {
	p := netaddr.Setup{}
	for _, tc := range []struct {
		in             string
		ok, loop, zone bool
	}{{"127.0.0.1:8097", true, true, false}, {"[::1]:1", true, true, false}, {"0.0.0.0:8097", true, false, false}, {"[fe80::1%eth0]:80", true, false, true}, {"localhost:8097", false, false, false}} {
		got, ok := p.ParseListen(tc.in)
		if ok != tc.ok || ok && (got.Loopback != tc.loop || got.Zoned != tc.zone) {
			t.Errorf("ParseListen(%q)=%+v,%v", tc.in, got, ok)
		}
	}
	if !p.ValidProxyPrefix("10.0.0.0/8") || p.ValidProxyPrefix("10.0.0.1") || p.ValidProxyPrefix("fe80::/64%eth0") {
		t.Error("ValidProxyPrefix mismatch")
	}
}
