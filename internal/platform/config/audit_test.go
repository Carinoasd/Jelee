package config

import (
	"testing"
	"time"
)

func TestAuditConfigDefaultsAndEnvironment(t *testing.T) {
	d := DefaultAuditConfig()
	if d.Validate() != nil || d.PurgeInterval() != time.Hour || d.Batch() != 1000 || d.MaxBatches() != 100 {
		t.Fatalf("defaults: %+v", d)
	}
	var zero AuditConfig
	if zero.Validate() != nil || zero.PurgeInterval() != 0 || zero.Batch() != 1000 || zero.MaxBatches() != 100 {
		t.Fatal("a zero section must leave the purge off with default batches")
	}
	env := map[string]string{"JELEE_AUDIT_PURGE_INTERVAL_MINUTES": "15", "JELEE_AUDIT_PURGE_BATCH": "250", "JELEE_AUDIT_PURGE_MAX_BATCHES": "4"}
	c := DefaultAuditConfig()
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.Validate() != nil || c.PurgeInterval() != 15*time.Minute || c.Batch() != 250 || c.MaxBatches() != 4 {
		t.Fatalf("environment not applied: %+v", c)
	}
	off := DefaultAuditConfig()
	if err := off.loadEnvironment(func(k string) (string, bool) { return "0", k == "JELEE_AUDIT_PURGE_INTERVAL_MINUTES" }); err != nil || off.PurgeInterval() != 0 || off.Validate() != nil {
		t.Fatal("interval 0 must switch the purge off")
	}
	for key := range env {
		if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == key }); err == nil {
			t.Fatalf("non-numeric %s accepted", key)
		}
	}
	for _, bad := range []AuditConfig{{PurgeIntervalMinutes: -1}, {PurgeIntervalMinutes: 10081}, {PurgeBatch: -1}, {PurgeBatch: 10001}, {PurgeMaxBatches: -1}, {PurgeMaxBatches: 1001}} {
		if bad.Validate() == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	load := func(batch string) (Config, error) {
		return LoadWith(func(k string) (string, bool) {
			switch k {
			case "JELEE_DATABASE_URL":
				return "postgres://jelee@localhost/jelee", true
			case "JELEE_AUDIT_PURGE_BATCH":
				return batch, batch != ""
			}
			return "", false
		})
	}
	if full, err := load(""); err != nil || full.Audit != DefaultAuditConfig() {
		t.Fatalf("defaults through LoadWith: %+v %v", full.Audit, err)
	}
	if full, err := load("20000"); err == nil {
		t.Fatalf("out-of-range batch loaded: %+v", full.Audit)
	}
}

func TestAuditArchiveConfig(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"JELEE_AUDIT_ARCHIVE_DIR": dir, "JELEE_AUDIT_ARCHIVE_RETENTION_DAYS": "90",
		"JELEE_AUDIT_ARCHIVE_SECURITY_RETENTION_DAYS": "400", "JELEE_AUDIT_ARCHIVE_MAX_MB": "512",
	}
	c := DefaultAuditConfig()
	if err := c.loadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if c.Validate() != nil || c.ArchiveDir != dir || c.ArchiveRetentionDays != 90 || c.ArchiveSecurityRetentionDays != 400 || c.ArchiveMaxMB != 512 {
		t.Fatalf("archive environment not applied: %+v", c)
	}
	for key := range env {
		if key == "JELEE_AUDIT_ARCHIVE_DIR" {
			continue
		}
		if err := c.loadEnvironment(func(k string) (string, bool) { return "x", k == key }); err == nil {
			t.Fatalf("non-numeric %s accepted", key)
		}
	}
	abs := DefaultAuditConfig()
	abs.ArchiveDir = dir
	for name, bad := range map[string]AuditConfig{
		"relative dir":      {PurgeIntervalMinutes: 60, ArchiveDir: "archive"},
		"limit without dir": {PurgeIntervalMinutes: 60, ArchiveMaxMB: 5},
		"days without dir":  {PurgeIntervalMinutes: 60, ArchiveRetentionDays: 5},
		"negative days":     {PurgeIntervalMinutes: 60, ArchiveDir: dir, ArchiveRetentionDays: -1},
		"too many days":     {PurgeIntervalMinutes: 60, ArchiveDir: dir, ArchiveSecurityRetentionDays: 36501},
		"huge cap":          {PurgeIntervalMinutes: 60, ArchiveDir: dir, ArchiveMaxMB: 1<<20 + 1},
		"purge off":         {ArchiveDir: dir},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if abs.Validate() != nil {
		t.Fatal("archive with defaults must validate")
	}
}
