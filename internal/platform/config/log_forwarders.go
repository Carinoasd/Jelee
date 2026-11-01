package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// MaxLogForwarders bounds the external log destinations.
const MaxLogForwarders = 8

// LogForwarderConfig is one external log destination (G46.1): syslog or
// Loki. Credentials never come from the configuration file itself; the file
// names a secret file, and the environment can give the value or a file.
type LogForwarderConfig struct {
	// Kind is syslog or loki.
	Kind string `json:"kind"`
	// Name is the destination label of the forwarder metrics (default: the
	// kind).
	Name            string `json:"name"`
	QueueEntries    int    `json:"queueEntries"`
	BatchSize       int    `json:"batchSize"`
	BatchIntervalMs int    `json:"batchIntervalMs"`
	TimeoutMs       int    `json:"timeoutMs"`

	// Syslog: network is udp (default), tcp, tls, unix or unixgram.
	Network  string `json:"network"`
	Address  string `json:"address"`
	Facility string `json:"facility"`
	AppName  string `json:"appName"`
	Hostname string `json:"hostname"`
	Framing  string `json:"framing"`

	// Loki.
	URL             string `json:"url"`
	Username        string `json:"username"`
	PasswordFile    string `json:"passwordFile"`
	BearerTokenFile string `json:"bearerTokenFile"`
	TenantID        string `json:"tenantId"`
	Service         string `json:"service"`

	Password    string `json:"-"`
	BearerToken string `json:"-"`
}

// options converts the entry; the error names no configured value.
func (f LogForwarderConfig) options() logging.ForwarderOptions {
	return logging.ForwarderOptions{
		Kind: logging.ForwarderKind(f.Kind), Name: f.Name, QueueEntries: f.QueueEntries, BatchSize: f.BatchSize,
		BatchInterval: time.Duration(f.BatchIntervalMs) * time.Millisecond, Timeout: time.Duration(f.TimeoutMs) * time.Millisecond,
		Syslog: logging.SyslogOptions{Network: f.Network, Address: f.Address, Facility: f.Facility, AppName: f.AppName, Hostname: f.Hostname, Framing: f.Framing},
		Loki:   logging.LokiOptions{URL: f.URL, Username: f.Username, Password: f.Password, BearerToken: f.BearerToken, TenantID: f.TenantID, Service: f.Service},
	}
}

func (c LoggingConfig) validateForwarders() error {
	if len(c.Forwarders) > MaxLogForwarders {
		return errors.New("too many log forwarders")
	}
	names := map[string]bool{}
	for _, f := range c.Forwarders {
		if f.Kind != string(logging.ForwardSyslog) && f.Kind != string(logging.ForwardLoki) {
			return errors.New("log forwarder kind must be syslog or loki")
		}
		if (f.Kind == "syslog" && (f.URL != "" || f.Username != "" || f.PasswordFile != "" || f.BearerTokenFile != "" || f.TenantID != "" || f.Service != "")) ||
			(f.Kind == "loki" && (f.Network != "" || f.Address != "" || f.Facility != "" || f.AppName != "" || f.Hostname != "" || f.Framing != "")) {
			return errors.New("log forwarder has options of another kind")
		}
		if f.Name != "" {
			if names[f.Name] {
				return errors.New("duplicate log forwarder name")
			}
			names[f.Name] = true
		}
		if f.BatchIntervalMs < 0 || f.TimeoutMs < 0 {
			return errors.New("log forwarder batch interval and timeout must not be negative")
		}
		for _, p := range []string{f.PasswordFile, f.BearerTokenFile} {
			if p != "" && (!filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n")) {
				return errors.New("log forwarder secret file must be an absolute path")
			}
		}
		if f.Kind == "loki" && f.Username != "" && f.Password == "" && f.PasswordFile == "" {
			return errors.New("log forwarder Loki basic authentication needs both username and password")
		}
		if f.Kind == "loki" && f.BearerTokenFile != "" && f.BearerToken == "" {
			return errors.New("log forwarder secret is missing")
		}
		if err := f.options().Validate(); err != nil {
			return err
		}
	}
	return nil
}

// forwarderOptions converts the configured forwarders.
func (c LoggingConfig) forwarderOptions() []logging.ForwarderOptions {
	if len(c.Forwarders) == 0 {
		return nil
	}
	out := make([]logging.ForwarderOptions, len(c.Forwarders))
	for i, f := range c.Forwarders {
		out[i] = f.options()
	}
	return out
}

