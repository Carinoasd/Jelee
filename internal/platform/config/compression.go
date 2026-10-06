package config

import (
	"errors"
	"fmt"
	"strconv"
)

// CompressionConfig controls gzip compression of non-media responses
// (G11.7). The zero value selects the defaults, so compression is on unless
// Mode is "off". Media, Range and image responses are never compressed,
// whatever these settings say.
type CompressionConfig struct {
	// Mode is "gzip" (the default when empty) or "off".
	Mode string `json:"mode"`
	// Level is the gzip level 1 (fastest) to 9 (smallest); 0 selects 5.
	Level int `json:"level"`
	// MinBytes is the smallest body that is compressed; 0 selects 1024.
	MinBytes int `json:"minBytes"`
}

const (
	defaultCompressionLevel    = 5
	defaultCompressionMinBytes = 1024
	maxCompressionMinBytes     = 1 << 20
)

// Validate never includes configured values in its errors.
func (c CompressionConfig) Validate() error {
	switch {
	case c.Mode != "" && c.Mode != "gzip" && c.Mode != "off":
		return errors.New("compression mode must be gzip or off")
	case c.Level < 0 || c.Level > 9:
		return errors.New("compression level must be between 1 and 9")
	case c.MinBytes < 0 || c.MinBytes > maxCompressionMinBytes:
		return errors.New("compression minBytes must be between 1 and 1048576")
	}
	return nil
}

// Enabled reports whether eligible responses are compressed.
func (c CompressionConfig) Enabled() bool { return c.Mode != "off" }

// GzipLevel is the effective gzip level.
func (c CompressionConfig) GzipLevel() int {
	if c.Level == 0 {
		return defaultCompressionLevel
	}
	return c.Level
}

// Threshold is the effective minimum body size in bytes.
func (c CompressionConfig) Threshold() int {
	if c.MinBytes == 0 {
		return defaultCompressionMinBytes
	}
	return c.MinBytes
}

func (c *CompressionConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_HTTP_COMPRESSION"); ok {
		c.Mode = value
	}
	for key, target := range map[string]*int{
		"JELEE_HTTP_COMPRESSION_LEVEL":     &c.Level,
		"JELEE_HTTP_COMPRESSION_MIN_BYTES": &c.MinBytes,
	} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	return nil
}
