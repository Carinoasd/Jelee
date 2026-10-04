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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Resources             ResourcesConfig `json:"resources"`
	Access                AccessConfig    `json:"access"`
	Streaming             StreamingConfig `json:"streaming"`
	Playback              PlaybackConfig  `json:"playback"`
	Listen                string          `json:"listen"`
	AllowedHosts          []string        `json:"allowedHosts"`
	TrustedProxies        []string        `json:"trustedProxies"`
	DatabaseURL           string          `json:"-"`
	TMDBAPIKey            string          `json:"-"`
	MaxConnections        int32           `json:"maxConnections"`
	MaxStreams            int             `json:"maxStreams"`
	RequestTimeoutSeconds int             `json:"requestTimeoutSeconds"`
	EnableCatalog         bool            `json:"enableCatalog"`
	EnableDirect          bool            `json:"enableDirect"`
	EnableAccounts        bool            `json:"enableAccounts"`
	EnableMetrics         bool            `json:"enableMetrics"`
	EnableImages          bool            `json:"enableImages"`
	Images                ImagesConfig    `json:"images"`
	Accounts              AccountsConfig  `json:"accounts"`
	EnableJobs            bool            `json:"enableJobs"`
	Jobs                  JobsConfig      `json:"jobs"`
	EnableProbe           bool            `json:"enableProbe"`
	EnableFamilyIgnore    bool            `json:"enableFamilyIgnore"`
	Logging               LoggingConfig   `json:"logging"`
	// EnableNFOWrite lets job workers claim nfo_write jobs and run NFO commit
	// recovery. It is off by default and requires job rollout.
	EnableNFOWrite bool `json:"enableNFOWrite"`
	// WebDir is the built single-page frontend (for example web/dist). Empty
	// disables the frontend; the API is unaffected either way.
	WebDir string `json:"webDir"`
	// EnableCompat mounts the third-party client compatibility layer under
	// /compat. It is off by default.
	EnableCompat bool `json:"enableCompat"`
	// CompatServerID pins the server identifier reported by the compatibility
	// layer (32 lowercase hex digits). Empty derives a stable value from
	// allowedHosts.
	CompatServerID string `json:"compatServerId"`
}

func Load() (Config, error) { return LoadWith(os.LookupEnv) }

// LoadWith keeps environment lookup injectable and never includes values in errors.
func LoadWith(lookup func(string) (string, bool)) (Config, error) {
	c := Config{Resources: DefaultResourcesConfig(), Access: DefaultAccessConfig(), Streaming: DefaultStreamingConfig(), Playback: DefaultPlaybackConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost", "127.0.0.1", "::1"}, MaxConnections: 8, MaxStreams: 8, RequestTimeoutSeconds: 15, Accounts: DefaultAccountsConfig(), Jobs: DefaultJobsConfig(), Images: DefaultImagesConfig(), Logging: DefaultLoggingConfig()}
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
	if value, ok := lookup("JELEE_COMPAT_SERVER_ID"); ok {
		c.CompatServerID = value
	}
	if value, ok := lookup("JELEE_WEB_DIR"); ok {
		c.WebDir = value
	}
	if value, ok := lookup("JELEE_TRUSTED_PROXIES"); ok {
		c.TrustedProxies = nil
		if strings.TrimSpace(value) != "" {
			c.TrustedProxies = strings.Split(value, ",")
		}
	}
	for name, target := range map[string]*bool{"JELEE_ENABLE_CATALOG": &c.EnableCatalog, "JELEE_ENABLE_DIRECT": &c.EnableDirect, "JELEE_ENABLE_ACCOUNTS": &c.EnableAccounts, "JELEE_ENABLE_METRICS": &c.EnableMetrics, "JELEE_ENABLE_IMAGES": &c.EnableImages, "JELEE_ENABLE_JOBS": &c.EnableJobs, "JELEE_ENABLE_PROBE": &c.EnableProbe, "JELEE_ENABLE_FAMILY_IGNORE": &c.EnableFamilyIgnore, "JELEE_ENABLE_NFO_WRITE": &c.EnableNFOWrite, "JELEE_COMPAT_ENABLED": &c.EnableCompat} {
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
	if err := c.Images.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Resources.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Access.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Streaming.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Playback.loadEnvironment(lookup); err != nil {
		return c, err
	}
	if err := c.Logging.loadEnvironment(lookup); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if err := c.Resources.Validate(); err != nil {
		return err
	}
	if err := c.Access.Validate(); err != nil {
		return err
	}
	if err := c.Streaming.Validate(); err != nil {
		return err
	}
	if err := c.Playback.Validate(); err != nil {
		return err
	}
	if err := c.Logging.Validate(); err != nil {
		return err
	}
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
	if c.WebDir != "" && (!filepath.IsAbs(c.WebDir) || filepath.Clean(c.WebDir) != c.WebDir || strings.ContainsRune(c.WebDir, 0)) {
		return errors.New("webDir must be a clean absolute path")
	}
	if c.MaxConnections < 1 || c.MaxConnections > 128 || c.MaxStreams < 1 || c.MaxStreams > 128 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 120 {
		return errors.New("concurrency or timeout is outside the supported range")
	}
	if c.CompatServerID != "" && !validCompatServerID(c.CompatServerID) {
		return errors.New("compatServerId must be 32 lowercase hex digits and not all zero")
	}
	if c.EnableDirect && !c.EnableCatalog {
		return errors.New("direct delivery requires catalog rollout")
	}
	if c.EnableAccounts {
		if err := c.Accounts.Validate(); err != nil {
			return err
		}
	}
	if c.EnableMetrics && !c.EnableAccounts {
		return errors.New("metrics require account rollout")
	}
	if c.EnableImages {
		if !c.EnableAccounts || !c.EnableCatalog {
			return errors.New("images require account and catalog rollout")
		}
		if err := c.Images.Validate(); err != nil {
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
	if c.EnableNFOWrite {
		if !c.EnableJobs {
			return errors.New("nfo write requires job rollout")
		}
		// The recovery loop and its lease renewal need a connection of their own.
		if int(c.MaxConnections) < c.Jobs.Workers+3 {
			return errors.New("nfo write requires at least workers plus three database connections")
		}
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

func validCompatServerID(id string) bool {
	if len(id) != 32 || strings.Trim(id, "0") == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
