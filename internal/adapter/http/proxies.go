package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedBytes = 8192
const maxForwardedHops = 32

type clientAddressKey struct{}

func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if value, ok := r.Context().Value(clientAddressKey{}).(string); ok {
		return value
	}
	return ClientIP(r)
}

func trustedAddress(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func forwardingHeadersPresent(r *http.Request) bool {
	for name := range r.Header {
		lower := strings.ToLower(name)
		if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
			return true
		}
	}
	return false
}

// proxyClientIP never changes the transport peer or consumes forwarded Host,
// scheme, roles or credentials. reason is a fixed, safe warning code.
func proxyClientIP(r *http.Request, prefixes []netip.Prefix) (ip, reason string) {
	peer := ClientIP(r)
	address, err := netip.ParseAddr(peer)
	if err != nil || !trustedAddress(address, prefixes) {
		if forwardingHeadersPresent(r) {
			return peer, "untrusted_peer"
		}
		return peer, ""
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return peer, ""
	}
	size := len(values) - 1
	for _, value := range values {
		size += len(value)
		if size > maxForwardedBytes {
			return peer, "invalid_chain"
		}
	}
	nodes := strings.Split(strings.Join(values, ","), ",")
	if len(nodes) > maxForwardedHops {
		return peer, "invalid_chain"
	}
	chain := make([]netip.Addr, len(nodes))
	for index, node := range nodes {
		address, err := netip.ParseAddr(strings.TrimSpace(node))
		if err != nil || address.Zone() != "" {
			return peer, "invalid_chain"
		}
		chain[index] = address.Unmap()
	}
	for index := len(chain) - 1; index >= 0; index-- {
		if !trustedAddress(chain[index], prefixes) || index == 0 {
			return chain[index].String(), ""
		}
	}
	return peer, "invalid_chain"
}

func (s *Server) withClientAddress(r *http.Request, requestID string) *http.Request {
	address, reason := proxyClientIP(r, s.trustedProxies)
	if reason != "" {
		s.logger.Warn("forwarding headers ignored", "component", "http", "requestId", requestID, "reason", reason)
	}
	return r.WithContext(context.WithValue(r.Context(), clientAddressKey{}, address))
}
