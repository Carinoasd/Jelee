package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ImagesConfig struct {
	TempRoot           string `json:"tempRoot"`
	MaxConcurrent      int    `json:"maxConcurrent"`
	MaxImageBytes      int64  `json:"maxImageBytes"`
	MaxSourceBytes     int64  `json:"maxSourceBytes"`
	MaxOutputBytes     int64  `json:"maxOutputBytes"`
	MaxOutputDimension int    `json:"maxOutputDimension"`
	CacheBytes         int64  `json:"cacheBytes"`
	CacheEntries       int    `json:"cacheEntries"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	CacheTTLSeconds    int    `json:"cacheTTLSeconds"`
	DefaultQuality     int    `json:"defaultQuality"`
}

// TempRoot must be explicitly set to an existing private directory when
// images are enabled. The adapter also refuses overlap with media roots.
func DefaultImagesConfig() ImagesConfig {
	return ImagesConfig{MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20, MaxOutputBytes: 2 << 20, MaxOutputDimension: 1024, CacheBytes: 32 << 20, CacheEntries: 128, TimeoutSeconds: 15, CacheTTLSeconds: 300, DefaultQuality: 85}
}

func (c ImagesConfig) Validate() error {
	if len(c.TempRoot) > 4096 || !utf8.ValidString(c.TempRoot) || strings.ContainsFunc(c.TempRoot, unicode.IsControl) || !filepath.IsAbs(c.TempRoot) {
		return errors.New("images tempRoot must be an absolute private directory")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 8 || c.MaxImageBytes < 16<<20 || c.MaxImageBytes > 256<<20 || c.MaxSourceBytes < 1 || c.MaxSourceBytes > 64<<20 || c.MaxOutputBytes < 64<<10 || c.MaxOutputBytes > 8<<20 || c.MaxOutputDimension < 16 || c.MaxOutputDimension > 2048 {
		return errors.New("image processing limits are outside supported bounds")
	}
	if c.CacheBytes < c.MaxOutputBytes || c.CacheBytes > 256<<20 || c.CacheEntries < 1 || c.CacheEntries > 4096 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 || c.CacheTTLSeconds < 1 || c.CacheTTLSeconds > 86400 || c.DefaultQuality < 1 || c.DefaultQuality > 100 {
		return errors.New("image cache or timing limits are outside supported bounds")
	}
	// Per-field bounds above make this arithmetic safe before conversion.
	if int64(c.MaxConcurrent)*c.MaxImageBytes+c.CacheBytes > 1<<30 || c.MaxOutputBytes >= c.MaxImageBytes {
		return errors.New("image aggregate memory budget exceeds supported limits")
	}
	return nil
}

func (c *ImagesConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_IMAGE_TEMP_ROOT"); ok {
		c.TempRoot = value
	}
	for name, target := range map[string]*int{
		"JELEE_IMAGE_MAX_CONCURRENT":       &c.MaxConcurrent,
		"JELEE_IMAGE_MAX_OUTPUT_DIMENSION": &c.MaxOutputDimension,
		"JELEE_IMAGE_CACHE_ENTRIES":        &c.CacheEntries,
		"JELEE_IMAGE_TIMEOUT_SECONDS":      &c.TimeoutSeconds,
		"JELEE_IMAGE_CACHE_TTL_SECONDS":    &c.CacheTTLSeconds,
		"JELEE_IMAGE_DEFAULT_QUALITY":      &c.DefaultQuality,
	} {
		if value, ok := lookup(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	for name, target := range map[string]*int64{
		"JELEE_IMAGE_MAX_WORKING_BYTES": &c.MaxImageBytes,
		"JELEE_IMAGE_MAX_SOURCE_BYTES":  &c.MaxSourceBytes,
		"JELEE_IMAGE_MAX_OUTPUT_BYTES":  &c.MaxOutputBytes,
		"JELEE_IMAGE_CACHE_BYTES":       &c.CacheBytes,
	} {
		if value, ok := lookup(name); ok {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	return nil
}
