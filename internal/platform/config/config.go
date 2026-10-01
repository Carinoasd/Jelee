package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen                string         `json:"listen"`
	AllowedHosts          []string       `json:"allowedHosts"`
	TrustedProxies        []string       `json:"trustedProxies"`
	DatabaseURL           string         `json:"-"`
	TMDBAPIKey            string         `json:"-"`
	MaxConnections        int32          `json:"maxConnections"`
	MaxStreams            int            `json:"maxStreams"`
	RequestTimeoutSeconds int            `json:"requestTimeoutSeconds"`
	EnableCatalog         bool           `json:"enableCatalog"`
	EnableDirect          bool           `json:"enableDirect"`
	EnableAccounts        bool           `json:"enableAccounts"`
	Accounts              AccountsConfig `json:"accounts"`
	EnableJobs            bool           `json:"enableJobs"`
	Jobs                  JobsConfig     `json:"jobs"`
	EnableProbe           bool           `json:"enableProbe"`
	EnableFamilyIgnore    bool           `json:"enableFamilyIgnore"`
}

func Load() (Config, error) { return LoadWith(os.LookupEnv) }

// LoadWith keeps environment lookup injectable and never includes values in errors.
func LoadWith(lookup func(string) (string, bool)) (Config, error) {
	c := Config{Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost", "127.0.0.1", "::1"}, MaxConnections: 8, MaxStreams: 8, RequestTimeoutSeconds: 15, Accounts: DefaultAccountsConfig(), Jobs: DefaultJobsConfig()}
	if path, ok := lookup("JELEE_CONFIG"); ok && path != "" {
		f, err := os.Open(path)
		if err != nil {
			return c, errors.New("cannot read JELEE_CONFIG")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 65537))
		if err != nil || len(data) > 65536 {
			return c, errors.New("configuration file exceeds the size limit or cannot be read")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&c); err != nil {
			return c, errors.New("invalid configuration file")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return c, errors.New("configuration must contain one JSON object")
		}
	}
	if value, ok := lookup("JELEE_DATABASE_URL"); ok {
		c.DatabaseURL = value
	}
	var tmdbErr error
	c.TMDBAPIKey, tmdbErr = loadTMDBKey(lookup)
	if tmdbErr != nil {
		return c, tmdbErr
	}
	if value, ok := lookup("JELEE_DATABASE_URL_FILE"); ok && value != "" {
		if c.DatabaseURL != "" {
			return c, errors.New("set only one database credential source")
		}
		f, err := os.Open(value)
		if err != nil {
			return c, errors.New("cannot read database credential file")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16385))
		if err != nil || len(data) > 16384 {
			return c, errors.New("invalid database credential file")
		}
		c.DatabaseURL = strings.TrimSpace(string(data))
	}
	if value, ok := lookup("JELEE_LISTEN"); ok {
		c.Listen = value
	}
	if value, ok := lookup("JELEE_ALLOWED_HOSTS"); ok {
		c.AllowedHosts = strings.Split(value, ",")
	}
	if value, ok := lookup("JELEE_TRUSTED_PROXIES"); ok {
		c.TrustedProxies = nil
		if strings.TrimSpace(value) != "" {
			c.TrustedProxies = strings.Split(value, ",")
		}
	}
	for name, target := range map[string]*bool{"JELEE_ENABLE_CATALOG": &c.EnableCatalog, "JELEE_ENABLE_DIRECT": &c.EnableDirect, "JELEE_ENABLE_ACCOUNTS": &c.EnableAccounts, "JELEE_ENABLE_JOBS": &c.EnableJobs, "JELEE_ENABLE_PROBE": &c.EnableProbe, "JELEE_ENABLE_FAMILY_IGNORE": &c.EnableFamilyIgnore} {
		if value, ok := lookup(name); ok {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return c, fmt.Errorf("invalid %s", name)
			}
			*target = b
		}
	}
	if value, ok := lookup("JELEE_DEV_MODE"); ok && value != "" && value != "false" {
		return c, errors.New("developer mode is unavailable in this production build")
	}
	if value, ok := lookup("JELEE_MAX_CONNECTIONS"); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 128 {
			return c, errors.New("invalid JELEE_MAX_CONNECTIONS")
		}
		c.MaxConnections = int32(n)
	}
	if value, ok := lookup("JELEE_MAX_STREAMS"); ok {
		n, err := strconv.Atoi(value)
		if err != nil {
			return c, errors.New("invalid JELEE_MAX_STREAMS")
		}
		c.MaxStreams = n
	}
	if err := c.Accounts.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Jobs.loadEnvironment(lookup); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return errors.New("JELEE_DATABASE_URL must identify a PostgreSQL database")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil {
		return errors.New("listen must be an explicit IP address and port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid listen port")
	}
	if _, err := c.TrustedProxyPrefixes(); err != nil {
		return err
	}
	if c.TMDBAPIKey != "" && !validTMDBKey(c.TMDBAPIKey) {
		return errors.New("invalid TMDB_API_KEY")
	}
	if len(c.AllowedHosts) == 0 {
		return errors.New("allowedHosts cannot be empty")
	}
	for _, h := range c.AllowedHosts {
		if h == "" || strings.ContainsAny(h, " /\\@\r\n\t") || h != strings.ToLower(h) {
			return errors.New("invalid allowedHosts entry")
		}
	}
	if c.MaxConnections < 1 || c.MaxConnections > 128 || c.MaxStreams < 1 || c.MaxStreams > 128 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 120 {
		return errors.New("concurrency or timeout is outside the supported range")
	}
	if c.EnableDirect && !c.EnableCatalog {
		return errors.New("direct delivery requires catalog rollout")
	}
	if c.EnableAccounts {
		if err := c.Accounts.Validate(); err != nil {
			return err
		}
	}
	if c.EnableJobs {
		if !c.EnableAccounts {
			return errors.New("jobs require account rollout")
		}
		if err := c.Jobs.Validate(); err != nil {
			return err
		}
		if int(c.MaxConnections) < c.Jobs.Workers+2 {
			return errors.New("jobs require at least workers plus two database connections")
		}
	}
	if c.EnableFamilyIgnore && !c.EnableJobs {
		return errors.New("family ignore requires job rollout")
	}
	if c.EnableProbe {
		if !c.EnableJobs {
			return errors.New("probe requires job rollout")
		}
		if c.Jobs.DatabaseTimeoutSeconds >= 10 {
			return errors.New("probe database timeout must be shorter than its heartbeat interval")
		}
	}
	return nil
}

func (c Config) RequestTimeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}
