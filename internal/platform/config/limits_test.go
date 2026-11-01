package config

import "testing"

func TestConnectionAndStreamDefaultsAndBounds(t *testing.T) {
	lookup := func(values map[string]string) func(string) (string, bool) {
		values["JELEE_DATABASE_URL"] = "postgres://localhost/jelee"
		return func(key string) (string, bool) { v, ok := values[key]; return v, ok }
	}
	c, err := LoadWith(lookup(map[string]string{}))
	if err != nil || c.MaxConnections != 64 || c.MaxStreams != 8 {
		t.Fatalf("defaults: connections %d streams %d: %v", c.MaxConnections, c.MaxStreams, err)
	}
	c, err = LoadWith(lookup(map[string]string{"JELEE_MAX_CONNECTIONS": "1024", "JELEE_MAX_STREAMS": "1024"}))
	if err != nil || c.MaxConnections != 1024 || c.MaxStreams != 1024 {
		t.Fatalf("upper bounds rejected: %v", err)
	}
	for _, values := range []map[string]string{{"JELEE_MAX_CONNECTIONS": "1025"}, {"JELEE_MAX_STREAMS": "1025"}} {
		if _, err := LoadWith(lookup(values)); err == nil {
			t.Fatalf("%v accepted", values)
		}
	}
	// The job reserve still applies on top of the larger pool.
	jobs := map[string]string{"JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_JOB_WORKERS": "8"}
	if _, err := LoadWith(lookup(jobs)); err != nil {
		t.Fatalf("default pool with eight workers: %v", err)
	}
	jobs["JELEE_MAX_CONNECTIONS"] = "9"
	if _, err := LoadWith(lookup(jobs)); err == nil {
		t.Fatal("eight workers with nine connections accepted")
	}
}
