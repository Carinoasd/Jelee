package outbound_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func providerCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spec := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "provider test"}, DNSNames: []string{"api.themoviedb.org"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, spec, spec, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func TestTMDBActualAdapterThroughGuardedTLS(t *testing.T) {
	cert, roots := providerCertificate(t)
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		dnsPrivate bool
		want       error
	}{
		{"success", 200, `{"success":true,"status_code":1}`, false, nil},
		{"invalid_credentials", 401, `{"error":"secret"}`, false, metadata.ErrCredentials},
		{"quota_block", 429, "", false, metadata.ErrRateLimited},
		{"bad_response", 200, "secret", false, metadata.ErrResponse},
		{"private_dns", 200, `{"success":true,"status_code":1}`, true, metadata.ErrUnavailable},
		{"redirect", 302, "", false, metadata.ErrUnavailable},
		{"retry_after", 429, `{"success":true,"status_code":1}`, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := new(atomic.Int32)
			dials := new(atomic.Int32)
			key := strings.Repeat("a", 32)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if r.Host != "api.themoviedb.org" || r.URL.Path != "/3/authentication" || r.URL.Query().Get("api_key") != key || r.TLS == nil {
					t.Error("real provider request contract differs")
				}
				if tc.status == 302 {
					w.Header().Set("Location", "https://127.0.0.1/?secret="+key)
				}
				status := tc.status
				if tc.name == "retry_after" {
					if n == 1 {
						w.Header().Set("Retry-After", "1")
					} else {
						status = 200
					}
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(tc.body))
			}))
			srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			srv.StartTLS()
			defer srv.Close()
			c, err := outbound.NewMappedTestClient(func(context.Context, string, string) ([]netip.Addr, error) {
				if tc.dnsPrivate {
					return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
				}
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			}, func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "93.184.216.34:443" {
					t.Error("hostname re-resolved or IP pin lost")
				}
				dials.Add(1)
				return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
			}, roots)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := metadata.NewTMDBWithClient(key, c)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			started := time.Now()
			err = adapter.ValidateCredentials(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("adapter error=%v", err)
			}
			want := int32(1)
			if tc.dnsPrivate {
				want = 0
			}
			wantRequests := want
			if tc.status == 429 {
				wantRequests = 3
			}
			if tc.name == "retry_after" {
				wantRequests = 2
				if time.Since(started) < time.Second {
					t.Fatal("real HTTP Retry-After minimum was bypassed")
				}
			}
			if requests.Load() != wantRequests || dials.Load() != want {
				t.Fatalf("requests=%d dials=%d", requests.Load(), dials.Load())
			}
			if err != nil && (strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "127.0.0.1")) {
				t.Fatal("secret or address leaked")
			}
		})
	}
}
