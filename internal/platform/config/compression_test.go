package config

import "testing"

func TestCompressionConfigDefaultsEnvironmentAndBounds(t *testing.T) {
	var zero CompressionConfig
	if !zero.Enabled() || zero.GzipLevel() != 5 || zero.Threshold() != 1024 || zero.Validate() != nil {
		t.Fatalf("zero value must select gzip level 5 above 1 KiB: %+v", zero)
	}
	env := map[string]string{"JELEE_HTTP_COMPRESSION": "off", "JELEE_HTTP_COMPRESSION_LEVEL": "9", "JELEE_HTTP_COMPRESSION_MIN_BYTES": "2048"}
	var c CompressionConfig
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.Enabled() || c.GzipLevel() != 9 || c.Threshold() != 2048 || c.Validate() != nil {
		t.Fatalf("environment not applied: %+v", c)
	}
	for _, bad := range []CompressionConfig{{Mode: "brotli"}, {Level: 10}, {Level: -1}, {MinBytes: -1}, {MinBytes: 1<<20 + 1}} {
		if bad.Validate() == nil {
			t.Errorf("%+v must be rejected", bad)
		}
	}
	for _, key := range []string{"JELEE_HTTP_COMPRESSION_LEVEL", "JELEE_HTTP_COMPRESSION_MIN_BYTES"} {
		var c CompressionConfig
		if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == key }); err == nil {
			t.Errorf("%s must be numeric", key)
		}
	}
	cfg, err := LoadWith(func(k string) (string, bool) {
		v, ok := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_HTTP_COMPRESSION": "zstd"}[k]
		return v, ok
	})
	if err == nil {
		t.Fatalf("an unknown mode must fail the configuration: %+v", cfg.Compression)
	}
}
