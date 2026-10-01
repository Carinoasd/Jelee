package config

import (
	"errors"
	"net/netip"
	"strings"
)

const MaxTrustedProxies = 64

// TrustedProxyPrefixes validates and copies configured CIDRs without exposing
// configuration values in errors. Returned prefixes are canonical and immutable.
func (c Config) TrustedProxyPrefixes() ([]netip.Prefix, error) {
	if len(c.TrustedProxies) > MaxTrustedProxies {
		return nil, errors.New("too many trustedProxies entries")
	}
	result := make([]netip.Prefix, 0, len(c.TrustedProxies))
	for _, value := range c.TrustedProxies {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Addr().Zone() != "" {
			return nil, errors.New("invalid trustedProxies CIDR")
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, errors.New("invalid trustedProxies mapped prefix")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}
