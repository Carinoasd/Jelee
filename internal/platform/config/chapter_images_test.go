package config

import (
	"path/filepath"
	"testing"
)

func TestChapterImagesOffByDefaultAndRequireProbeAndStore(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := LoadWith(lookup)
	if err != nil || cfg.EnableChapterImages {
		t.Fatal("chapter images must default to off", err)
	}
	values["JELEE_ENABLE_CHAPTER_IMAGES"] = "maybe"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("invalid flag accepted")
	}
	values["JELEE_ENABLE_CHAPTER_IMAGES"] = "true"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("chapter images without probe and images")
	}
	for key, value := range map[string]string{"JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_ENABLE_PROBE": "true"} {
		values[key] = value
	}
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("chapter images without images")
	}
	values["JELEE_ENABLE_IMAGES"], values["JELEE_IMAGE_TEMP_ROOT"] = "true", t.TempDir()
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("chapter images without a persistent image store")
	}
	values["JELEE_IMAGE_STORE_ROOT"] = filepath.Join(t.TempDir(), "store")
	if cfg, err = LoadWith(lookup); err != nil || !cfg.EnableChapterImages || cfg.EnableEmbeddedCovers {
		t.Fatal("valid chapter image configuration is independent of covers", err)
	}
	values["JELEE_ENABLE_CHAPTER_IMAGES"] = "false"
	if cfg, err = LoadWith(lookup); err != nil || cfg.EnableChapterImages {
		t.Fatal("explicit off", err)
	}
}
