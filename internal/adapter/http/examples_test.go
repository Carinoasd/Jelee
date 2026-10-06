package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/examples/go/walkthrough"
)

// G49.5: the examples under examples/ are part of the API contract. The Go
// example runs against a real server on PostgreSQL; the curl examples are
// checked against the OpenAPI document and, where sh, curl and jq exist,
// run against the same server.

const examplesRoot = "../../../examples"

// curlCommand finds one curl invocation: its --request method and the path
// of its "$JELEE_URL/..." target.
var (
	curlMethod = regexp.MustCompile(`--request ([A-Z]+)`)
	curlTarget = regexp.MustCompile(`"\$\{?JELEE_URL\}?(/[^"?]*)`)
	curlVar    = regexp.MustCompile(`^\$\{?[A-Z_]+\}?$`)
)

type curlCall struct{ file, method, path string }

// curlExampleCalls parses every curl call of examples/curl/*.sh. A command
// spans backslash-continued lines; path segments that are shell variables
// stand for path parameters.
func curlExampleCalls(t *testing.T) []curlCall {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(examplesRoot, "curl", "*.sh"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no curl examples found: %v", err)
	}
	var calls []curlCall
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(data), "\\\n", " ")
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || !strings.Contains(line, "curl ") {
				continue
			}
			method, target := curlMethod.FindStringSubmatch(line), curlTarget.FindStringSubmatch(line)
			if method == nil || target == nil {
				t.Errorf("%s: curl call without --request METHOD and a \"$JELEE_URL/...\" target: %s", filepath.Base(file), strings.TrimSpace(line))
				continue
			}
			calls = append(calls, curlCall{file: filepath.Base(file), method: method[1], path: target[1]})
		}
	}
	return calls
}

