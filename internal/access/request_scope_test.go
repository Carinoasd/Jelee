package access

import (
	"net/netip"
	"testing"
)

func TestRequestScopeLAN(t *testing.T) {
	for address, lan := range map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "172.31.255.1": true, "192.168.0.10": true, "127.0.0.1": true, "::1": true, "fd00::1": true, "fc12::5": true,
		"::ffff:192.168.1.1": true, "172.32.0.1": false, "8.8.8.8": false, "203.0.113.1": false, "169.254.1.1": false, "fe80::1": false, "2001:db8::1": false,
	} {
		if got := (RequestScope{IP: netip.MustParseAddr(address)}).LAN(); got != lan {
			t.Errorf("%s: LAN %t, want %t", address, got, lan)
		}
	}
	if (RequestScope{}).LAN() {
		t.Fatal("unknown address on the LAN")
	}
}
