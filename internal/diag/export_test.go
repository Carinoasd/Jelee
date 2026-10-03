package diag

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

type leakFixture struct {
	env      Environment
	db       *fakeDB
	secrets  []string
	logPath  string
	mediaDir string
}

const (
	testBearer  = "jelee-access-token-9f8e7d6c5b4a3210"
	testWebhook = "whsec_Q2xhdWRlU2VjcmV0V2ViaG9vaw"
	testPlainPw = "Hunter2!Passw0rd"
)

// newLeakFixture injects a token, a DSN, passwords and absolute paths into
// the configuration, the environment, the database and the log file.
func newLeakFixture(t *testing.T) leakFixture {
	t.Helper()
	env, db := healthyEnv(t)
	base := t.TempDir()
	media := filepath.Join(base, "Private Collection")
	imgTemp := filepath.Join(base, "image-temp")
	logDir := filepath.Join(base, "logs")
	for _, dir := range []string{media, imgTemp, logDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(logDir, "jelee.log")
	now := env.Now()
	var log strings.Builder
	write := func(at time.Time, extra string) {
		fmt.Fprintf(&log, `{"time":%q,"level":"INFO","msg":"request done","component":"http","status":200%s}`+"\n", at.Format(time.RFC3339Nano), extra)
	}
	write(now.Add(-48*time.Hour), `,"event":"too_old"`)
	write(now.Add(-time.Hour), fmt.Sprintf(`,"authorization":"Bearer %s","dsn":%q,"path":%q,"password":%q,"webhookSecret":%q,"clientIp":"203.0.113.77"`,
		testBearer, testDSN, filepath.Join(media, "secret-movie.mkv"), testPlainPw, testWebhook))
	log.WriteString("not json " + testDSN + "\n")
	write(now.Add(-time.Minute), `,"nested":{"token":"`+testBearer+`","durationMs":5},"`+filepath.ToSlash(media)+`":1`)
	write(now.Add(-time.Second), `,"event":"latest_record"`)
	if err := os.WriteFile(logPath, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	env.Config.TMDBAPIKey = testTMDBKey
	env.Config.Images.TempRoot = imgTemp
	env.Config.Logging.Output, env.Config.Logging.File.Path = "both", logPath
	env.Config.Logging.PathRoots = []string{media}
	env.Lookup = lookupMap(map[string]string{"JELEE_DATABASE_URL": testDSN, "TMDB_API_KEY": testTMDBKey, "JELEE_LOG_FILE": logPath})
	db.roots = []Root{{ID: "8a1c6f0e-0000-4000-8000-000000000001", LibraryID: "lib-1", Path: media}}
	db.tables = []TableStat{{Name: "items", EstimatedRows: 12, TotalBytes: 8192}, {Name: "jobs", EstimatedRows: 3, TotalBytes: 4096}}
	db.jobs = []JobGroup{{Kind: "inventory_scan", State: "failed", ErrorCode: "scan_io", Count: 2, Latest: now.Add(-time.Hour)}}
	secrets := []string{testPassword, testDBHost, testDSN, "postgres://", testTMDBKey, testBearer, testWebhook, testPlainPw, "Hunter2", "203.0.113.77",
		base, filepath.ToSlash(base), media, imgTemp, logPath, "Private Collection", "secret-movie"}
	return leakFixture{env: env, db: db, secrets: secrets, logPath: logPath, mediaDir: media}
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(data)
	}
	return out
}

func TestExportBundleContentsAndRedaction(t *testing.T) {
	fx := newLeakFixture(t)
	out := filepath.Join(t.TempDir(), "bundle.zip")
	if err := NewSession(fx.env).Export(context.Background(), ExportOptions{Out: out}); err != nil {
		t.Fatalf("export: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode = %v", info.Mode().Perm())
	}
	files := readZip(t, out)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{"config.json", "db-stats.json", "doctor.json", "jobs.json", "logs.jsonl", "manifest.json"}
	if !slices.Equal(names, want) {
		t.Fatalf("bundle files = %v", names)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		for _, secret := range fx.secrets {
			if strings.Contains(body, secret) || strings.Contains(name, secret) || strings.Contains(string(raw), secret) {
				t.Fatalf("%s leaked %q", name, secret)
			}
		}
	}
	logs := files["logs.jsonl"]
	if strings.Contains(logs, "too_old") || !strings.Contains(logs, "latest_record") || !strings.Contains(logs, `"[redacted]"`) || strings.Count(logs, "\n") != 3 {
		t.Fatalf("logs.jsonl = %s", logs)
	}
	if !strings.Contains(files["db-stats.json"], `"items"`) || !strings.Contains(files["jobs.json"], `"scan_io"`) {
		t.Fatalf("stats = %s %s", files["db-stats.json"], files["jobs.json"])
	}
	for _, s := range []string{`"databaseConfigured": true`, `"tmdbConfigured": true`, `"tempRootConfigured": true`} {
		if !strings.Contains(files["config.json"], s) {
			t.Fatalf("config.json lacks %s: %s", s, files["config.json"])
		}
	}
	if !strings.Contains(files["manifest.json"], "not_collected_requires_developer_mode") || !strings.Contains(files["doctor.json"], `"library_roots"`) {
		t.Fatalf("manifest/doctor = %s", files["manifest.json"])
	}
}

func TestExportLogBounds(t *testing.T) {
	env, _ := healthyEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "jelee.log")
	var b strings.Builder
	now := env.Now()
	for i := 0; i < 2000; i++ {
		if i == 1900 {
			// An overlong record inside the scanned tail is dropped whole.
			b.WriteString(`{"time":"` + now.Format(time.RFC3339Nano) + `","msg":"` + strings.Repeat("x", maxLogLineBytes+10) + `"}` + "\n")
		}
		fmt.Fprintf(&b, `{"time":%q,"level":"INFO","msg":"tick","count":%d}`+"\n", now.Add(time.Duration(i-2000)*time.Second).Format(time.RFC3339Nano), i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	env.Config.Logging.Output, env.Config.Logging.File.Path = "file", path
	out := filepath.Join(t.TempDir(), "b.zip")
	if err := NewSession(env).Export(context.Background(), ExportOptions{Out: out, Since: 10 * time.Minute, MaxLogBytes: 16384}); err != nil {
		t.Fatal(err)
	}
	logs := readZip(t, out)["logs.jsonl"]
	if len(logs) > 16384 || len(logs) < 8192 || !strings.Contains(logs, `"count":1999`) || strings.Contains(logs, "xxxx") {
		t.Fatalf("log tail size %d: %.200s", len(logs), logs)
	}
	// 600 records fall inside the window; the cap keeps only the newest.
	if strings.Contains(logs, `"count":1399`) {
		t.Fatal("record outside the window kept")
	}
}

func TestExportWithoutDatabaseOrLogs(t *testing.T) {
	env, _ := healthyEnv(t)
	env.OpenDB = func(context.Context, string) (Database, error) { return nil, errors.New("down") }
	out := filepath.Join(t.TempDir(), "b.zip")
	if err := NewSession(env).Export(context.Background(), ExportOptions{Out: out}); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, out)
	if _, ok := files["db-stats.json"]; ok {
		t.Fatal("db stats without database")
	}
	if !strings.Contains(files["manifest.json"], "database_unavailable") || !strings.Contains(files["manifest.json"], "no_log_file_configured") {
		t.Fatalf("manifest = %s", files["manifest.json"])
	}
}

func exportCode(err error) string {
	var e *ExportError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestExportRefusesUnsafeOutputs(t *testing.T) {
	env, _ := healthyEnv(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.zip")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := exportCode(NewSession(env).Export(context.Background(), ExportOptions{Out: existing})); code != CodeExportExists {
		t.Fatalf("existing output: %q", code)
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Fatal("existing file modified")
	}
	if code := exportCode(NewSession(env).Export(context.Background(), ExportOptions{Out: filepath.Join(dir, "x.txt")})); code != CodeExportPath {
		t.Fatalf("non-zip output: %q", code)
	}
	if code := exportCode(NewSession(env).Export(context.Background(), ExportOptions{Out: filepath.Join(dir, "missing", "x.zip")})); code != CodeExportFailed {
		t.Fatalf("missing directory: %q", code)
	}
}

// TestExportDeletesLeakingBundle proves the post-write scan: content that
// slipped past redaction makes the export fail and removes the file.
func TestExportDeletesLeakingBundle(t *testing.T) {
	fx := newLeakFixture(t)
	for _, leak := range []string{testDSN, testTMDBKey, fx.mediaDir, "Authorization: Bearer " + testBearer, `{"password=` + testPlainPw + `"}`, `"C:\\Users\\someone\\Videos"`} {
		collectHook = func(entries []bundleEntry) []bundleEntry {
			return append(entries, bundleEntry{"extra.txt", []byte("value " + leak)})
		}
		out := filepath.Join(t.TempDir(), "leak.zip")
		err := NewSession(fx.env).Export(context.Background(), ExportOptions{Out: out})
		collectHook = nil
		if exportCode(err) != CodeExportFound {
			t.Fatalf("leak %q: err = %v", leak, err)
		}
		if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("leaking bundle kept for %q", leak)
		}
	}
}

func TestVerifyBundleScansNamesAndCorruptArchives(t *testing.T) {
	dir := t.TempDir()
	scanner := &Scanner{}
	scanner.Add(testPassword)
	named := filepath.Join(dir, "named.zip")
	f, err := os.Create(named)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if _, err := zw.Create(testPassword + ".txt"); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()
	if exportCode(VerifyBundle(named, scanner)) != CodeExportFound {
		t.Fatal("secret entry name not detected")
	}
	corrupt := filepath.Join(dir, "corrupt.zip")
	if err := os.WriteFile(corrupt, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exportCode(VerifyBundle(corrupt, scanner)) != CodeExportFailed {
		t.Fatal("corrupt archive accepted")
	}
	for _, p := range []string{named, corrupt} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("rejected bundle kept")
		}
	}
}

