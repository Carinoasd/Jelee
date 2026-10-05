package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// NewMappedTestClient exists only in the augmented test binary. Production
// consumers have no resolver/dialer/CA replacement API.
func NewMappedTestClient(lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool) (*Client, error) {
	c, err := newClient([]string{"api.themoviedb.org"}, lookup, dial)
	if err != nil {
		return nil, err
	}
	c.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return c, nil
}

func NewMappedTestClientWithBudget(lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool, budget app.WorkBudget) (*Client, error) {
	c, err := NewMappedTestClient(lookup, dial, roots)
	if err == nil {
		c.budget = budget
	}
	return c, err
}

// NewMappedWebhookTestClient is NewWebhookClient with a test resolver and
// dialer. Only the augmented test binary can replace either.
func NewMappedWebhookTestClient(hosts []string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error), extraRoots []byte) (*Client, error) {
	c, err := newClient(hosts, lookup, dial)
	if err != nil {
		return nil, err
	}
	return c, c.configureWebhook(extraRoots)
}
