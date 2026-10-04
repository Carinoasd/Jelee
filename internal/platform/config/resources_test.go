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
	if c.Resources != (ResourcesConfig{CPUFactor: 0.5, IO: 3, Total: 7, Queue: 0, Adaptive: DefaultAdaptiveConfig()}) {
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

func TestAdaptiveConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"resources":{"adaptive":{"loadHigh":2,"loadLow":1}},"logging":{"traceSampleRate":0.5}}`), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_CONFIG": path, "JELEE_RESOURCE_ADAPTIVE": "true",
		"JELEE_RESOURCE_ADAPTIVE_INTERVAL_SECONDS": "5", "JELEE_RESOURCE_ADAPTIVE_MIN_PERCENT": "50", "JELEE_TRACE_SAMPLE_RATE": "0.25"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	c, err := LoadWith(lookup)
	if err != nil {
		t.Fatal(err)
	}
	a := c.Resources.Adaptive
	if !a.Enabled || a.IntervalSeconds != 5 || a.MinPercent != 50 || a.LoadHigh != 2 || a.LoadLow != 1 || a.CooldownSeconds != 120 || c.Logging.TraceSampleRate != 0.25 {
		t.Fatalf("adaptive or tracing settings lost: %+v rate %v", a, c.Logging.TraceSampleRate)
	}
	if DefaultResourcesConfig().Adaptive.Enabled || DefaultLoggingConfig().TraceSampleRate != 0.1 {
		t.Fatal("adaptive concurrency must be off and sampling 0.1 by default")
	}
	for key, value := range map[string]string{"JELEE_RESOURCE_ADAPTIVE": "maybe", "JELEE_RESOURCE_ADAPTIVE_INTERVAL_SECONDS": "0", "JELEE_RESOURCE_ADAPTIVE_MIN_PERCENT": "101",
		"JELEE_RESOURCE_ADAPTIVE_DWELL_SECONDS": "x", "JELEE_TRACE_SAMPLE_RATE": "1.5"} {
		saved, had := values[key]
		values[key] = value
		if _, err := LoadWith(lookup); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
		if had {
			values[key] = saved
		} else {
			delete(values, key)
		}
	}
	for _, mutate := range []func(*AdaptiveConfig){
		func(a *AdaptiveConfig) { a.LoadLow = a.LoadHigh },
		func(a *AdaptiveConfig) { a.ThrottleHigh = 1.5 },
		func(a *AdaptiveConfig) { a.MemoryPressureLow = 0 },
		func(a *AdaptiveConfig) { a.MemoryUsageHigh = math.NaN() },
		func(a *AdaptiveConfig) { a.StepPercent = 0 },
		func(a *AdaptiveConfig) { a.CooldownSeconds = -1 },
	} {
		bad := DefaultResourcesConfig()
		mutate(&bad.Adaptive)
		if bad.Validate() == nil {
			t.Fatalf("invalid adaptive settings accepted: %+v", bad.Adaptive)
		}
	}
	bad := DefaultLoggingConfig()
	bad.TraceSampleRate = math.NaN()
	if bad.Validate() == nil {
		t.Fatal("NaN sample rate accepted")
	}
}
