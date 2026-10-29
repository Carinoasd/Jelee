package config

import (
	"path/filepath"
	"testing"
)

func TestImageFetchOffByDefaultAndRequiresJobsImagesAndStore(t *testing.T) {
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := LoadWith(lookup)
	if err != nil || cfg.EnableImageFetch {
		t.Fatal("image fetch must default to off (local-only images)", err)
	}
	values["JELEE_ENABLE_IMAGE_FETCH"] = "true"
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("image fetch without jobs and images")
	}
	for key, value := range map[string]string{"JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_JOBS": "true"} {
		values[key] = value
	}
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("image fetch without images")
	}
	values["JELEE_ENABLE_IMAGES"], values["JELEE_IMAGE_TEMP_ROOT"] = "true", t.TempDir()
	if _, err := LoadWith(lookup); err == nil {
		t.Fatal("image fetch without a persistent image store")
	}
	values["JELEE_IMAGE_STORE_ROOT"] = filepath.Join(t.TempDir(), "store")
	if cfg, err = LoadWith(lookup); err != nil || !cfg.EnableImageFetch {
		t.Fatal("valid image fetch configuration", err)
	}
}
