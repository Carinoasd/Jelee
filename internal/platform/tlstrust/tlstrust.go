// Package tlstrust builds the client TLS configuration of the outbound
// connections to collectors the operator runs: syslog over TLS, Loki and the
// OTLP trace exporter. Certificates are always verified. By default the
// system roots are trusted; a configured CA file (PEM, one or more
// certificates) replaces them, so a collector with a private or self-signed
// certificate works without turning verification off. There is no option
// that skips verification.
package tlstrust

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxFileBytes bounds a CA file; a bundle of a few hundred certificates
// fits.
const MaxFileBytes = 1 << 20

// Errors name neither the path nor the file contents.
var (
	ErrPath    = errors.New("CA file must be an absolute path")
	ErrRead    = errors.New("cannot read CA file")
	ErrContent = errors.New("CA file must hold one or more PEM certificates and nothing else")
)

// ValidPath reports whether path can name a CA file: absolute, without NUL
// or line breaks.
func ValidPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsAny(path, "\x00\r\n")
}

// LoadPool reads the PEM CA file at path into a pool that holds only its
// certificates. A file without a certificate, with a block of another type
// (such as a private key) or with a certificate that does not parse is
// refused.
func LoadPool(path string) (*x509.CertPool, error) {
	if !ValidPath(path) {
		return nil, ErrPath
	}
	f, err := os.Open(path) //nolint:gosec // G304: the operator names the CA file
	if err != nil {
		return nil, ErrRead
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	_ = f.Close()
	if err != nil {
		return nil, ErrRead
	}
	if len(data) > MaxFileBytes {
		return nil, ErrContent
	}
	return ParsePool(data)
}

// ParsePool parses PEM data like LoadPool.
func ParsePool(data []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	certificates := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, ErrContent
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, ErrContent
		}
		pool.AddCert(certificate)
		certificates++
	}
	if certificates == 0 || strings.TrimSpace(string(data)) != "" {
		return nil, ErrContent
	}
	return pool, nil
}

// ClientConfig is TLS 1.2 or later with full verification. A nil pool
// trusts the system roots; otherwise only the pool's certificates are
// trusted. Nothing else of the verification is configurable.
func ClientConfig(pool *x509.CertPool) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12}
	if pool != nil {
		c.RootCAs = pool
	}
	return c
}
