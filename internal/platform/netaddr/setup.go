// Package netaddr parses network addresses for application validators that
// must not depend on the net packages themselves.
package netaddr

import (
	"net/netip"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Setup implements app.SetupAddressParser with net/netip.
type Setup struct{}

func (Setup) ParseListen(value string) (domain.SetupListenAddress, bool) {
	listen, err := netip.ParseAddrPort(value)
	if err != nil {
		return domain.SetupListenAddress{}, false
	}
	return domain.SetupListenAddress{Port: listen.Port(), Loopback: listen.Addr().IsLoopback(), Zoned: listen.Addr().Zone() != ""}, true
}

func (Setup) ValidProxyPrefix(value string) bool {
	prefix, err := netip.ParsePrefix(value)
	return err == nil && prefix.Addr().Zone() == ""
}
