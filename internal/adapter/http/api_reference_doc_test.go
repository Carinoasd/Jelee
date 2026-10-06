package httpapi

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
)

// G49.7 / G49.8: docs/api-reference.md carries the public error code table
// and the route groups. The table must equal errorCodeStatuses and the
// zh-CN message catalog, and the groups must cover every OpenAPI path with
// no stale prefix. Set JELEE_API_REFERENCE_UPDATE=1 to regenerate the table.

const (
	errorTableBegin = "<!-- error-codes:begin -->\n"
	errorTableEnd   = "<!-- error-codes:end -->"
	groupsBegin     = "<!-- api-groups:begin -->\n"
	groupsEnd       = "<!-- api-groups:end -->"
)

var groupPrefix = regexp.MustCompile("`(/[^`]*)`")

func apiReferencePath() string {
	return filepath.Join("..", "..", "..", "docs", "api-reference.md")
}

// errorCodeTable renders the table from the implementation.
func errorCodeTable() string {
	codes := make([]string, 0, len(errorCodeStatuses))
	for code := range errorCodeStatuses {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	var b strings.Builder
	b.WriteString("| 错误码 | HTTP 状态 | 默认消息（zh-CN） |\n| --- | --- | --- |\n")
	for _, code := range codes {
		statuses := make([]string, 0, len(errorCodeStatuses[code]))
		for _, s := range errorCodeStatuses[code] {
			statuses = append(statuses, strconv.Itoa(s))
		}
		message := strings.ReplaceAll(i18n.Message(code, "zh-CN", "—"), "|", "\\|")
		b.WriteString("| `" + code + "` | " + strings.Join(statuses, "、") + " | " + message + " |\n")
	}
	return b.String()
}

func section(t *testing.T, text, begin, end string) (string, string, string) {
	t.Helper()
	before, rest, ok := strings.Cut(text, begin)
	if !ok {
		t.Fatalf("docs/api-reference.md lacks %q", strings.TrimSpace(begin))
	}
	body, after, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatalf("docs/api-reference.md lacks %q", end)
	}
	return before, body, after
}

func TestAPIReferenceErrorCodeTableMatchesImplementation(t *testing.T) {
	data, err := os.ReadFile(apiReferencePath())
	if err != nil {
		t.Fatal(err)
	}
	before, body, after := section(t, string(data), errorTableBegin, errorTableEnd)
	want := errorCodeTable()
	if body == want {
		return
	}
	if os.Getenv("JELEE_API_REFERENCE_UPDATE") != "" {
		if err := os.WriteFile(apiReferencePath(), []byte(before+errorTableBegin+want+errorTableEnd+after), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("docs/api-reference.md error code table regenerated")
		return
	}
	got, wanted := strings.Split(body, "\n"), strings.Split(want, "\n")
	for _, line := range wanted {
		if !containsString(got, line) {
			t.Errorf("missing or different row: %s", line)
		}
	}
	for _, line := range got {
		if !containsString(wanted, line) {
			t.Errorf("row not in the implementation: %s", line)
		}
	}
	t.Error("error code table is stale; run JELEE_API_REFERENCE_UPDATE=1 go test -run TestAPIReferenceErrorCodeTable ./internal/adapter/http/")
}

func TestAPIReferenceGroupsCoverOpenAPI(t *testing.T) {
	data, err := os.ReadFile(apiReferencePath())
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := section(t, string(data), groupsBegin, groupsEnd)
	var prefixes []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ---") {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		for _, m := range groupPrefix.FindAllStringSubmatch(first, -1) {
			prefixes = append(prefixes, m[1])
		}
	}
	if len(prefixes) < 10 {
		t.Fatalf("only %d group prefixes parsed", len(prefixes))
	}
	paths := Specification(leakConfig(t, "postgres://localhost/jelee", 0))["paths"].(map[string]any)
	used := map[string]bool{}
	for path := range paths {
		covered := false
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
				covered, used[p] = true, true
			}
		}
		if !covered {
			t.Errorf("OpenAPI path %s belongs to no documented group", path)
		}
	}
	for _, p := range prefixes {
		if !used[p] {
			t.Errorf("documented group prefix %s matches no OpenAPI path", p)
		}
	}
}
