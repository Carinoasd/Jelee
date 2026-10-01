package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func jobTestPolicy() domain.JobPolicy {
	return domain.JobPolicy{QueueLimit: 16, HistoryLimit: 16, MaxEntries: 10000, MaxDirectories: 1000, MaxAttempts: 3, MissingCountLimit: 10, MissingPercentLimit: 50}
}

func TestJobValidationRejectsUnboundedPoliciesAndUnsafeBatchPaths(t *testing.T) {
	p := jobTestPolicy()
	if !validJobPolicy(p) {
		t.Fatal("valid policy rejected")
	}
	for _, change := range []func(*domain.JobPolicy){func(p *domain.JobPolicy) { p.QueueLimit = 0 }, func(p *domain.JobPolicy) { p.HistoryLimit = 10001 }, func(p *domain.JobPolicy) { p.MaxEntries = 1000001 }, func(p *domain.JobPolicy) { p.MaxDirectories = 100001 }, func(p *domain.JobPolicy) { p.MaxAttempts = 0 }, func(p *domain.JobPolicy) { p.MissingCountLimit = 0 }, func(p *domain.JobPolicy) { p.MissingPercentLimit = 101 }} {
		q := p
		change(&q)
		if validJobPolicy(q) {
			t.Fatal("unbounded policy accepted")
		}
	}
	for _, change := range []func(*domain.JobPolicy){func(p *domain.JobPolicy) { p.QueueLimit = 1001 }, func(p *domain.JobPolicy) { p.HistoryLimit = 101 }, func(p *domain.JobPolicy) { p.MaxEntries = 99 }, func(p *domain.JobPolicy) { p.MaxEntries = 500001 }, func(p *domain.JobPolicy) { p.MaxAttempts = 11 }, func(p *domain.JobPolicy) { p.MissingCountLimit = 500001 }} {
		q := p
		change(&q)
		if validJobPolicy(q) {
			t.Fatal("adapter policy differs from public application limits")
		}
	}
	for _, key := range []string{"", "contains space", "secret\nkey", strings.Repeat("k", 129), "非ascii"} {
		if validJobKey(key) {
			t.Fatal("invalid idempotency key accepted")
		}
	}
	for _, ttl := range []time.Duration{0, time.Second - 1, time.Hour + 1} {
		if validLeaseDuration(ttl) {
			t.Fatal("invalid lease duration accepted")
		}
	}
	d := domain.ScanDirectory{RootID: "11111111-1111-4111-8111-111111111111", RootPath: "private-root", Path: "."}
	entry := domain.InventoryEntry{RootID: d.RootID, Path: "video.mkv", Kind: "video", Size: 42}
	if !validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{entry}, Directories: []string{"folder"}, Done: true}) {
		t.Fatal("valid batch rejected")
	}
	for _, name := range []string{".", "..", "../escape", "/absolute", "C:/host", "bad\\path", "folder/child", "a/../b", "bad\nname", strings.Repeat("a", 1025)} {
		e := entry
		e.Path = name
		if validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{e}}) {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
	e := entry
	e.Kind = "directory"
	if validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{e}}) {
		t.Fatal("unknown file kind accepted")
	}
	e = entry
	e.Size = -1
	if validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{e}}) {
		t.Fatal("negative size accepted")
	}
	if validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{entry, entry}}) || validScanBatch(d, domain.ScanBatch{Entries: []domain.InventoryEntry{entry}, Directories: []string{entry.Path}}) || validScanBatch(d, domain.ScanBatch{Skipped: 1}) {
		t.Fatal("duplicate path or intermediate skipped count accepted")
	}
	if validJobError(domain.JobSucceeded, "scan_io") || validJobError(domain.JobFailed, "private-root-and-error") || validJobError(domain.JobRunning, "") {
		t.Fatal("unsafe completion accepted")
	}
	for _, code := range []string{"scan_io", "scan_unavailable", "scan_limit", "job_timeout", "job_attempts_exhausted"} {
		if !validJobError(domain.JobFailed, code) {
			t.Fatal("worker error rejected")
		}
	}
}
