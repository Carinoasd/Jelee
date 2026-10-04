package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// WebhookMasterKeyBytes is the AES-256 key size of the webhook master key.
const WebhookMasterKeyBytes = 32

// MasterKey holds the webhook master key text. It never prints, so a
// configuration dumped with %v or %#v cannot leak it.
type MasterKey string

func (MasterKey) String() string   { return "[redacted]" }
func (MasterKey) GoString() string { return "[redacted]" }

// WebhooksConfig controls outgoing webhooks (G12). Endpoint signing secrets
// and custom header values are stored sealed with the master key (AES-GCM);
// without a master key webhooks cannot be enabled.
type WebhooksConfig struct {
	// MasterKey is read from JELEE_WEBHOOK_MASTER_KEY or
	// JELEE_WEBHOOK_MASTER_KEY_FILE only: 32 bytes as standard or URL-safe
	// base64, or as 64 hex digits.
	MasterKey MasterKey `json:"-"`
	// AllowedHosts restricts endpoint hosts to these exact names (G12.5).
	// Empty allows any public host. Private, loopback and link-local
	// addresses are refused either way.
	AllowedHosts []string `json:"allowedHosts"`
	// CAFile is a PEM bundle of extra trusted roots for endpoints with a
	// self-signed certificate. Certificate verification itself can never be
	// turned off.
	CAFile string `json:"caFile"`
	// PollMilliseconds is the idle wait between delivery rounds.
	PollMilliseconds int `json:"pollMilliseconds"`
	// Batch bounds the events fanned out and deliveries claimed per round.
	Batch int `json:"batch"`
	// Concurrency bounds deliveries in flight on this instance.
	Concurrency int `json:"concurrency"`
	// LeaseSeconds is how long a claimed delivery is reserved; an instance
	// that dies mid-delivery is replaced after it expires (at least once).
	LeaseSeconds int `json:"leaseSeconds"`
	// RetentionDays deletes settled events with their delivery log after
	// this many days.
	RetentionDays int `json:"retentionDays"`
}

func DefaultWebhooksConfig() WebhooksConfig {
	return WebhooksConfig{PollMilliseconds: 1000, Batch: 50, Concurrency: 4, LeaseSeconds: 120, RetentionDays: 14}
}

func (c WebhooksConfig) effective() WebhooksConfig {
	d := DefaultWebhooksConfig()
	if c.PollMilliseconds == 0 && c.Batch == 0 && c.Concurrency == 0 && c.LeaseSeconds == 0 && c.RetentionDays == 0 {
		d.MasterKey, d.AllowedHosts, d.CAFile = c.MasterKey, c.AllowedHosts, c.CAFile
		return d
	}
	return c
}

// Key decodes the master key. The error never contains the key.
func (c WebhooksConfig) Key() ([]byte, error) {
	text := strings.TrimSpace(string(c.MasterKey))
	if text == "" {
		return nil, errors.New("webhooks require JELEE_WEBHOOK_MASTER_KEY or JELEE_WEBHOOK_MASTER_KEY_FILE")
	}
	for _, decode := range []func(string) ([]byte, error){
		hex.DecodeString, base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString, base64.RawURLEncoding.DecodeString,
	} {
		if key, err := decode(text); err == nil && len(key) == WebhookMasterKeyBytes {
			return key, nil
		}
	}
	return nil, errors.New("webhook master key must be 32 bytes in base64 or 64 hex digits")
}

func (c WebhooksConfig) Validate() error {
	if _, err := c.Key(); err != nil {
		return err
	}
	c = c.effective()
	switch {
	case c.PollMilliseconds < 100 || c.PollMilliseconds > 60000:
		return errors.New("webhook pollMilliseconds must be between 100 and 60000")
	case c.Batch < 1 || c.Batch > 500:
		return errors.New("webhook batch must be between 1 and 500")
	case c.Concurrency < 1 || c.Concurrency > 32:
		return errors.New("webhook concurrency must be between 1 and 32")
	case c.LeaseSeconds < 60 || c.LeaseSeconds > 3600:
		// A lease must outlive the longest attempt (30 s) and its recording.
		return errors.New("webhook leaseSeconds must be between 60 and 3600")
	case c.RetentionDays < 1 || c.RetentionDays > 365:
		return errors.New("webhook retentionDays must be between 1 and 365")
	case len(c.AllowedHosts) > 64:
		return errors.New("webhook allowedHosts holds at most 64 hosts")
	}
	for _, h := range c.AllowedHosts {
		if h == "" || len(h) > 253 || strings.ContainsAny(h, " /\\@:%[]\r\n\t") || h != strings.ToLower(h) {
			return errors.New("invalid webhook allowedHosts entry")
		}
	}
	if c.CAFile != "" && strings.ContainsRune(c.CAFile, 0) {
		return errors.New("invalid webhook caFile")
	}
	return nil
}

func (c WebhooksConfig) PollInterval() time.Duration {
	return time.Duration(c.effective().PollMilliseconds) * time.Millisecond
}

func (c WebhooksConfig) Lease() time.Duration {
	return time.Duration(c.effective().LeaseSeconds) * time.Second
}

func (c WebhooksConfig) Retention() time.Duration {
	return time.Duration(c.effective().RetentionDays) * 24 * time.Hour
}

func (c WebhooksConfig) BatchSize() int { return c.effective().Batch }

func (c WebhooksConfig) Workers() int { return c.effective().Concurrency }

// RootCAs reads the configured extra roots; nil when none are configured.
func (c WebhooksConfig) RootCAs() ([]byte, error) {
	if c.CAFile == "" {
		return nil, nil
	}
	f, err := os.Open(c.CAFile)
	if err != nil {
		return nil, errors.New("cannot read webhook caFile")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("webhook caFile exceeds 1 MiB or cannot be read")
	}
	return data, nil
}

func (c *WebhooksConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	key, present := lookup("JELEE_WEBHOOK_MASTER_KEY")
	path, filePresent := lookup("JELEE_WEBHOOK_MASTER_KEY_FILE")
	if present && filePresent {
		return errors.New("set only one webhook master key source")
	}
	if filePresent {
		f, err := os.Open(path)
		if err != nil {
			return errors.New("cannot read webhook master key file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1025))
		if err != nil || len(data) > 1024 {
			return errors.New("invalid webhook master key file")
		}
		key = string(data)
	}
	if present || filePresent {
		c.MasterKey = MasterKey(strings.TrimSpace(key))
	}
	if value, ok := lookup("JELEE_WEBHOOK_ALLOWED_HOSTS"); ok {
		c.AllowedHosts = nil
		for _, h := range strings.Split(value, ",") {
			if h = strings.TrimSpace(h); h != "" {
				c.AllowedHosts = append(c.AllowedHosts, h)
			}
		}
	}
	if value, ok := lookup("JELEE_WEBHOOK_CA_FILE"); ok {
		c.CAFile = value
	}
	for name, target := range map[string]*int{
		"JELEE_WEBHOOK_POLL_MILLISECONDS": &c.PollMilliseconds,
		"JELEE_WEBHOOK_BATCH":             &c.Batch,
		"JELEE_WEBHOOK_CONCURRENCY":       &c.Concurrency,
		"JELEE_WEBHOOK_LEASE_SECONDS":     &c.LeaseSeconds,
		"JELEE_WEBHOOK_RETENTION_DAYS":    &c.RetentionDays,
	} {
		if value, ok := lookup(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	return nil
}
