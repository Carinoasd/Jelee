package config

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MatroskaConfig enables copying embedded text subtitles and font
// attachments out of Matroska sources into a rebuildable cache (G15.5,
// G15.7). It needs the optional mkvtoolnix runtime (E4); without it the
// routes answer like missing items and doctor reports the tool.
type MatroskaConfig struct {
	EnableExtraction bool   `json:"enableExtraction"`
	CacheRoot        string `json:"cacheRoot"`
	CacheMaxBytes    int64  `json:"cacheMaxBytes"`
}

// DefaultMatroskaConfig keeps extraction off with a 1 GiB cache bound.
func DefaultMatroskaConfig() MatroskaConfig { return MatroskaConfig{CacheMaxBytes: 1 << 30} }

// Validate checks the cache root and bound when extraction is enabled.
func (c MatroskaConfig) Validate() error {
	if !c.EnableExtraction {
		return nil
	}
	if len(c.CacheRoot) > 4096 || !utf8.ValidString(c.CacheRoot) || strings.ContainsFunc(c.CacheRoot, unicode.IsControl) || !filepath.IsAbs(c.CacheRoot) || filepath.Clean(c.CacheRoot) != c.CacheRoot {
		return errors.New("matroska cacheRoot must be an absolute private directory")
	}
	if c.CacheMaxBytes < 256<<20 || c.CacheMaxBytes > 1<<40 {
		return errors.New("matroska cacheMaxBytes is outside supported bounds")
	}
	return nil
}

func (c *MatroskaConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_ENABLE_MATROSKA_EXTRACTION"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("JELEE_ENABLE_MATROSKA_EXTRACTION must be a boolean")
		}
		c.EnableExtraction = enabled
	}
	if value, ok := lookup("JELEE_MATROSKA_CACHE_ROOT"); ok {
		c.CacheRoot = value
	}
	if value, ok := lookup("JELEE_MATROSKA_CACHE_MAX_BYTES"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("JELEE_MATROSKA_CACHE_MAX_BYTES must be an integer")
		}
		c.CacheMaxBytes = n
	}
	return nil
}