func TestScannerPatterns(t *testing.T) {
	s := &Scanner{}
	for _, dirty := range []string{
		"postgresql://x", "https://user:pw@host/x", "password=abc123", "Bearer abcdefghijkl", "api_key=0123456789abcdef",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJqZWxlZSJ9", `"/srv/media/a.mkv"`, `path=/home/u/x`, `C:\Users\x`, `"C:\\Users\\x"`, `\\server\share`,
	} {
		if s.Clean([]byte(dirty)) {
			t.Errorf("not detected: %s", dirty)
		}
	}
	for _, clean := range []string{
		"docs/troubleshooting.md", "Run: jelee-migrate up", "IPv4 /8 or narrower, IPv6 /16", `"time":"2026-10-04T12:00:00Z"`, "[redacted]", "api.themoviedb.org",
		"(scripts/make.ps1 bootstrap-media on Windows)", "chmod 1777 on the temporary directory",
	} {
		if !s.Clean([]byte(clean)) {
			t.Errorf("false positive: %s", clean)
		}
	}
	every := ""
	for _, code := range Codes() {
		every += code + " " + codes[code].Message + " " + codes[code].Fix + "\n"
	}
	if !s.Clean([]byte(every)) {
		t.Fatal("fixed code text trips the scanner")
	}
}

func TestScannerNeedlesFromDSN(t *testing.T) {
	s := &Scanner{}
	s.AddDSN("host=db.secret.example port=5432 user=jelee password='kv-Secret-99' dbname=jelee")
	s.AddDSN("postgres://jelee:p%40ss-word9@127.0.0.1/jelee")
	for _, leaked := range []string{"db.secret.example", "kv-Secret-99", "p@ss-word9"} {
		if s.Clean([]byte("x " + leaked + " y")) {
			t.Errorf("needle %q missing", leaked)
		}
	}
	if !s.Clean([]byte("jelee 127.0.0.1")) {
		t.Fatal("loopback host or user name treated as secret")
	}
}
