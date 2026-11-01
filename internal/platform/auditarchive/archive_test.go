package auditarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func record(id int64, category string, at time.Time) domain.AuditRecord {
	return domain.AuditRecord{ID: id, Category: category, Event: "user.created", TargetRef: "x", Before: json.RawMessage("null"), After: json.RawMessage(`{"a":1}`), OccurredAt: at}
}

func open(t *testing.T, o Options) (*Archive, string) {
	t.Helper()
	o.Dir = filepath.Join(t.TempDir(), "archive")
	a, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, o.Dir
}

func day(s string) time.Time {
	v, err := time.Parse("2006-01-02T15:04:05Z", s+"T10:00:00Z")
	if err != nil {
		panic(err)
	}
	return v
}

func TestArchiveSplitsByCategoryAndDay(t *testing.T) {
	a, dir := open(t, Options{})
	records := []domain.AuditRecord{
		record(1, "audit", day("2026-01-01")), record(2, "security", day("2026-01-01")), record(3, "audit", day("2026-01-01")),
		record(4, "audit", day("2026-01-02")),
	}
	receipt, err := a.Archive(context.Background(), records)
	if err != nil || receipt.Files != 3 || receipt.Bytes <= 0 {
		t.Fatalf("receipt %+v err %v", receipt, err)
	}
	files, err := a.List()
	if err != nil || len(files) != 3 {
		t.Fatalf("files %+v err %v", files, err)
	}
	if files[0].Name != "audit-audit-20260101-1-3.ndjson.gz" || files[1].Name != "audit-security-20260101-2-2.ndjson.gz" || files[2].Name != "audit-audit-20260102-4-4.ndjson.gz" {
		t.Fatalf("names %+v", files)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, files[0].Name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v", info, err)
		}
	}
	results, err := VerifyDir(dir)
	if err != nil || len(results) != 3 || results[0].Rows != 2 || results[1].Rows != 1 {
		t.Fatalf("verify %+v err %v", results, err)
	}
	for _, r := range results {
		if r.Err != "" {
			t.Fatalf("%s: %s", r.Name, r.Err)
		}
	}
	// The line shape is the administrator listing's record.
	f, _ := os.Open(filepath.Join(dir, files[0].Name))
	defer func() { _ = f.Close() }()
	zr, _ := gzip.NewReader(f)
	body, _ := io.ReadAll(zr)
	var first domain.AuditRecord
	if err := json.Unmarshal(bytes.SplitN(body, []byte("\n"), 2)[0], &first); err != nil || first.ID != 1 || first.Event != "user.created" {
		t.Fatalf("first line %s %v", body, err)
	}
	// No temporary file is left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Fatalf("leftover %s", e.Name())
		}
	}
}

func TestArchiveNeverOverwrites(t *testing.T) {
	a, dir := open(t, Options{})
	records := []domain.AuditRecord{record(5, "audit", day("2026-02-01")), record(6, "audit", day("2026-02-01"))}
	if _, err := a.Archive(context.Background(), records); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "audit-audit-20260201-5-6.ndjson.gz")
	before, _ := os.ReadFile(name)
	// Same rows again (a retry after a failed commit): kept, not rewritten.
	receipt, err := a.Archive(context.Background(), records)
	if err != nil || receipt.Files != 0 {
		t.Fatalf("retry %+v %v", receipt, err)
	}
	if after, _ := os.ReadFile(name); !bytes.Equal(before, after) {
		t.Fatal("existing archive file was rewritten")
	}
	// A damaged file with the target name is an error, never replaced.
	if err := os.WriteFile(name, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Archive(context.Background(), records); err == nil {
		t.Fatal("damaged existing file accepted")
	}
	if got, _ := os.ReadFile(name); string(got) != "garbage" {
		t.Fatal("damaged file was replaced")
	}
}

