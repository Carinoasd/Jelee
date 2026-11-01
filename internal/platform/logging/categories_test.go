package logging

import (
	"context"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G46.3: the media record (seek, offset, If-Range, outcome) and the task
// progress record (kind, count, total, progress) pass the whitelist with
// valid values; malformed values under the same keys do not.
func TestMediaAndTaskFieldsPassTheWhitelist(t *testing.T) {
	r, out := openTest(t, Options{})
	log := r.Logger()
	const source = "00000000-0000-4000-8000-000000000011"
	const job = "00000000-0000-4000-8000-000000000012"
	log.Info("media stream interrupted", "component", "media", "method", "GET", "sourceId", source, "outcome", "write_timeout", "status", 206,
		"bytes", 4096, "seek", true, "offset", int64(1<<33), "ifRange", "hit", "durationMs", 12)
	log.Info("media stream interrupted", "component", "media", "sourceId", "/srv/media/a.mkv", "seek", "yes", "offset", "123", "ifRange", "\"etag\"")
	ctx := domain.WithLogFields(context.Background(), domain.LogFields{TaskID: job, JobRunID: job + ":3", Kind: "inventory_scan"})
	log.InfoContext(ctx, "job progress", "component", "jobs", "count", 40, "total", 100, "progress", 40.0)
	log.InfoContext(domain.WithLogFields(context.Background(), domain.LogFields{Kind: "Inventory Scan"}), "job progress", "component", "jobs", "total", "100", "progress", "40%")
	log.Error("process panic", "component", "system", "crashFile", "crash-20261101T120000.000000000Z.log")
	log.Error("process panic", "component", "system", "crashFile", "/var/log/jelee/crash/crash-20261101T120000.000000000Z.log")
	recs := records(t, flushed(t, r, out))
	if len(recs) != 6 {
		t.Fatalf("records %v", recs)
	}
	media := recs[0]
	if media["sourceId"] != source || media["seek"] != true || media["offset"] != float64(1<<33) || media["ifRange"] != "hit" || media["outcome"] != "write_timeout" {
		t.Fatalf("media record %v", media)
	}
	for _, key := range []string{"sourceId", "seek", "offset", "ifRange"} {
		if recs[1][key] != Redacted {
			t.Errorf("malformed %s passed: %v", key, recs[1][key])
		}
	}
	task := recs[2]
	if task["taskId"] != job || task["jobRunId"] != job+":3" || task["kind"] != "inventory_scan" || task["count"] != float64(40) || task["total"] != float64(100) || task["progress"] != float64(40) {
		t.Fatalf("task record %v", task)
	}
	for _, key := range []string{"kind", "total", "progress"} {
		if recs[3][key] != Redacted {
			t.Errorf("malformed %s passed: %v", key, recs[3][key])
		}
	}
	if recs[4]["crashFile"] != "crash-20261101T120000.000000000Z.log" || recs[5]["crashFile"] != Redacted {
		t.Fatalf("crash file %v / %v", recs[4]["crashFile"], recs[5]["crashFile"])
	}
}
