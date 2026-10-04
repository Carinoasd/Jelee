package config

import (
	"path/filepath"
	"testing"
)

func TestMatroskaExtractionConfiguration(t *testing.T) {
	root := t.TempDir()
	base := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_DIRECT": "true"}
	load := func(extra map[string]string) (Config, error) {
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range extra {
			values[k] = v
		}
		return LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	}
	c, err := load(nil)
	if err != nil || c.Matroska.EnableExtraction || c.Matroska.CacheMaxBytes != 1<<30 {
		t.Fatalf("default: %v %+v", err, c.Matroska)
	}
	c, err = load(map[string]string{"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": root, "JELEE_MATROSKA_CACHE_MAX_BYTES": "536870912"})
	if err != nil || !c.Matroska.EnableExtraction || c.Matroska.CacheRoot != root || c.Matroska.CacheMaxBytes != 512<<20 {
		t.Fatalf("enabled: %v %+v", err, c.Matroska)
	}
	for name, extra := range map[string]map[string]string{
		"flag":       {"JELEE_ENABLE_MATROSKA_EXTRACTION": "maybe"},
		"bytes":      {"JELEE_MATROSKA_CACHE_MAX_BYTES": "lots"},
		"no root":    {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true"},
		"relative":   {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": "cache"},
		"unclean":    {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": root + string(filepath.Separator) + "."},
		"too small":  {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": root, "JELEE_MATROSKA_CACHE_MAX_BYTES": "1024"},
		"no direct":  {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": root, "JELEE_ENABLE_DIRECT": "false"},
		"no catalog": {"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": root, "JELEE_ENABLE_CATALOG": "false", "JELEE_ENABLE_DIRECT": "false"},
	} {
		if _, err := load(extra); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
