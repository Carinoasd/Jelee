package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestResourcesConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"resources":{"cpuFactor":0.5,"io":3,"total":4,"queue":0}}`), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path, "JELEE_RESOURCE_TOTAL": "7"}
	c, err := LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources != (ResourcesConfig{CPUFactor: 0.5, IO: 3, Total: 7, Queue: 0}) {
		t.Fatalf("file/env lost: %+v", c.Resources)
	}
	if n := c.Resources.CPULimit(); n < 1 || n > 256 {
		t.Fatal("CPU bound lost")
	}
	delete(values, "JELEE_CONFIG")
	delete(values, "JELEE_RESOURCE_TOTAL")
	c, err = LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil || c.Resources != DefaultResourcesConfig() {
		t.Fatal("resource defaults lost")
	}
	for key, value := range map[string]string{"JELEE_RESOURCE_CPU_FACTOR": "NaN", "JELEE_RESOURCE_IO": "0", "JELEE_RESOURCE_TOTAL": "1025", "JELEE_RESOURCE_QUEUE": "-1"} {
		values[key] = value
		if _, err := LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok }); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
		delete(values, key)
	}
	for _, factor := range []float64{math.Inf(1), math.NaN(), 0, 8.01} {
		bad := DefaultResourcesConfig()
		bad.CPUFactor = factor
		if bad.Validate() == nil {
			t.Fatal("invalid factor accepted")
		}
	}
}
