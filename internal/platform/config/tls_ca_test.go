package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
)

// caFile of the log forwarders and the trace exporter: read from the file
// and the environment, refused at startup when unreadable, not PEM or set
// where no certificate is checked.
func TestCAFileSettings(t *testing.T) {
	caFile := tlstrusttest.NewCA(t, "ca").WriteFile(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := forwardLoad(t, map[string]string{
		"JELEE_LOG_FORWARD_LOKI_URL":       "https://loki.example.org",
		"JELEE_LOG_FORWARD_LOKI_CA_FILE":   caFile,
		"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "log.example.org:6514",
		"JELEE_LOG_FORWARD_SYSLOG_NETWORK": "tls",
		"JELEE_LOG_FORWARD_SYSLOG_CA_FILE": caFile,
		"JELEE_TRACE_EXPORTER":             "otlp",
		"JELEE_TRACE_OTLP_ENDPOINT":        "https://collector.internal:4318",
		"JELEE_TRACE_OTLP_CA_FILE":         caFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range c.Logging.Options().Forward {
		if o.Kind == logging.ForwardLoki && (o.Loki.CAFile != caFile || o.Syslog.CAFile != "") ||
			o.Kind == logging.ForwardSyslog && (o.Syslog.CAFile != caFile || o.Loki.CAFile != "") {
			t.Fatalf("forwarder options %+v", o)
		}
	}
	if c.Tracing.OTLP.Settings().CAFile != caFile {
		t.Fatalf("tracing settings %+v", c.Tracing.OTLP.Settings())
	}

	file := accountConfigFile(t, `{"logging":{"forwarders":[{"kind":"loki","url":"https://loki.example.org","caFile":`+quote(caFile)+`}]},
		"tracing":{"exporter":"otlp","otlp":{"endpoint":"https://collector.internal","caFile":`+quote(caFile)+`}}}`)
	c, err = forwardLoad(t, map[string]string{"JELEE_CONFIG": file})
	if err != nil || c.Logging.Forwarders[0].CAFile != caFile || c.Tracing.OTLP.CAFile != caFile {
		t.Fatalf("file caFile %+v %+v %v", c.Logging.Forwarders, c.Tracing.OTLP, err)
	}

	for name, env := range map[string]map[string]string{
		"loki missing file":   {"JELEE_LOG_FORWARD_LOKI_URL": "https://loki.example.org", "JELEE_LOG_FORWARD_LOKI_CA_FILE": missing},
		"loki not pem":        {"JELEE_LOG_FORWARD_LOKI_URL": "https://loki.example.org", "JELEE_LOG_FORWARD_LOKI_CA_FILE": notPEM},
		"loki plain http":     {"JELEE_LOG_FORWARD_LOKI_URL": "http://127.0.0.1:3100", "JELEE_LOG_FORWARD_LOKI_CA_FILE": caFile},
		"syslog without tls":  {"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "log.example.org:514", "JELEE_LOG_FORWARD_SYSLOG_CA_FILE": caFile},
		"syslog missing file": {"JELEE_LOG_FORWARD_SYSLOG_ADDRESS": "log.example.org:6514", "JELEE_LOG_FORWARD_SYSLOG_NETWORK": "tls", "JELEE_LOG_FORWARD_SYSLOG_CA_FILE": missing},
		"ca file alone":       {"JELEE_LOG_FORWARD_LOKI_CA_FILE": caFile},
		"trace missing file":  {"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "https://collector.internal", "JELEE_TRACE_OTLP_CA_FILE": missing},
		"trace not pem":       {"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "https://collector.internal", "JELEE_TRACE_OTLP_CA_FILE": notPEM},
		"trace insecure http": {"JELEE_TRACE_EXPORTER": "otlp", "JELEE_TRACE_OTLP_ENDPOINT": "http://127.0.0.1:4318", "JELEE_TRACE_OTLP_INSECURE": "true", "JELEE_TRACE_OTLP_CA_FILE": caFile},
		"trace not enabled":   {"JELEE_TRACE_OTLP_CA_FILE": caFile},
	} {
		_, err := forwardLoad(t, env)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		if strings.Contains(err.Error(), filepath.Dir(missing)) || strings.Contains(err.Error(), filepath.Dir(caFile)) {
			t.Errorf("%s: error names the path: %v", name, err)
		}
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
