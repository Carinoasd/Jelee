package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
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
	// OutputFormats lists the encodings a request may ask for (G40.6);
	// DefaultFormat is used when it names none and must be listed. Only
	// jpeg and webp exist; AVIF has no reliable pure Go encoder (ADR 0010).
	OutputFormats []string `json:"outputFormats"`
	DefaultFormat string   `json:"defaultFormat"`
	// Crop selects how fill requests are cropped: smart (the window with the
	// most edge detail), center, or off (fill requests are fitted instead).
	Crop string `json:"crop"`
	// The persistent original/variant store is used only when StoreRoot is
	// set; otherwise rendered images live in the memory cache alone. Byte
	// limits and the index bound are per class.
	StoreRoot          string `json:"storeRoot"`
	StoreOriginalBytes int64  `json:"storeOriginalBytes"`
	StoreVariantBytes  int64  `json:"storeVariantBytes"`
	StoreEntries       int    `json:"storeEntries"`
}

// TempRoot must be explicitly set to an existing private directory when
// images are enabled. The adapter also refuses overlap with media roots.
func DefaultImagesConfig() ImagesConfig {
	return ImagesConfig{MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20, MaxOutputBytes: 2 << 20, MaxOutputDimension: 1024, CacheBytes: 32 << 20, CacheEntries: 128, TimeoutSeconds: 15, CacheTTLSeconds: 300, DefaultQuality: 85,
		OutputFormats: []string{"jpeg", "webp"}, DefaultFormat: "jpeg", Crop: ImageCropSmart,
		StoreOriginalBytes: 4 << 30, StoreVariantBytes: 1 << 30, StoreEntries: 131072}
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
	if err := c.validateOutput(); err != nil {
		return err
	}
	// Store minimums exceed the largest source and output object limits.
	if c.StoreOriginalBytes < 64<<20 || c.StoreOriginalBytes > 4<<40 || c.StoreVariantBytes < 16<<20 || c.StoreVariantBytes > 1<<40 || c.StoreEntries < 1024 || c.StoreEntries > 1<<21 {
		return errors.New("image store limits are outside supported bounds")
	}
	if c.StoreRoot != "" {
		// The adapter rejects media-root overlap and symlink aliases on open.
		if len(c.StoreRoot) > 4096 || !utf8.ValidString(c.StoreRoot) || strings.ContainsFunc(c.StoreRoot, unicode.IsControl) || !filepath.IsAbs(c.StoreRoot) {
			return errors.New("images storeRoot must be an absolute private directory")
		}
		store, temp := filepath.Clean(c.StoreRoot), filepath.Clean(c.TempRoot)
		if imageConfigPathInside(store, temp) || imageConfigPathInside(temp, store) {
			return errors.New("images storeRoot must not overlap tempRoot")
		}
	}
	// Per-field bounds above make this arithmetic safe before conversion.
	if int64(c.MaxConcurrent)*c.MaxImageBytes+c.CacheBytes > 1<<30 || c.MaxOutputBytes >= c.MaxImageBytes {
		return errors.New("image aggregate memory budget exceeds supported limits")
	}
	return nil
}

// Image crop modes (JELEE_IMAGE_CROP).
const (
	ImageCropSmart  = "smart"
	ImageCropCenter = "center"
	ImageCropOff    = "off"
)

func (c ImagesConfig) validateOutput() error {
	if len(c.OutputFormats) < 1 || len(c.OutputFormats) > 2 {
		return errors.New("images outputFormats must list jpeg, webp or both")
	}
	seen := map[string]bool{}
	for _, format := range c.OutputFormats {
		if format == "avif" {
			return errors.New("images outputFormats: avif output is not supported (no pure Go encoder)")
		}
		if format != "jpeg" && format != "webp" || seen[format] {
			return errors.New("images outputFormats must list jpeg, webp or both")
		}
		seen[format] = true
	}
	if !seen[c.DefaultFormat] {
		return errors.New("images defaultFormat must be one of outputFormats")
	}
	if c.Crop != ImageCropSmart && c.Crop != ImageCropCenter && c.Crop != ImageCropOff {
		return errors.New("images crop must be smart, center or off")
	}
	return nil
}

// AllowsFormat reports whether a request may ask for format. An unset
// list (a configuration built without defaults) offers JPEG only.
func (c ImagesConfig) AllowsFormat(format string) bool {
	if len(c.OutputFormats) == 0 {
		return format == "jpeg"
	}
	for _, allowed := range c.OutputFormats {
		if allowed == format {
			return true
		}
	}
	return false
}

func (c *ImagesConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_IMAGE_TEMP_ROOT"); ok {
		c.TempRoot = value
	}
	if value, ok := lookup("JELEE_IMAGE_STORE_ROOT"); ok {
		c.StoreRoot = value
	}
	if value, ok := lookup("JELEE_IMAGE_OUTPUT_FORMATS"); ok {
		c.OutputFormats = nil
		for _, format := range strings.Split(value, ",") {
			c.OutputFormats = append(c.OutputFormats, strings.ToLower(strings.TrimSpace(format)))
		}
	}
	if value, ok := lookup("JELEE_IMAGE_DEFAULT_FORMAT"); ok {
		c.DefaultFormat = strings.ToLower(strings.TrimSpace(value))
	}
	if value, ok := lookup("JELEE_IMAGE_CROP"); ok {
		c.Crop = strings.ToLower(strings.TrimSpace(value))
	}
	for name, target := range map[string]*int{
		"JELEE_IMAGE_MAX_CONCURRENT":       &c.MaxConcurrent,
		"JELEE_IMAGE_MAX_OUTPUT_DIMENSION": &c.MaxOutputDimension,
		"JELEE_IMAGE_CACHE_ENTRIES":        &c.CacheEntries,
		"JELEE_IMAGE_TIMEOUT_SECONDS":      &c.TimeoutSeconds,
		"JELEE_IMAGE_CACHE_TTL_SECONDS":    &c.CacheTTLSeconds,
		"JELEE_IMAGE_DEFAULT_QUALITY":      &c.DefaultQuality,
		"JELEE_IMAGE_STORE_ENTRIES":        &c.StoreEntries,
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
		"JELEE_IMAGE_MAX_WORKING_BYTES":    &c.MaxImageBytes,
		"JELEE_IMAGE_MAX_SOURCE_BYTES":     &c.MaxSourceBytes,
		"JELEE_IMAGE_MAX_OUTPUT_BYTES":     &c.MaxOutputBytes,
		"JELEE_IMAGE_CACHE_BYTES":          &c.CacheBytes,
		"JELEE_IMAGE_STORE_ORIGINAL_BYTES": &c.StoreOriginalBytes,
		"JELEE_IMAGE_STORE_VARIANT_BYTES":  &c.StoreVariantBytes,
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

func imageConfigPathInside(parent, child string) bool {
	if runtime.GOOS == "windows" {
		parent, child = strings.ToLower(parent), strings.ToLower(child)
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}