// Environment names per kind; the first one switches the forwarder on.
var (
	syslogForwardEnv = []string{"ADDRESS", "NAME", "NETWORK", "FACILITY", "APP_NAME", "HOSTNAME", "FRAMING", "QUEUE_ENTRIES", "BATCH_SIZE", "BATCH_INTERVAL_MS", "TIMEOUT_MS"}
	lokiForwardEnv   = []string{"URL", "NAME", "USERNAME", "PASSWORD", "PASSWORD_FILE", "BEARER_TOKEN", "BEARER_TOKEN_FILE", "TENANT_ID", "SERVICE", "QUEUE_ENTRIES", "BATCH_SIZE", "BATCH_INTERVAL_MS", "TIMEOUT_MS"}
)

// loadForwarderEnvironment applies JELEE_LOG_FORWARD_SYSLOG_* and
// JELEE_LOG_FORWARD_LOKI_*. A kind given by the environment replaces the file
// entries of that kind; a variable without the one that switches the kind on
// (ADDRESS or URL) is a half configuration and refuses to start.
func (c *LoggingConfig) loadForwarderEnvironment(lookup func(string) (string, bool)) error {
	for _, spec := range []struct {
		kind   string
		names  []string
		enable string
	}{{"syslog", syslogForwardEnv, "ADDRESS"}, {"loki", lokiForwardEnv, "URL"}} {
		prefix := "JELEE_LOG_FORWARD_" + strings.ToUpper(spec.kind) + "_"
		values := map[string]string{}
		for _, name := range spec.names {
			if v, ok := lookup(prefix + name); ok {
				values[name] = v
			}
		}
		if len(values) == 0 {
			continue
		}
		if values[spec.enable] == "" {
			return fmt.Errorf("%s%s is required when other %s options are set", prefix, spec.enable, prefix)
		}
		f := LogForwarderConfig{Kind: spec.kind, Name: values["NAME"], Network: values["NETWORK"], Address: values["ADDRESS"], Facility: values["FACILITY"],
			AppName: values["APP_NAME"], Hostname: values["HOSTNAME"], Framing: values["FRAMING"], URL: values["URL"], Username: values["USERNAME"],
			Password: values["PASSWORD"], BearerToken: values["BEARER_TOKEN"], PasswordFile: values["PASSWORD_FILE"], BearerTokenFile: values["BEARER_TOKEN_FILE"],
			TenantID: values["TENANT_ID"], Service: values["SERVICE"]}
		for name, target := range map[string]*int{"QUEUE_ENTRIES": &f.QueueEntries, "BATCH_SIZE": &f.BatchSize, "BATCH_INTERVAL_MS": &f.BatchIntervalMs, "TIMEOUT_MS": &f.TimeoutMs} {
			if v, ok := values[name]; ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					return fmt.Errorf("invalid %s%s", prefix, name)
				}
				*target = n
			}
		}
		if (f.Password != "" && f.PasswordFile != "") || (f.BearerToken != "" && f.BearerTokenFile != "") {
			return errors.New("set only one log forwarder credential source")
		}
		kept := c.Forwarders[:0:0]
		for _, existing := range c.Forwarders {
			if existing.Kind != spec.kind {
				kept = append(kept, existing)
			}
		}
		c.Forwarders = append(kept, f)
	}
	return c.loadForwarderSecrets()
}

// loadForwarderSecrets reads the credential files. Errors never name the
// file contents.
func (c *LoggingConfig) loadForwarderSecrets() error {
	for i := range c.Forwarders {
		f := &c.Forwarders[i]
		for _, s := range []struct {
			path   string
			target *string
		}{{f.PasswordFile, &f.Password}, {f.BearerTokenFile, &f.BearerToken}} {
			if s.path == "" {
				continue
			}
			if !filepath.IsAbs(s.path) {
				return errors.New("log forwarder secret file must be an absolute path")
			}
			file, err := os.Open(s.path) //nolint:gosec // G304: the operator names the secret file
			if err != nil {
				return errors.New("cannot read log forwarder secret file")
			}
			data, err := io.ReadAll(io.LimitReader(file, 4097))
			_ = file.Close()
			if err != nil || len(data) > 4096 {
				return errors.New("invalid log forwarder secret file")
			}
			*s.target = strings.TrimSpace(string(data))
			if *s.target == "" {
				return errors.New("log forwarder secret file is empty")
			}
		}
	}
	return nil
}
