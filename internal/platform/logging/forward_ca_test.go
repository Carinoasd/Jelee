package logging

import (
	"bufio"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
)

// A Loki behind a private CA is reached once its CA file is configured;
// another CA file, or none, refuses the certificate.
func TestLokiCAFileTrustsOnlyItsCertificates(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "loki ca")
	var hits atomic.Int64
	srv := ca.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusNoContent) }))

	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: srv.URL, CAFile: ca.WriteFile(t)}})
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "push over the private CA", func() bool { return f.ForwardStat().Sent == 1 })
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}

	for name, caFile := range map[string]string{"other CA": tlstrusttest.NewCA(t, "other").WriteFile(t), "system roots": ""} {
		t.Run(name, func(t *testing.T) {
			before := hits.Load()
			f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: srv.URL, CAFile: caFile}})
			f.maxAttempts = 1
			_, _ = f.Write([]byte(sampleRecord))
			eventually(t, "tls failure", func() bool { return f.ForwardStat().Dropped == 1 })
			if hits.Load() != before || f.ForwardStat().Sent != 0 {
				t.Fatalf("certificate of the private CA accepted: %+v", f.ForwardStat())
			}
		})
	}
}

// syslog over TLS to a collector behind a private CA.
func TestSyslogTLSCAFileTrustsOnlyItsCertificates(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "syslog ca")
	ln := ca.Listen(t)
	lines := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil {
					lines <- line
				}
			}()
		}
	}()
	addr := ln.Addr().String()

	f := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, BatchInterval: 10 * time.Millisecond,
		Syslog: SyslogOptions{Network: "tls", Address: addr, Framing: "newline", CAFile: ca.WriteFile(t)}})
	_, _ = f.Write([]byte(record("INFO", "http", "over tls")))
	select {
	case line := <-lines:
		if !strings.Contains(line, "over tls") {
			t.Fatalf("%q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no message over the private CA: %+v", f.ForwardStat())
	}

	other := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, BatchInterval: 10 * time.Millisecond,
		Syslog: SyslogOptions{Network: "tls", Address: addr, Framing: "newline", CAFile: tlstrusttest.NewCA(t, "other").WriteFile(t)}})
	other.maxAttempts = 1
	_, _ = other.Write([]byte(record("INFO", "http", "must not arrive")))
	eventually(t, "tls failure", func() bool { return other.ForwardStat().Dropped == 1 })
	select {
	case line := <-lines:
		t.Fatalf("certificate of the private CA accepted: %q", line)
	case <-time.After(50 * time.Millisecond):
	}
}

// RootCAs is set only from a configured CA file; nothing ever skips
// verification.
func TestForwarderTLSRootsComeOnlyFromTheCAFile(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "ca")
	caFile := ca.WriteFile(t)
	configs := func(caFile string) []*tls.Config {
		syslog := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "127.0.0.1:6514", CAFile: caFile}})
		loki := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, Loki: LokiOptions{URL: "https://127.0.0.1:3100", CAFile: caFile}})
		return []*tls.Config{syslog.sender.(*syslogSender).tlsConfig, loki.sender.(*lokiSender).client.Transport.(*http.Transport).TLSClientConfig}
	}
	for _, c := range configs("") {
		if c.RootCAs != nil || c.InsecureSkipVerify || c.MinVersion < tls.VersionTLS12 {
			t.Fatalf("without a CA file: roots set %v, skip %v, min %#x", c.RootCAs != nil, c.InsecureSkipVerify, c.MinVersion)
		}
	}
	for _, c := range configs(caFile) {
		if c.RootCAs == nil || c.InsecureSkipVerify || c.MinVersion < tls.VersionTLS12 {
			t.Fatalf("with a CA file: roots set %v, skip %v, min %#x", c.RootCAs != nil, c.InsecureSkipVerify, c.MinVersion)
		}
	}
}

func TestForwarderCAFileValidation(t *testing.T) {
	caFile := tlstrusttest.NewCA(t, "ca").WriteFile(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")
	notPEM := filepath.Join(filepath.Dir(caFile), "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]ForwarderOptions{
		"syslog udp":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "127.0.0.1:514", CAFile: caFile}},
		"syslog tcp":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: "127.0.0.1:514", CAFile: caFile}},
		"syslog missing":   {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "127.0.0.1:6514", CAFile: missing}},
		"syslog not pem":   {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "127.0.0.1:6514", CAFile: notPEM}},
		"syslog relative":  {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "127.0.0.1:6514", CAFile: "ca.pem"}},
		"loki http":        {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://127.0.0.1:3100", CAFile: caFile}},
		"loki missing":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki.example.org", CAFile: missing}},
		"loki not pem":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki.example.org", CAFile: notPEM}},
		"syslog loki file": {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "127.0.0.1:6514"}, Loki: LokiOptions{CAFile: caFile}},
	} {
		if err := o.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), filepath.Dir(caFile)) || strings.Contains(err.Error(), missing) {
			t.Errorf("%s: error names the path: %v", name, err)
		}
		if f, err := NewForwarder(o); err == nil {
			_ = f.Close()
			t.Errorf("%s built", name)
		}
	}
	for name, o := range map[string]ForwarderOptions{
		"syslog tls": {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "log.example:6514", CAFile: caFile}},
		"loki https": {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki.example.org", CAFile: caFile}},
	} {
		if err := o.Validate(); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}
