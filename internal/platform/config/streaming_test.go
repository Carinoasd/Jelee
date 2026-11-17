package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStreamingConfiguration(t *testing.T) {
	if got := (Config{}).Streaming.RevokeCheckInterval(); got != 5*time.Second {
		t.Fatal("zero configuration must keep the 5 second recheck", got)
	}
	load := func(values map[string]string) (Config, error) {
		values["JELEE_DATABASE_URL"] = "postgres://localhost/jelee"
		return LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	}
	c, err := load(map[string]string{})
	if err != nil || c.Streaming != DefaultStreamingConfig() || c.Streaming.RevokeCheckInterval() != 5*time.Second ||
		!c.Streaming.EnableStreamLimit || c.Streaming.MaxStreamsPerUser != 4 || c.Streaming.MaxStreamsPerDevice != 0 ||
		!c.Streaming.EnableBandwidthLimit || c.Streaming.MaxKbpsPerUser != 0 || c.Streaming.BandwidthScope != "user" {
		t.Fatalf("defaults: %+v %v", c.Streaming, err)
	}
	c, err = load(map[string]string{"JELEE_STREAM_REVOKE_CHECK_SECONDS": "2", "JELEE_STREAM_LIMIT_ENABLED": "false", "JELEE_STREAM_MAX_PER_USER": "8",
		"JELEE_STREAM_MAX_PER_DEVICE": "1", "JELEE_BANDWIDTH_LIMIT_ENABLED": "false", "JELEE_BANDWIDTH_MAX_KBPS_PER_USER": "20000", "JELEE_BANDWIDTH_SCOPE": "device"})
	want := StreamingConfig{RevokeCheckSeconds: 2, MaxStreamsPerUser: 8, MaxStreamsPerDevice: 1, MaxKbpsPerUser: 20000, BandwidthScope: "device"}
	if err != nil || c.Streaming != want || c.Streaming.RevokeCheckInterval() != 2*time.Second {
		t.Fatalf("environment: %+v %v", c.Streaming, err)
	}
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"streaming":{"revokeCheckSeconds":10,"enableStreamLimit":true,"maxStreamsPerUser":2,"enableBandwidthLimit":true,"maxKbpsPerUser":8000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = load(map[string]string{"JELEE_CONFIG": file})
	if err != nil || c.Streaming.RevokeCheckSeconds != 10 || c.Streaming.MaxStreamsPerUser != 2 || c.Streaming.MaxKbpsPerUser != 8000 {
		t.Fatalf("file: %+v %v", c.Streaming, err)
	}
	for key, value := range map[string]string{
		"JELEE_STREAM_REVOKE_CHECK_SECONDS": "0", "JELEE_STREAM_LIMIT_ENABLED": "sometimes", "JELEE_STREAM_MAX_PER_USER": "-1",
		"JELEE_STREAM_MAX_PER_DEVICE": "129", "JELEE_BANDWIDTH_LIMIT_ENABLED": "", "JELEE_BANDWIDTH_MAX_KBPS_PER_USER": "10000001",
		"JELEE_BANDWIDTH_SCOPE": "session",
	} {
		if _, err := load(map[string]string{key: value}); err == nil {
			t.Fatalf("%s=%q accepted", key, value)
		}
	}
	for _, invalid := range []StreamingConfig{{RevokeCheckSeconds: -1}, {RevokeCheckSeconds: 301}, {MaxStreamsPerUser: 129}, {MaxStreamsPerDevice: -1}, {MaxKbpsPerUser: -1}, {BandwidthScope: "User"}} {
		if invalid.Validate() == nil {
			t.Fatalf("invalid streaming configuration accepted: %+v", invalid)
		}
	}
	file = filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(file, []byte(`{"streaming":{"maxStreamsPerUser":1000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(map[string]string{"JELEE_CONFIG": file}); err == nil {
		t.Fatal("out-of-range file value accepted")
	}
}

func TestStreamingBufferAndPrefetch(t *testing.T) {
	if got := (StreamingConfig{}); got.BufferBytes() != 32<<10 || got.PrefetchBytes() != 2048<<10 {
		t.Fatal("zero configuration must keep the 32 KiB buffer and 2 MiB read-ahead", got.BufferBytes(), got.PrefetchBytes())
	}
	if got := DefaultStreamingConfig(); got.BufferBytes() != 32<<10 || got.PrefetchBytes() != 2048<<10 {
		t.Fatal("defaults", got)
	}
	load := func(values map[string]string) (Config, error) {
		values["JELEE_DATABASE_URL"] = "postgres://localhost/jelee"
		return LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	}
	c, err := load(map[string]string{"JELEE_STREAM_BUFFER_KIB": "256", "JELEE_STREAM_PREFETCH_KIB": "64"})
	if err != nil || c.Streaming.BufferBytes() != 256<<10 || c.Streaming.PrefetchBytes() != 64<<10 {
		t.Fatalf("environment: %+v %v", c.Streaming, err)
	}
	c, err = load(map[string]string{"JELEE_STREAM_PREFETCH_DISABLED": "true", "JELEE_STREAM_PREFETCH_KIB": "4096"})
	if err != nil || c.Streaming.PrefetchBytes() != 0 {
		t.Fatalf("disabled: %+v %v", c.Streaming, err)
	}
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"streaming":{"bufferKiB":1024,"prefetchKiB":65536}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = load(map[string]string{"JELEE_CONFIG": file})
	if err != nil || c.Streaming.BufferBytes() != 1<<20 || c.Streaming.PrefetchBytes() != 64<<20 || !c.Streaming.EnableStreamLimit {
		t.Fatalf("file: %+v %v", c.Streaming, err)
	}
	for key, value := range map[string]string{"JELEE_STREAM_BUFFER_KIB": "15", "JELEE_STREAM_PREFETCH_KIB": "65537", "JELEE_STREAM_PREFETCH_DISABLED": "maybe"} {
		if _, err := load(map[string]string{key: value}); err == nil {
			t.Errorf("%s=%s accepted", key, value)
		}
	}
	for _, bad := range []StreamingConfig{{BufferKiB: 1025}, {BufferKiB: -1}, {PrefetchKiB: 63}, {PrefetchKiB: -1}} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
