package outbound

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// NewMappedStreamTestClient exists only in the augmented test binary. It
// accepts any public host so redirect and image-host policy can be exercised.
func NewMappedStreamTestClient(lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool) (*Client, error) {
	c, err := newClient(nil, lookup, dial)
	if err != nil {
		return nil, err
	}
	c.transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return c, nil
}

func NewMappedStreamTestClientWithBudget(lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error), roots *x509.CertPool, budget app.WorkBudget) (*Client, error) {
	c, err := NewMappedStreamTestClient(lookup, dial, roots)
	if err == nil {
		c.budget = budget
	}
	return c, err
}