func TestArchiveRejectsBadInput(t *testing.T) {
	a, _ := open(t, Options{})
	if _, err := a.Archive(context.Background(), []domain.AuditRecord{record(1, "ordinary", day("2026-01-01"))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown category: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Archive(ctx, []domain.AuditRecord{record(1, "audit", day("2026-01-01"))}); err == nil {
		t.Fatal("canceled context archived")
	}
	for name, o := range map[string]Options{
		"no dir": {}, "negative days": {Dir: t.TempDir(), RetentionDays: -1}, "huge days": {Dir: t.TempDir(), SecurityRetentionDays: MaxRetentionDays + 1},
		"negative cap": {Dir: t.TempDir(), MaxBytes: -1},
	} {
		if _, err := Open(o); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted", name)
		}
	}
	// An unwritable location is reported, not ignored.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Dir: filepath.Join(blocker, "sub")}); err == nil {
		t.Fatal("directory below a file opened")
	}
}

func TestArchiveWriteFailureLeavesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced here")
	}
	a, dir := open(t, Options{})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := a.Archive(context.Background(), []domain.AuditRecord{record(1, "audit", day("2026-01-01"))}); err == nil {
		t.Fatal("write into a read-only directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("leftovers %v", entries)
	}
}

func TestPruneByAgeSecurityOverrideAndCap(t *testing.T) {
	a, dir := open(t, Options{RetentionDays: 30, SecurityRetentionDays: 90})
	if _, err := a.Archive(context.Background(), []domain.AuditRecord{
		record(1, "audit", day("2026-01-01")), record(2, "security", day("2026-01-01")),
		record(3, "audit", day("2026-03-01")), record(4, "security", day("2026-03-01")),
	}); err != nil {
		t.Fatal(err)
	}
	// Foreign files are never touched.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := day("2026-03-15")
	pruned, err := a.Prune(context.Background(), now)
	if err != nil || pruned.Files != 1 {
		t.Fatalf("pruned %+v %v", pruned, err)
	}
	files, _ := a.List()
	// The 2026-01-01 audit file is past 30 days; its security sibling has 90.
	if len(files) != 3 || !containsName(files, "audit-security-20260101-2-2.ndjson.gz") || containsName(files, "audit-audit-20260101-1-1.ndjson.gz") {
		t.Fatalf("left %+v", files)
	}
	// Day 1+retention boundary: the audit file of 2026-03-01 expires only after 2026-04-01.
	if got := containsName(files, "audit-audit-20260301-3-3.ndjson.gz"); !got {
		t.Fatal("a file inside its retention was removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("foreign file removed")
	}
	// Security rows outlive audit rows (90 days), then go too.
	pruned, err = a.Prune(context.Background(), day("2026-06-15"))
	if err != nil || pruned.Files != 3 {
		t.Fatalf("late prune %+v %v", pruned, err)
	}

	capped, cdir := open(t, Options{MaxBytes: 1})
	for i, d := range []string{"2026-01-01", "2026-01-02", "2026-01-03"} {
		if _, err := capped.Archive(context.Background(), []domain.AuditRecord{record(int64(i+1), "audit", day(d))}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := capped.List()
	if len(got) != 3 {
		t.Fatalf("setup %v", got)
	}
	var total int64
	for _, f := range got {
		total += f.Bytes
	}
	capped.opts.MaxBytes = total - got[0].Bytes // drop exactly the oldest
	pruned, err = capped.Prune(context.Background(), day("2026-01-04"))
	if err != nil || pruned.Files != 1 || pruned.Bytes != got[0].Bytes {
		t.Fatalf("cap prune %+v %v", pruned, err)
	}
	if _, err := os.Stat(filepath.Join(cdir, got[0].Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oldest file survived the cap")
	}
}

func containsName(files []File, name string) bool {
	for _, f := range files {
		if f.Name == name {
			return true
		}
	}
	return false
}

func TestPruneRemovesStaleTempOnly(t *testing.T) {
	a, dir := open(t, Options{})
	stale := filepath.Join(dir, tempPrefix+"old")
	fresh := filepath.Join(dir, tempPrefix+"new")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Prune(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale temp kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh temp removed")
	}
}

func TestVerifyDetectsDamage(t *testing.T) {
	a, dir := open(t, Options{})
	if _, err := a.Archive(context.Background(), []domain.AuditRecord{record(1, "audit", day("2026-01-01")), record(2, "audit", day("2026-01-01"))}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "audit-audit-20260101-1-2.ndjson.gz")
	good, _ := os.ReadFile(name)
	write := func(file string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gz := func(lines ...string) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		for _, l := range lines {
			_, _ = w.Write([]byte(l))
		}
		_ = w.Close()
		return b.Bytes()
	}
	line := func(id int, cat, at string) string {
		return `{"id":` + string(rune('0'+id)) + `,"category":"` + cat + `","event":"user.created","before":null,"after":null,"occurredAt":"` + at + `"}` + "\n"
	}
	cases := map[string][]byte{
		"audit-audit-20260101-1-2.ndjson.gz": good[:len(good)-10],
		"audit-audit-20260102-1-2.ndjson.gz": gz(line(1, "audit", "2026-01-02T01:00:00Z"), strings.TrimSuffix(line(2, "audit", "2026-01-02T01:00:00Z"), "\n")),
		"audit-audit-20260103-1-2.ndjson.gz": gz(line(1, "audit", "2026-01-03T01:00:00Z")),
		"audit-audit-20260104-1-2.ndjson.gz": gz(line(2, "audit", "2026-01-04T01:00:00Z"), line(1, "audit", "2026-01-04T01:00:00Z")),
		"audit-audit-20260105-1-2.ndjson.gz": gz(line(1, "security", "2026-01-05T01:00:00Z"), line(2, "security", "2026-01-05T01:00:00Z")),
		"audit-audit-20260106-1-2.ndjson.gz": gz(line(1, "audit", "2026-01-07T01:00:00Z"), line(2, "audit", "2026-01-07T01:00:00Z")),
		"audit-audit-20260107-1-2.ndjson.gz": gz("not json\n"),
		"audit-audit-20260108-1-2.ndjson.gz": []byte("plain text"),
		"audit-audit-20260109-1-2.ndjson.gz": gz(),
	}
	for file, data := range cases {
		write(file, data)
	}
	results, err := VerifyDir(dir)
	if err != nil || len(results) != len(cases) {
		t.Fatalf("results %d err %v", len(results), err)
	}
	for _, r := range results {
		if r.Err == "" {
			t.Errorf("%s verified", r.Name)
		}
	}
}

func TestVerifyAcceptsWholeFileAndTruncatedIsDamaged(t *testing.T) {
	a, dir := open(t, Options{})
	if _, err := a.Archive(context.Background(), []domain.AuditRecord{record(1, "audit", day("2026-01-01")), record(2, "audit", day("2026-01-01"))}); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "audit-audit-20260101-1-2.ndjson.gz")
	if r, _ := VerifyDir(dir); len(r) != 1 || r[0].Err != "" || r[0].Rows != 2 {
		t.Fatalf("whole file: %+v", r)
	}
	data, _ := os.ReadFile(name)
	if err := os.WriteFile(name, data[:len(data)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if r, _ := VerifyDir(dir); len(r) != 1 || r[0].Err == "" {
		t.Fatalf("truncated file verified: %+v", r)
	}
}

func TestListDirMissing(t *testing.T) {
	if _, err := ListDir(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("missing directory listed")
	}
}

func TestParseName(t *testing.T) {
	for _, bad := range []string{"audit-audit-20261301-1-2.ndjson.gz", "audit-other-20260101-1-2.ndjson.gz", "audit-audit-20260101-3-2.ndjson.gz", "audit-audit-20260101-0-2.ndjson.gz", "../audit-audit-20260101-1-2.ndjson.gz", "audit-audit-20260101-1-2.ndjson"} {
		if _, ok := parseName(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
