package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	_ "github.com/MoYuanCN/Jelee/internal/platform/logging/otlplog"
)

func forwardLoad(t *testing.T, values map[string]string) (Config, error) {
	t.Helper()
	values["JELEE_DATABASE_URL"] = "postgres://localhost/jelee"
	return LoadWith(accountConfigLookup(values))
}

func TestLogForwardersAreOffByDefault(t *testing.T) {
	c, err := forwardLoad(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Logging.Forwarders) != 0 || len(c.Logging.Options().Forward) != 0 {
		t.Fatalf("forwarders by default: %+v", c.Logging.Forwarders)
	}
}

func TestLogForwardersFromEnvironment(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "loki-password")
	if err := os.WriteFile(secret, []byte("s3cret-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := forwardLoad(t, map[string]string{
		"JELEE_LOG_FORWARD_LOKI_URL":               "https://loki.example.org",
		"JELEE_LOG_FORWARD_LOKI_USERNAME":          "jelee",
		"JELEE_LOG_FORWARD_LOKI_PASSWORD_FILE":     secret,
		"JELEE_LOG_FORWARD_LOKI_TENANT_ID":         "home",
		"JELEE_LOG_FORWARD_LOKI_BATCH_SIZE":        "50",
		"JELEE_LOG_FORWARD_LOKI_BATCH_INTERVAL_MS": "250",
		"JELEE_LOG_FORWARD_LOKI_TIMEOUT_MS":        "3000",
		"JELEE_LOG_FORWARD_LOKI_QUEUE_ENTRIES":     "1000",
		"JELEE_LOG_FORWARD_SYSLOG_ADDRESS":         "log.example.org:514",
		"JELEE_LOG_FORWARD_SYSLOG_NETWORK":         "tcp",
		"JELEE_LOG_FORWARD_SYSLOG_FACILITY":        "daemon",
		"JELEE_LOG_FORWARD_SYSLOG_NAME":            "central",
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Logging.Options().Forward
	if len(opts) != 2 {
		t.Fatalf("%d forwarders", len(opts))
	}
	byKind := map[logging.ForwarderKind]logging.ForwarderOptions{}
	for _, o := range opts {
		byKind[o.Kind] = o
	}
	l := byKind[logging.ForwardLoki]
	if l.Loki.URL != "https://loki.example.org" || l.Loki.Username != "jelee" || l.Loki.Password != "s3cret-pass" || l.Loki.TenantID != "home" ||
		l.BatchSize != 50 || l.BatchInterval != 250*time.Millisecond || l.Timeout != 3*time.Second || l.QueueEntries != 1000 {
		t.Fatalf("loki options %+v", l)
	}
	s := byKind[logging.ForwardSyslog]
	if s.Name != "central" || s.Syslog.Network != "tcp" || s.Syslog.Address != "log.example.org:514" || s.Syslog.Facility != "daemon" {
		t.Fatalf("syslog options %+v", s)
	}
	// The password never reaches a serialized configuration.
	data, err := json.Marshal(c.Logging)
	if err != nil || strings.Contains(string(data), "s3cret-pass") {
		t.Fatalf("serialized logging config exposes the password (%v): %s", err, data)
	}
}

func TestLogForwardersFromFileAndEnvironmentReplacement(t *testing.T) {
	file := accountConfigFile(t, `{"logging":{"forwarders":[
		{"kind":"loki","url":"https://file.example.org","name":"file-loki"},
		{"kind":"syslog","address":"file.example.org:514"}]}}`)
	c, err := forwardLoad(t, map[string]string{"JELEE_CONFIG": file})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Logging.Forwarders) != 2 || c.Logging.Forwarders[0].Name != "file-loki" {
		t.Fatalf("file forwarders %+v", c.Logging.Forwarders)
	}
	c, err = forwardLoad(t, map[string]string{"JELEE_CONFIG": file, "JELEE_LOG_FORWARD_LOKI_URL": "https://env.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, f := range c.Logging.Forwarders {
		urls = append(urls, f.Kind+"="+f.URL+f.Address)
	}
	if strings.Join(urls, " ") != "syslog=file.example.org:514 loki=https://env.example.org" {
		t.Fatalf("environment did not replace the file's Loki entry: %v", urls)
	}
}

func TestLogForwardersRefuseHalfAndBadConfigurations(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(secretFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]string{
		"loki option without url":     {"JELEE_LOG_FORWARD_LOKI_USERNAME": "u"},
		"syslog option without addr":  {"JELEE_LOG_FORWARD_SYSLOG_FACILITY": "daemon"},
		"loki user without password":  {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_USERNAME": "u"},
		"loki password without user":  {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_PASSWORD": "p"},
		"basic and bearer":            {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_USERNAME": "u", "JELEE_LOG_FORWARD_LOKI_PASSWORD": "p", "JELEE_LOG_FORWARD_LOKI_BEARER_TOKEN": "t"},
		"two password sources":        {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_USERNAME": "u", "JELEE_LOG_FORWARD_LOKI_PASSWORD": "p", "JELEE_LOG_FORWARD_LOKI_PASSWORD_FILE": secretFile},
		"empty secret file":           {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_BEARER_TOKEN_FILE": secretFile},
		"missing secret file":         {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_BEARER_TOKEN_FILE": filepath.Join(t.TempDir(), "nope")},
		"relative secret file":        {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_BEARER_TOKEN_FILE": "token"},
		"clear text credentials":      {"JELEE_LOG_FORWARD_LOKI_URL": "http://l.example.org", "JELEE_LOG_FORWARD_LOKI_BEARER_TOKEN": "t"},
		"bad batch size":              {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_BATCH_SIZE": "many"},
		"batch size out of range":     {"JELEE_LOG_FORWARD_LOKI_URL": "https://l.example.org", "JELEE_LOG_FORWARD_LOKI_BATCH_SIZE": "99999999"},
		"timeout out of range":        {"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "a.example.org:514", "JELEE_LOG_FORWARD_SYSLOG_TIMEOUT_MS": "-1"},
		"syslog bad network":          {"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "a.example.org:514", "JELEE_LOG_FORWARD_SYSLOG_NETWORK": "pigeon"},
		"syslog address without port": {"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "a.example.org"},
	}
	for name, values := range cases {
		_, err := forwardLoad(t, values)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		for _, v := range values {
			if len(v) > 1 && v != "https://l.example.org" && strings.Contains(err.Error(), v) {
				t.Errorf("%s: error repeats %q: %v", name, v, err)
			}
		}
	}
	for name, body := range map[string]string{
		"file kind":        `{"logging":{"forwarders":[{"kind":"gelf","address":"a:1"}]}}`,
		"otlp no endpoint": `{"logging":{"forwarders":[{"kind":"otlp"}]}}`,
		"mixed options":    `{"logging":{"forwarders":[{"kind":"loki","url":"https://l.example.org","address":"a:1"}]}}`,
		"duplicate name":   `{"logging":{"forwarders":[{"kind":"loki","url":"https://l.example.org","name":"x"},{"kind":"syslog","address":"a:1","name":"x"}]}}`,
		"too many":         `{"logging":{"forwarders":[` + strings.Repeat(`{"kind":"syslog","address":"a:1"},`, 9) + `{"kind":"syslog","address":"a:1"}]}}`,
		"relative file":    `{"logging":{"forwarders":[{"kind":"loki","url":"https://l.example.org","bearerTokenFile":"rel"}]}}`,
		"inline secret":    `{"logging":{"forwarders":[{"kind":"loki","url":"http://l.example.org","username":"u"}]}}`,
	} {
		if _, err := forwardLoad(t, map[string]string{"JELEE_CONFIG": accountConfigFile(t, body)}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The configuration file has no inline secret fields; unknown keys are
	// refused rather than ignored.
	for _, key := range []string{`"password":"x"`, `"bearerToken":"y"`} {
		body := `{"logging":{"forwarders":[{"kind":"loki","url":"https://l.example.org",` + key + `}]}}`
		if _, err := forwardLoad(t, map[string]string{"JELEE_CONFIG": accountConfigFile(t, body)}); err == nil {
			t.Errorf("inline secret %s accepted", key)
		}
	}
}

func TestLogForwardersRefuseUnixSocketsOnWindowsOnly(t *testing.T) {
	_, err := forwardLoad(t, map[string]string{"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "/run/log.sock", "JELEE_LOG_FORWARD_SYSLOG_NETWORK": "unixgram"})
	if (err != nil) != (os.PathSeparator == '\\') {
		t.Fatalf("unix socket forwarder: err=%v on separator %q", err, os.PathSeparator)
	}
}

func TestLogForwardersOpenAndShipThroughTheRouter(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got <- r.URL.Path + " " + b.String()
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c, err := forwardLoad(t, map[string]string{"JELEE_LOG_FORWARD_LOKI_URL": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	r, err := logging.Open(c.Logging.Options(), &stdout)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Info("config probe", "component", "http")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if !strings.HasPrefix(msg, "/loki/api/v1/push ") || !strings.Contains(msg, "config probe") {
			t.Fatalf("collector got %q", msg)
		}
	default:
		t.Fatal("collector received nothing")
	}
}

func TestLogForwardersOTLPFromEnvironment(t *testing.T) {
	c, err := forwardLoad(t, map[string]string{
		"JELEE_LOG_FORWARD_OTLP_ENDPOINT":          "https://otel-collector.internal:4318",
		"JELEE_LOG_FORWARD_OTLP_HEADERS":           "Authorization=Bearer%20otlp-s3cret, X-Scope-OrgID=home",
		"JELEE_LOG_FORWARD_OTLP_NAME":              "collector",
		"JELEE_LOG_FORWARD_OTLP_BATCH_SIZE":        "200",
		"JELEE_LOG_FORWARD_OTLP_BATCH_INTERVAL_MS": "500",
		"JELEE_LOG_FORWARD_OTLP_TIMEOUT_MS":        "4000",
		"JELEE_LOG_FORWARD_OTLP_QUEUE_ENTRIES":     "2048",
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := c.Logging.Options().Forward
	if len(opts) != 1 {
		t.Fatalf("%d forwarders", len(opts))
	}
	o := opts[0]
	if o.Kind != logging.ForwardOTLP || o.Name != "collector" || o.OTLP.Endpoint != "https://otel-collector.internal:4318" || o.OTLP.Insecure ||
		o.OTLP.Headers["Authorization"] != "Bearer otlp-s3cret" || o.OTLP.Headers["X-Scope-OrgID"] != "home" ||
		o.BatchSize != 200 || o.BatchInterval != 500*time.Millisecond || o.Timeout != 4*time.Second || o.QueueEntries != 2048 {
		t.Fatalf("otlp options %+v", o)
	}
	data, err := json.Marshal(c.Logging)
	if err != nil || strings.Contains(string(data), "otlp-s3cret") {
		t.Fatalf("serialized logging config exposes the headers (%v): %s", err, data)
	}
	c, err = forwardLoad(t, map[string]string{"JELEE_LOG_FORWARD_OTLP_ENDPOINT": "http://127.0.0.1:4318", "JELEE_LOG_FORWARD_OTLP_INSECURE": "true"})
	if err != nil || !c.Logging.Forwarders[0].Insecure {
		t.Fatalf("loopback insecure: %v", err)
	}
}

func TestLogForwardersOTLPHeadersFile(t *testing.T) {
	headers := filepath.Join(t.TempDir(), "otlp-headers")
	if err := os.WriteFile(headers, []byte("Authorization=Bearer otlp-file-s3cret\r\nX-Scope-OrgID=home,X-Extra=a%2Cb\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := accountConfigFile(t, `{"logging":{"forwarders":[{"kind":"otlp","endpoint":"https://collector.internal","headersFile":`+strconv.Quote(headers)+`,"batchSize":10}]}}`)
	for name, values := range map[string]map[string]string{
		"file":        {"JELEE_CONFIG": file},
		"environment": {"JELEE_LOG_FORWARD_OTLP_ENDPOINT": "https://collector.internal", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": headers},
	} {
		c, err := forwardLoad(t, values)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h := c.Logging.Options().Forward[0].OTLP.Headers
		if len(h) != 3 || h["Authorization"] != "Bearer otlp-file-s3cret" || h["X-Scope-OrgID"] != "home" || h["X-Extra"] != "a,b" {
			t.Fatalf("%s: headers %v", name, h)
		}
	}
}

func TestLogForwardersOTLPRefusesHalfAndBadConfigurations(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("no separator here"), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := "JELEE_LOG_FORWARD_OTLP_ENDPOINT"
	for name, values := range map[string]map[string]string{
		"option without endpoint":   {"JELEE_LOG_FORWARD_OTLP_HEADERS": "Authorization=Bearer%20otlp-s3cret"},
		"insecure without endpoint": {"JELEE_LOG_FORWARD_OTLP_INSECURE": "true"},
		"two header sources":        {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS": "A=b", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": empty},
		"bad insecure":              {ep: "http://127.0.0.1:4318", "JELEE_LOG_FORWARD_OTLP_INSECURE": "maybe"},
		"bad headers":               {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS": "otlp-s3cret"},
		"reserved header":           {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS": "Content-Type=otlp-s3cret"},
		"plain remote":              {ep: "http://10.1.2.3:4318", "JELEE_LOG_FORWARD_OTLP_INSECURE": "true"},
		"plain without insecure":    {ep: "http://127.0.0.1:4318"},
		"https insecure":            {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_INSECURE": "true"},
		"empty headers file":        {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": empty},
		"garbage headers file":      {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": garbage},
		"missing headers file":      {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": filepath.Join(dir, "nope")},
		"relative headers file":     {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_HEADERS_FILE": "headers"},
		"ca with plain http":        {ep: "http://127.0.0.1:4318", "JELEE_LOG_FORWARD_OTLP_INSECURE": "true", "JELEE_LOG_FORWARD_OTLP_CA_FILE": empty},
		"bad batch":                 {ep: "https://c.example.org", "JELEE_LOG_FORWARD_OTLP_BATCH_SIZE": "0x10"},
	} {
		_, err := forwardLoad(t, values)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		for _, needle := range []string{"otlp-s3cret", "10.1.2.3", dir} {
			if strings.Contains(err.Error(), needle) {
				t.Errorf("%s: error repeats %q: %v", name, needle, err)
			}
		}
	}
	for name, body := range map[string]string{
		"otlp with loki url":    `{"logging":{"forwarders":[{"kind":"otlp","endpoint":"https://c.example.org","url":"https://l.example.org"}]}}`,
		"otlp with address":     `{"logging":{"forwarders":[{"kind":"otlp","endpoint":"https://c.example.org","address":"a:1"}]}}`,
		"loki with endpoint":    `{"logging":{"forwarders":[{"kind":"loki","url":"https://l.example.org","endpoint":"https://c.example.org"}]}}`,
		"syslog with insecure":  `{"logging":{"forwarders":[{"kind":"syslog","address":"a:1","insecure":true}]}}`,
		"inline headers":        `{"logging":{"forwarders":[{"kind":"otlp","endpoint":"https://c.example.org","headers":{"Authorization":"x"}}]}}`,
		"relative headers file": `{"logging":{"forwarders":[{"kind":"otlp","endpoint":"https://c.example.org","headersFile":"h"}]}}`,
	} {
		if _, err := forwardLoad(t, map[string]string{"JELEE_CONFIG": accountConfigFile(t, body)}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestLogForwardersOTLPOpenAndShipThroughTheRouter(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path + " " + r.Header.Get("Content-Type") + " " + r.Header.Get("X-Scope-OrgID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, err := forwardLoad(t, map[string]string{"JELEE_LOG_FORWARD_OTLP_ENDPOINT": srv.URL, "JELEE_LOG_FORWARD_OTLP_INSECURE": "1", "JELEE_LOG_FORWARD_OTLP_HEADERS": "X-Scope-OrgID=home"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := logging.Open(c.Logging.Options(), &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Info("config probe", "component", "http")
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-got:
		if msg != "/v1/logs application/x-protobuf home" {
			t.Fatalf("collector got %q", msg)
		}
	default:
		t.Fatal("collector received nothing")
	}
}
