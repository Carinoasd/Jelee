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
	Listen                string   `json:"listen"`
	AllowedHosts          []string `json:"allowedHosts"`
	DatabaseURL           string   `json:"-"`
	MaxConnections        int32    `json:"maxConnections"`
	MaxStreams            int      `json:"maxStreams"`
	RequestTimeoutSeconds int      `json:"requestTimeoutSeconds"`
	EnableCatalog         bool     `json:"enableCatalog"`
	EnableDirect          bool     `json:"enableDirect"`
}

func Load() (Config, error) { return LoadWith(os.LookupEnv) }

// LoadWith keeps environment lookup injectable and never includes values in errors.
func LoadWith(lookup func(string) (string, bool)) (Config, error) {
	c := Config{Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost", "127.0.0.1", "::1"}, MaxConnections: 8, MaxStreams: 8, RequestTimeoutSeconds: 15}
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
	for name, target := range map[string]*bool{"JELEE_ENABLE_CATALOG": &c.EnableCatalog, "JELEE_ENABLE_DIRECT": &c.EnableDirect} {
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
	return nil
}

func (c Config) RequestTimeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}
