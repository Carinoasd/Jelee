package config

import (
	"math"
	"path/filepath"
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
	if cfg.Images.StoreRoot != "" || cfg.Images.StoreOriginalBytes != 4<<30 || cfg.Images.StoreVariantBytes != 1<<30 || cfg.Images.StoreEntries != 131072 {
		t.Fatal("image store defaults", cfg.Images)
	}
	values["JELEE_IMAGE_STORE_ROOT"] = filepath.Join(t.TempDir(), "store")
	values["JELEE_IMAGE_STORE_ORIGINAL_BYTES"] = "134217728"
	values["JELEE_IMAGE_STORE_VARIANT_BYTES"] = "33554432"
	values["JELEE_IMAGE_STORE_ENTRIES"] = "2048"
	cfg, err = LoadWith(lookup)
	if err != nil || cfg.Images.StoreRoot != values["JELEE_IMAGE_STORE_ROOT"] || cfg.Images.StoreOriginalBytes != 128<<20 || cfg.Images.StoreVariantBytes != 32<<20 || cfg.Images.StoreEntries != 2048 {
		t.Fatal("image store overrides", err)
	}
	values["JELEE_IMAGE_STORE_ROOT"] = values["JELEE_IMAGE_TEMP_ROOT"]
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("store sharing the scratch directory accepted")
	}
	delete(values, "JELEE_IMAGE_STORE_ROOT")
	values["JELEE_IMAGE_STORE_VARIANT_BYTES"] = "x"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("malformed store limit accepted")
	}
	values["JELEE_IMAGE_STORE_VARIANT_BYTES"] = "33554432"
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
	withStore := base
	withStore.StoreRoot = filepath.Join(filepath.Dir(base.TempRoot), "image-store")
	if err := withStore.Validate(); err != nil {
		t.Fatal("separate store root rejected", err)
	}
	for name, change := range map[string]func(*ImagesConfig){
		"relative path":     func(c *ImagesConfig) { c.TempRoot = "images" },
		"control in path":   func(c *ImagesConfig) { c.TempRoot += "\n" },
		"zero slots":        func(c *ImagesConfig) { c.MaxConcurrent = 0 },
		"working overflow":  func(c *ImagesConfig) { c.MaxImageBytes = math.MaxInt64 },
		"aggregate":         func(c *ImagesConfig) { c.MaxConcurrent = 8; c.MaxImageBytes = 256 << 20 },
		"output capacity":   func(c *ImagesConfig) { c.CacheBytes = c.MaxOutputBytes - 1 },
		"empty cache":       func(c *ImagesConfig) { c.CacheEntries = 0 },
		"no timeout":        func(c *ImagesConfig) { c.TimeoutSeconds = 0 },
		"no expiry":         func(c *ImagesConfig) { c.CacheTTLSeconds = 0 },
		"quality":           func(c *ImagesConfig) { c.DefaultQuality = 101 },
		"store relative":    func(c *ImagesConfig) { c.StoreRoot = "store" },
		"store control":     func(c *ImagesConfig) { c.StoreRoot = c.TempRoot + "-store\n" },
		"store is scratch":  func(c *ImagesConfig) { c.StoreRoot = c.TempRoot },
		"store in scratch":  func(c *ImagesConfig) { c.StoreRoot = filepath.Join(c.TempRoot, "store") },
		"scratch in store":  func(c *ImagesConfig) { c.StoreRoot = filepath.Dir(c.TempRoot) },
		"store originals":   func(c *ImagesConfig) { c.StoreOriginalBytes = 64<<20 - 1 },
		"store overflow":    func(c *ImagesConfig) { c.StoreOriginalBytes = math.MaxInt64 },
		"store variants":    func(c *ImagesConfig) { c.StoreVariantBytes = 16<<20 - 1 },
		"store variant cap": func(c *ImagesConfig) { c.StoreVariantBytes = 1<<40 + 1 },
		"store index":       func(c *ImagesConfig) { c.StoreEntries = 1023 },
		"store index cap":   func(c *ImagesConfig) { c.StoreEntries = 1<<21 + 1 },
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
