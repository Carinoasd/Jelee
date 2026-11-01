package logging

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
)

// fakeOTLP is a ForwardSender that records its target and answers with
// scripted errors.
type fakeOTLP struct {
	mu      sync.Mutex
	target  OTLPTarget
	errs    []error
	got     [][]byte
	calls   atomic.Int64
	closed  atomic.Bool
	factory error
}

func (f *fakeOTLP) Send(_ context.Context, records [][]byte) error {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	f.got = append(f.got, records...)
	return nil
}

func (f *fakeOTLP) Close() { f.closed.Store(true) }

// withOTLP installs a sender factory for one test; the external tests of
// this directory link the real one.
func withOTLP(t *testing.T, fake *fakeOTLP) {
	t.Helper()
	previous := otlpFactory.Load()
	t.Cleanup(func() { otlpFactory.Store(previous) })
	if fake == nil {
		RegisterOTLPSender(nil)
		return
	}
	RegisterOTLPSender(func(target OTLPTarget) (ForwardSender, error) {
		fake.target = target
		if fake.factory != nil {
			return nil, fake.factory
		}
		return fake, nil
	})
}

func TestOTLPOptionsValidation(t *testing.T) {
	caFile := tlstrusttest.NewCA(t, "ca").WriteFile(t)
	many := map[string]string{}
	for i := range OTLPMaxHeaders + 1 {
		many["X-H"+string(rune('a'+i))] = "v"
	}
	cases := map[string]ForwarderOptions{
		"no endpoint":        {Kind: ForwardOTLP},
		"headers only":       {Kind: ForwardOTLP, OTLP: OTLPOptions{Headers: map[string]string{"Authorization": "Bearer t"}}},
		"scheme":             {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "grpc://collector:4317"}},
		"relative":           {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "collector:4318"}},
		"userinfo":           {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://u:p@collector"}},
		"query":              {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector/?token=x"}},
		"fragment":           {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector/#x"}},
		"port":               {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector:99999"}},
		"plain remote":       {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "http://10.0.0.5:4318", Insecure: true}},
		"plain name":         {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "http://localhost:4318", Insecure: true}},
		"plain no flag":      {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "http://127.0.0.1:4318"}},
		"https insecure":     {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Insecure: true}},
		"header name":        {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: map[string]string{"Bad Name": "v"}}},
		"reserved header":    {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: map[string]string{"Content-Type": "text/plain"}}},
		"header line break":  {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: map[string]string{"Authorization": "a\r\nX: y"}}},
		"empty header":       {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: map[string]string{"X-Tenant": ""}}},
		"too many headers":   {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: many}},
		"headers too large":  {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", Headers: map[string]string{"Authorization": strings.Repeat("a", OTLPMaxHeaderLen)}}},
		"ca with http":       {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "http://127.0.0.1:4318", Insecure: true, CAFile: caFile}},
		"ca missing":         {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", CAFile: filepath.Join(t.TempDir(), "missing.pem")}},
		"ca relative":        {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector", CAFile: "ca.pem"}},
		"otlp with loki":     {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector"}, Loki: LokiOptions{URL: "https://loki"}},
		"otlp with syslog":   {Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector"}, Syslog: SyslogOptions{Address: "x:1"}},
		"loki with otlp":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki"}, OTLP: OTLPOptions{Insecure: true}},
		"syslog with otlp":   {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1"}, OTLP: OTLPOptions{Endpoint: "https://collector"}},
		"otlp bad queue":     {Kind: ForwardOTLP, QueueEntries: -1, OTLP: OTLPOptions{Endpoint: "https://collector"}},
		"otlp bad interval":  {Kind: ForwardOTLP, BatchInterval: time.Hour, OTLP: OTLPOptions{Endpoint: "https://collector"}},
		"otlp bad batch":     {Kind: ForwardOTLP, BatchSize: MaxForwardBatchSize + 1, OTLP: OTLPOptions{Endpoint: "https://collector"}},
		"otlp bad timeout":   {Kind: ForwardOTLP, Timeout: time.Hour, OTLP: OTLPOptions{Endpoint: "https://collector"}},
		"otlp bad dest name": {Kind: ForwardOTLP, Name: "Bad Name", OTLP: OTLPOptions{Endpoint: "https://collector"}},
	}
	for name, o := range cases {
		err := o.validate("linux")
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		for _, secret := range []string{"u:p@", "token=x", "Bearer", "10.0.0.5", "Bad Name", "a\r\n", filepath.Dir(caFile)} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error repeats a configured value: %v", name, err)
			}
		}
	}
	for name, o := range map[string]OTLPOptions{
		"https":            {Endpoint: "https://otel-collector.internal:4318"},
		"https prefix":     {Endpoint: "https://gateway.example.org/otlp/", Headers: map[string]string{"Authorization": "Bearer t", "X-Scope-OrgID": "home"}},
		"loopback v4":      {Endpoint: "http://127.0.0.1:4318", Insecure: true},
		"loopback v6":      {Endpoint: "http://[::1]:4318/v1/logs", Insecure: true},
		"https with ca":    {Endpoint: "https://collector.internal", CAFile: caFile},
		"16 small headers": {Endpoint: "https://collector", Headers: func() map[string]string { delete(many, "X-Ha"); return many }()},
	} {
		if err := (ForwarderOptions{Kind: ForwardOTLP, OTLP: o}).validate("windows"); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

func TestOTLPLogsPathIsAppended(t *testing.T) {
	for endpoint, want := range map[string]string{
		"https://c:4318":               "https://c:4318/v1/logs",
		"https://c:4318/":              "https://c:4318/v1/logs",
		"https://c/otlp":               "https://c/otlp/v1/logs",
		"https://c/otlp/v1/logs":       "https://c/otlp/v1/logs",
		"https://c/v1/logs/":           "https://c/v1/logs",
		"http://127.0.0.1:4318/v1/log": "http://127.0.0.1:4318/v1/log/v1/logs",
	} {
		u, err := OTLPOptions{Endpoint: endpoint, Insecure: strings.HasPrefix(endpoint, "http:")}.parseURL()
		if err != nil || u.String() != want {
			t.Errorf("%s: %v %v, want %s", endpoint, u, err, want)
		}
	}
}

func TestOTLPNeedsTheLinkedSender(t *testing.T) {
	withOTLP(t, nil)
	o := ForwarderOptions{Kind: ForwardOTLP, OTLP: OTLPOptions{Endpoint: "https://collector.example.org"}}
	if err := o.Validate(); err != nil {
		t.Fatalf("the configuration check needs no sender: %v", err)
	}
	if _, err := NewForwarder(o); !errors.Is(err, errOTLPUnavailable) {
		t.Fatalf("NewForwarder without a sender = %v", err)
	}
	if _, err := Open(Options{Forward: []ForwarderOptions{o}}, &syncBuffer{}); err == nil {
		t.Fatal("Open accepted an otlp forwarder without a sender")
	}
	fake := &fakeOTLP{factory: errors.New("cannot reach https://collector.example.org with Bearer s3cret")}
	withOTLP(t, fake)
	if _, err := NewForwarder(o); err == nil || strings.Contains(err.Error(), "collector.example.org") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("factory error = %v", err)
	}
}

func TestOTLPTargetClientIsLockedDown(t *testing.T) {
	caFile := tlstrusttest.NewCA(t, "ca").WriteFile(t)
	for _, ca := range []string{"", caFile} {
		fake := &fakeOTLP{}
		withOTLP(t, fake)
		headers := map[string]string{"Authorization": "Bearer t"}
		f := mustForwarder(t, ForwarderOptions{Kind: ForwardOTLP, Name: "collector", Timeout: 3 * time.Second,
			OTLP: OTLPOptions{Endpoint: "https://collector.internal:4318/base", Headers: headers, CAFile: ca}})
		headers["Authorization"] = "changed"
		target := fake.target
		if target.URL != "https://collector.internal:4318/base/v1/logs" || target.Headers["Authorization"] != "Bearer t" || target.Timeout != 3*time.Second {
			t.Fatalf("target %+v", target)
		}
		if f.Name() != "collector" || f.Kind() != ForwardOTLP {
			t.Fatalf("forwarder %s %s", f.Name(), f.Kind())
		}
		client := target.Client
		transport := client.Transport.(*http.Transport)
		if client.Timeout != 3*time.Second || client.CheckRedirect == nil || client.CheckRedirect(nil, nil) != http.ErrUseLastResponse || transport.Proxy != nil {
			t.Fatal("the client follows redirects, uses a proxy or has no timeout")
		}
		c := transport.TLSClientConfig
		if c.InsecureSkipVerify || c.MinVersion < tls.VersionTLS12 || (c.RootCAs != nil) != (ca != "") {
			t.Fatalf("ca %q: roots set %v, skip %v, min %#x", ca, c.RootCAs != nil, c.InsecureSkipVerify, c.MinVersion)
		}
		_ = f.Close()
		if !fake.closed.Load() {
			t.Fatal("Close did not reach the sender")
		}
	}
}

func TestOTLPSenderFailuresFollowTheCorePolicy(t *testing.T) {
	fake := &fakeOTLP{errs: []error{
		errors.New("dial tcp 10.0.0.5:4318: connection refused"), // unclassified: retried as network
		ForwardFailure("status 503", true, 0),
	}}
	withOTLP(t, fake)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardOTLP, BatchInterval: 10 * time.Millisecond, OTLP: OTLPOptions{Endpoint: "https://collector"}})
	var mu sync.Mutex
	var notes []string
	f.notify = func(s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "delivery after two retries", func() bool { return f.ForwardStat().Sent == 1 })
	if st := f.ForwardStat(); st.Failures != 2 || st.Dropped != 0 || fake.calls.Load() != 3 {
		t.Fatalf("stat %+v calls %d", st, fake.calls.Load())
	}
	mu.Lock()
	if len(notes) != 2 || !strings.Contains(notes[0], "(network)") || strings.Contains(strings.Join(notes, " "), "10.0.0.5") {
		t.Fatalf("notes %q", notes)
	}
	mu.Unlock()

	// A rejection is dropped and counted at once.
	fake.mu.Lock()
	fake.errs = []error{ForwardFailure("rejected 401", false, 0)}
	fake.mu.Unlock()
	calls := fake.calls.Load()
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "drop", func() bool { return f.ForwardStat().Dropped == 1 })
	time.Sleep(30 * time.Millisecond)
	if fake.calls.Load() != calls+1 {
		t.Fatalf("a rejected batch was retried: %d calls", fake.calls.Load()-calls)
	}
	if ForwardRetryAfter("7") != 7*time.Second || ForwardRetryAfter("soon") != 0 {
		t.Fatal("Retry-After parsing")
	}
	if err := ForwardNetworkFailure(context.DeadlineExceeded); !strings.HasSuffix(err.Error(), ": timeout") {
		t.Fatalf("deadline classified as %v", err)
	}
}
