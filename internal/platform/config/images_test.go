package config

import (
	"math"
	"testing"
)

func TestImagesConfigurationRolloutAndOverrides(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := LoadWith(lookup)
	if err != nil || cfg.EnableImages || cfg.Images.MaxConcurrent != 2 || cfg.Images.MaxImageBytes != 96<<20 {
		t.Fatal("invalid disabled defaults", err)
	}
	values["JELEE_ENABLE_IMAGES"] = "true"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("images accepted without accounts/catalog")
	}
	values["JELEE_ENABLE_ACCOUNTS"], values["JELEE_ENABLE_CATALOG"] = "true", "true"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("images accepted without private scratch path")
	}
	values["JELEE_IMAGE_TEMP_ROOT"] = t.TempDir()
	values["JELEE_IMAGE_MAX_CONCURRENT"] = "1"
	values["JELEE_IMAGE_MAX_WORKING_BYTES"] = "67108864"
	values["JELEE_IMAGE_DEFAULT_QUALITY"] = "90"
	cfg, err = LoadWith(lookup)
	if err != nil || !cfg.EnableImages || cfg.Images.MaxConcurrent != 1 || cfg.Images.MaxImageBytes != 64<<20 || cfg.Images.DefaultQuality != 90 {
		t.Fatal("image overrides", err)
	}
	values["JELEE_IMAGE_CACHE_BYTES"] = "9223372036854775808"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestImagesConfigurationRejectsUnsafeBudgets(t *testing.T) {
	base := DefaultImagesConfig()
	base.TempRoot = t.TempDir()
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ImagesConfig){
		"relative path":    func(c *ImagesConfig) { c.TempRoot = "images" },
		"control in path":  func(c *ImagesConfig) { c.TempRoot += "\n" },
		"zero slots":       func(c *ImagesConfig) { c.MaxConcurrent = 0 },
		"working overflow": func(c *ImagesConfig) { c.MaxImageBytes = math.MaxInt64 },
		"aggregate":        func(c *ImagesConfig) { c.MaxConcurrent = 8; c.MaxImageBytes = 256 << 20 },
		"output capacity":  func(c *ImagesConfig) { c.CacheBytes = c.MaxOutputBytes - 1 },
		"empty cache":      func(c *ImagesConfig) { c.CacheEntries = 0 },
		"no timeout":       func(c *ImagesConfig) { c.TimeoutSeconds = 0 },
		"no expiry":        func(c *ImagesConfig) { c.CacheTTLSeconds = 0 },
		"quality":          func(c *ImagesConfig) { c.DefaultQuality = 101 },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			change(&value)
			if value.Validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