// documentedOperation reports whether method and a concrete example path
// match an operation of the document; {param} matches one variable segment.
func documentedOperation(paths map[string]any, method, path string) bool {
	segments := strings.Split(path, "/")
	for pattern, raw := range paths {
		if _, ok := raw.(map[string]any)[strings.ToLower(method)]; !ok {
			continue
		}
		want := strings.Split(pattern, "/")
		if len(want) != len(segments) {
			continue
		}
		match := true
		for i, segment := range want {
			if strings.HasPrefix(segment, "{") {
				match = match && curlVar.MatchString(segments[i])
			} else {
				match = match && segment == segments[i]
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestCurlExamplesUseDocumentedOperations(t *testing.T) {
	paths := Specification(ReferenceConfig())["paths"].(map[string]any)
	calls := curlExampleCalls(t)
	seen := map[string]bool{}
	for _, call := range calls {
		seen[call.method+" "+call.path] = true
		if !documentedOperation(paths, call.method, call.path) {
			t.Errorf("%s: %s %s is not an operation of api/openapi.json", call.file, call.method, call.path)
		}
	}
	// The set covers the walk of the Go example and the public discovery.
	for _, want := range []string{"POST /api/v1/auth/login", "GET /api/v1/users/me", "GET /api/v1/libraries", "GET /api/v1/users/$JELEE_USER_ID/libraries", "GET /api/v1/items", "GET /api/v1/items/$JELEE_ITEM_ID/details", "POST /api/v1/auth/logout", "GET /api/v1/system", "GET /api/v1/openapi.json"} {
		if !seen[want] {
			t.Errorf("curl examples lack %s", want)
		}
	}
	// Reverse check: the matcher rejects what the document does not have.
	for _, bad := range [][2]string{{"GET", "/api/v1/nope"}, {"DELETE", "/api/v1/system"}, {"GET", "/api/v1/items/literal/details"}} {
		if documentedOperation(paths, bad[0], bad[1]) {
			t.Errorf("matcher accepted undocumented %s %s", bad[0], bad[1])
		}
	}
}

// literalSecret finds a password or token given a literal value.
var literalSecret = regexp.MustCompile(`(?i)(password|token)["']?\s*[:=]\s*["'][^"'$\s]`)

func TestExamplesCarryNoCredentials(t *testing.T) {
	var files []string
	err := filepath.WalkDir(examplesRoot, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("walk examples: %v", err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if match := literalSecret.Find(data); match != nil {
			t.Errorf("%s: literal credential %q; read it from the environment", file, match)
		}
	}
	// Reverse check of the pattern itself.
	for _, sample := range []string{`JELEE_PASSWORD="hunter2"`, `{"password":"x"}`, `token = 'abc'`} {
		if !literalSecret.MatchString(sample) {
			t.Errorf("credential pattern misses %s", sample)
		}
	}
	for _, sample := range []string{`password: cfg.Password`, `: "${JELEE_PASSWORD:?set JELEE_PASSWORD}"`, `"password": env.JELEE_PASSWORD`} {
		if literalSecret.MatchString(sample) {
			t.Errorf("credential pattern flags %s", sample)
		}
	}
}

// TestGoExampleWalkthroughPostgres runs examples/go against a real server on
// the access leak fixture: the viewer sees its granted library only, the
// administrator lists every library, and both sessions end revoked.
func TestGoExampleWalkthroughPostgres(t *testing.T) {
	ctx, store, dsn := leakStore(t)
	f := leakFixture(t, ctx, store)
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	// A per-run password checked by the password stub; nothing is stored in
	// the repository.
	secret := hex.EncodeToString(random[:])
	for _, name := range []string{"leak-viewer", "leak-admin"} {
		if _, err := store.SetLocalPassword(ctx, name, "$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo"); err != nil {
			t.Fatal(err)
		}
	}
	passwords := &httpAccountPasswords{verify: func(_ context.Context, password, _ string) (bool, error) { return password == secret, nil }}
	cfg := leakConfig(t, dsn, 0)
	cfg.Accounts.LoginUserLimit = 100
	server := httptest.NewServer(leakHandlerWith(t, store, cfg, passwords))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	activeSessions := func(name string) int {
		t.Helper()
		var n int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.name=$1 AND s.revoked_at IS NULL AND s.client_kind='web'`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	var report bytes.Buffer
	viewer, err := walkthrough.Run(runCtx, walkthrough.Config{BaseURL: server.URL, Name: "leak-viewer", Password: secret, Client: server.Client()}, &report)
	if err != nil {
		t.Fatalf("viewer walk: %v\n%s", err, report.String())
	}
	if viewer.Admin || viewer.UserID != f.viewer || len(viewer.Libraries) != 1 || viewer.Libraries[0].ID != f.visibleLibrary ||
		len(viewer.Items) != 1 || viewer.Items[0].ID != f.visibleItem || viewer.TotalItems != 1 || viewer.Details == nil || viewer.Details.ID != f.visibleItem || viewer.Details.Title != "Visible Leak Probe Title" {
		t.Fatalf("viewer walk result: %+v", viewer)
	}
	if leakContainsAny(report.String(), f.markers...) {
		t.Fatalf("viewer report shows hidden content:\n%s", report.String())
	}
	if !strings.Contains(report.String(), "Visible Leak Probe Title") {
		t.Fatalf("viewer report:\n%s", report.String())
	}

	report.Reset()
	admin, err := walkthrough.Run(runCtx, walkthrough.Config{BaseURL: server.URL + "/", Name: "leak-admin", Password: secret, Client: server.Client(), ItemLimit: 5}, &report)
	if err != nil {
		t.Fatalf("administrator walk: %v\n%s", err, report.String())
	}
	if !admin.Admin || len(admin.Libraries) != 2 || len(admin.Items) != 1 || admin.Details == nil {
		t.Fatalf("administrator walk result: %+v", admin)
	}
	if activeSessions("leak-viewer") != 0 || activeSessions("leak-admin") != 0 {
		t.Fatal("example left a web session active")
	}

	// A wrong password surfaces the error envelope.
	_, err = walkthrough.Run(runCtx, walkthrough.Config{BaseURL: server.URL, Name: "leak-viewer", Password: secret + "x", Client: server.Client()}, nil)
	var apiErr *walkthrough.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Code == "" || apiErr.TraceID == "" {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err = walkthrough.Run(runCtx, walkthrough.Config{BaseURL: "ftp://example.invalid", Name: "a", Password: "b"}, nil); err == nil {
		t.Fatal("non-HTTP base URL accepted")
	}

	runCurlExamples(t, server.URL, secret, f)
}

// runCurlExamples runs the curl scripts against the test server when sh,
// curl and jq are installed (Linux CI images have them); otherwise the
// static check above is the whole coverage.
func runCurlExamples(t *testing.T, base, secret string, f leakIDs) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Log("curl examples NOT RUN: POSIX shell scripts")
		return
	}
	for _, tool := range []string{"sh", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Logf("curl examples NOT RUN: %s not installed", tool)
			return
		}
	}
	dir, err := filepath.Abs(filepath.Join(examplesRoot, "curl"))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "JELEE_URL=" + base}
	script := func(name string, extra ...string) string {
		t.Helper()
		cmd := exec.Command("sh", filepath.Join(dir, name))
		cmd.Dir, cmd.Env = work, append(append([]string{}, env...), extra...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s%s", name, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	if out := script("system.sh"); !strings.Contains(out, `"name":"Jelee"`) {
		t.Fatalf("system.sh: %s", out)
	}
	script("openapi.sh")
	if info, err := os.Stat(filepath.Join(work, "openapi.json")); err != nil || info.Size() == 0 {
		t.Fatal("openapi.sh wrote no document")
	}
	token := strings.TrimSpace(script("login.sh", "JELEE_USER=leak-viewer", "JELEE_PASSWORD="+secret))
	if len(token) != 43 {
		t.Fatalf("login.sh printed %d bytes, want a token", len(token))
	}
	auth := "JELEE_TOKEN=" + token
	if out := script("me.sh", auth); !strings.Contains(out, f.viewer) {
		t.Fatalf("me.sh: %s", out)
	}
	if out := script("library-grants.sh", auth); !strings.Contains(out, f.visibleLibrary) || leakContainsAny(out, f.markers...) {
		t.Fatalf("library-grants.sh: %s", out)
	}
	if out := script("items.sh", auth, "JELEE_LIBRARY_ID="+f.visibleLibrary); !strings.Contains(out, f.visibleItem) {
		t.Fatalf("items.sh: %s", out)
	}
	if out := script("item-details.sh", auth, "JELEE_ITEM_ID="+f.visibleItem); !strings.Contains(out, "Visible Leak Probe Title") {
		t.Fatalf("item-details.sh: %s", out)
	}
	script("logout.sh", auth)
	// The revoked token no longer works.
	cmd := exec.Command("sh", filepath.Join(dir, "me.sh"))
	cmd.Dir, cmd.Env = work, append(append([]string{}, env...), auth)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "authentication_required") {
		t.Fatalf("me.sh after logout: %v %s", err, out)
	}
	ran := []string{"system", "openapi", "login", "me", "library-grants", "items", "item-details", "logout"}
	sort.Strings(ran)
	t.Logf("curl examples ran: %s", strings.Join(ran, ", "))
}
