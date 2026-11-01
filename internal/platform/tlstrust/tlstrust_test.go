package tlstrust_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust"
	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
)

func TestClientConfigTrustsOnlyTheCAFile(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "collector ca")
	srv := ca.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	get := func(c *tls.Config) error {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: c}}
		defer client.CloseIdleConnections()
		resp, err := client.Get(srv.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	pool, err := tlstrust.LoadPool(ca.WriteFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := get(tlstrust.ClientConfig(pool)); err != nil {
		t.Fatalf("certificate signed by the CA file refused: %v", err)
	}
	// Without the CA file the system roots do not know the private CA.
	var unknown x509.UnknownAuthorityError
	if err := get(tlstrust.ClientConfig(nil)); !errors.As(err, &unknown) {
		t.Fatalf("system roots accepted a private CA: %v", err)
	}
	// Another CA does not vouch for the server either.
	other, err := tlstrust.LoadPool(tlstrusttest.NewCA(t, "other ca").WriteFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := get(tlstrust.ClientConfig(other)); !errors.As(err, &unknown) {
		t.Fatalf("certificate of another CA accepted: %v", err)
	}
}

func TestClientConfigNeverSkipsVerification(t *testing.T) {
	pool, _ := tlstrust.ParsePool(tlstrusttest.NewCA(t, "ca").PEM)
	for _, c := range []*tls.Config{tlstrust.ClientConfig(nil), tlstrust.ClientConfig(pool)} {
		if c.InsecureSkipVerify || c.VerifyPeerCertificate != nil || c.VerifyConnection != nil || c.MinVersion < tls.VersionTLS12 {
			t.Fatalf("weakened configuration %+v", c)
		}
	}
	if tlstrust.ClientConfig(nil).RootCAs != nil || tlstrust.ClientConfig(pool).RootCAs != pool {
		t.Fatal("RootCAs must be set only for a CA file")
	}
	data, err := os.ReadFile("tlstrust.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"InsecureSkipVerify", "VerifyPeerCertificate", "VerifyConnection"} {
		if strings.Contains(string(data), banned) {
			t.Errorf("tlstrust.go uses %s", banned)
		}
	}
	if n := strings.Count(string(data), "RootCAs ="); n != 1 || !strings.Contains(string(data), "if pool != nil {\n\t\tc.RootCAs = pool") {
		t.Errorf("RootCAs must be assigned once, from a CA file pool (%d assignments)", n)
	}
}

func TestLoadPoolRefusesBadFiles(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "ca")
	second := tlstrusttest.NewCA(t, "second")
	if _, err := tlstrust.LoadPool(ca.WriteFile(t, second.PEM)); err != nil {
		t.Fatalf("bundle of two certificates refused: %v", err)
	}
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, tc := range map[string]struct {
		path string
		want error
	}{
		"relative":    {"ca.pem", tlstrust.ErrPath},
		"empty":       {"", tlstrust.ErrPath},
		"line break":  {filepath.Join(dir, "a\nb"), tlstrust.ErrPath},
		"missing":     {filepath.Join(dir, "missing.pem"), tlstrust.ErrRead},
		"directory":   {dir, tlstrust.ErrRead},
		"not pem":     {write("text.pem", "not a certificate"), tlstrust.ErrContent},
		"empty file":  {write("empty.pem", ""), tlstrust.ErrContent},
		"private key": {write("key.pem", string(ca.PEM)+"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"), tlstrust.ErrContent},
		"broken der":  {write("broken.pem", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), tlstrust.ErrContent},
		"trailing":    {write("trailing.pem", string(ca.PEM)+"garbage"), tlstrust.ErrContent},
		"too large":   {write("large.pem", string(ca.PEM)+strings.Repeat("\n", tlstrust.MaxFileBytes)), tlstrust.ErrContent},
		"pem headers": {write("headers.pem", strings.Replace(string(ca.PEM), "-----\n", "-----\nProc-Type: 4,ENCRYPTED\n\n", 1)), tlstrust.ErrContent},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tlstrust.LoadPool(tc.path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err != nil && (strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "garbage")) {
				t.Fatalf("error echoes the path or contents: %v", err)
			}
		})
	}
}
