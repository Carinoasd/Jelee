package telemetry

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
)

// The default alert rules (G50.6) live in deploy/prometheus/jelee-alerts.yml.
// promtool is not part of the pinned toolchain, so this test parses the
// file's YAML subset strictly and checks what promtool cannot: every metric
// an expression names is exposed by the production /metrics families, and
// every alert has a complete runbook section.

type alertRule struct {
	group, alert, expr, duration string
	labels, annotations          map[string]string
	line                         int
}

var alertKeyLine = regexp.MustCompile(`^( *)(- )?([a-z_]+):(?: (.*))?$`)

// parseAlertRules reads the subset jelee-alerts.yml is written in: a
// "groups" list of {name, rules}, each rule {alert, expr, for, labels,
// annotations}, scalars plain or double-quoted, and "|" block scalars for
// expressions. Anything else is an error, so the file cannot drift into
// syntax this check does not understand.
func parseAlertRules(text string) ([]alertRule, error) {
	var rules []alertRule
	group := ""
	section := ""
	var current *alertRule
	block := false
	scanner := bufio.NewScanner(strings.NewReader(text))
	line := 0
	scalar := func(raw string) (string, error) {
		if strings.HasPrefix(raw, `"`) {
			return strconv.Unquote(raw)
		}
		// YAML plain scalars cannot start with an indicator or contain
		// ": " or " #"; such values must be quoted.
		if raw == "" || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(raw[0])) || strings.Contains(raw, ": ") || strings.Contains(raw, " #") {
			return "", fmt.Errorf("line %d: plain scalar %q needs quotes", line, raw)
		}
		return raw, nil
	}
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.Contains(text, "\t") {
			return nil, fmt.Errorf("line %d: tab", line)
		}
		trimmed := strings.TrimSpace(text)
		indent := len(text) - len(strings.TrimLeft(text, " "))
		if block {
			if trimmed != "" && indent >= 10 {
				current.expr += strings.TrimSpace(text) + "\n"
				continue
			}
			block = false
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := alertKeyLine.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("line %d: unsupported syntax %q", line, text)
		}
		level, item, key, value := len(m[1]), m[2] != "", m[3], m[4]
		switch {
		case level == 0 && key == "groups" && !item && value == "":
		case level == 2 && item && key == "name":
			group, current, section = value, nil, ""
		case level == 4 && key == "rules" && !item && value == "" && group != "":
		case level == 6 && item && key == "alert" && group != "":
			rules = append(rules, alertRule{group: group, alert: value, labels: map[string]string{}, annotations: map[string]string{}, line: line})
			current, section = &rules[len(rules)-1], ""
		case level == 8 && !item && current != nil && (key == "labels" || key == "annotations") && value == "":
			section = key
		case level == 8 && !item && current != nil && key == "expr":
			if value == "|" {
				block = true
				continue
			}
			v, err := scalar(value)
			if err != nil {
				return nil, err
			}
			current.expr = v
		case level == 8 && !item && current != nil && key == "for":
			current.duration = value
		case level == 10 && !item && current != nil && section != "":
			v, err := scalar(value)
			if err != nil {
				return nil, err
			}
			target := current.labels
			if section == "annotations" {
				target = current.annotations
			}
			if _, dup := target[key]; dup {
				return nil, fmt.Errorf("line %d: duplicate %s", line, key)
			}
			target[key] = v
		default:
			return nil, fmt.Errorf("line %d: unexpected key %q at indentation %d", line, key, level)
		}
	}
	return rules, scanner.Err()
}

// promqlWords are the functions, operators and keywords the rules may use.
var promqlWords = map[string]bool{
	"and": true, "or": true, "unless": true, "by": true, "without": true, "on": true, "ignoring": true, "bool": true,
	"sum": true, "max": true, "min": true, "avg": true, "count": true, "rate": true, "increase": true, "delta": true, "deriv": true,
	"predict_linear": true, "time": true, "abs": true, "clamp_min": true, "max_over_time": true, "min_over_time": true, "avg_over_time": true,
}

var (
	promqlQuoted   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	promqlMatchers = regexp.MustCompile(`\{[^}]*\}`)
	promqlRanges   = regexp.MustCompile(`\[[^\]]*\]`)
	promqlGrouping = regexp.MustCompile(`\b(by|without|on|ignoring)\s*\(([^)]*)\)`)
	promqlWord     = regexp.MustCompile(`[A-Za-z_:][A-Za-z0-9_:]*`)
	promqlNumber   = regexp.MustCompile(`(^|[^A-Za-z0-9_:.])[0-9]+(\.[0-9]+)?(e[0-9]+)?`)
	templateLabel  = regexp.MustCompile(`\$labels\.([a-z_]+)`)
)

// promqlMetrics returns the metric names of an expression and the labels its
// grouping clauses name.
func promqlMetrics(expr string) (metrics, labels []string, err error) {
	depth := map[rune]int{}
	for _, r := range promqlQuoted.ReplaceAllString(expr, `""`) {
		switch r {
		case '(', '{', '[':
			depth[r]++
		case ')':
			depth['(']--
		case '}':
			depth['{']--
		case ']':
			depth['[']--
		}
		for _, d := range depth {
			if d < 0 {
				return nil, nil, fmt.Errorf("unbalanced brackets in %q", expr)
			}
		}
	}
	for open, d := range depth {
		if d != 0 {
			return nil, nil, fmt.Errorf("unclosed %q in %q", open, expr)
		}
	}
	stripped := promqlQuoted.ReplaceAllString(expr, `""`)
	stripped = promqlMatchers.ReplaceAllString(stripped, " ")
	stripped = promqlRanges.ReplaceAllString(stripped, " ")
	for _, g := range promqlGrouping.FindAllStringSubmatch(stripped, -1) {
		for _, label := range strings.Split(g[2], ",") {
			if label = strings.TrimSpace(label); label != "" {
				labels = append(labels, label)
			}
		}
	}
	stripped = promqlGrouping.ReplaceAllString(stripped, "$1 ")
	stripped = promqlNumber.ReplaceAllString(stripped, "$1 ")
	for _, word := range promqlWord.FindAllString(stripped, -1) {
		if promqlWords[word] {
			continue
		}
		metrics = append(metrics, word)
	}
	if len(metrics) == 0 {
		return nil, nil, fmt.Errorf("expression %q names no metric", expr)
	}
	return metrics, labels, nil
}

func exposedFamily(families map[string]*dto.MetricFamily, name string) *dto.MetricFamily {
	if family := families[name]; family != nil {
		return family
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if family := families[strings.TrimSuffix(name, suffix)]; strings.HasSuffix(name, suffix) && family != nil && family.GetType() == dto.MetricType_HISTOGRAM {
			return family
		}
	}
	return nil
}

func familyHasLabel(family *dto.MetricFamily, label string) bool {
	for _, point := range family.Metric {
		for _, l := range point.Label {
			if l.GetName() == label {
				return true
			}
		}
	}
	return false
}

// validateAlertRules returns every problem of rules against the exposed
// families and the runbook text.
func validateAlertRules(rules []alertRule, families map[string]*dto.MetricFamily, runbook string) []error {
	var problems []error
	fail := func(r alertRule, format string, args ...any) {
		problems = append(problems, fmt.Errorf("line %d %s: %s", r.line, r.alert, fmt.Sprintf(format, args...)))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if !regexp.MustCompile(`^Jelee[A-Z][A-Za-z0-9]+$`).MatchString(r.alert) || seen[r.alert] {
			fail(r, "alert names must be unique Jelee CamelCase")
		}
		seen[r.alert] = true
		if r.duration != "" {
			if _, err := model.ParseDuration(r.duration); err != nil {
				fail(r, "invalid for duration %q", r.duration)
			}
		}
		if severity := r.labels["severity"]; severity != "info" && severity != "warning" && severity != "critical" || len(r.labels) != 1 {
			fail(r, "labels must be exactly a severity of info, warning or critical")
		}
		anchor := strings.ToLower(r.alert)
		if r.annotations["summary"] == "" || r.annotations["description"] == "" || r.annotations["runbook"] != "docs/runbook.md#"+anchor || len(r.annotations) != 3 {
			fail(r, "annotations must be summary, description and runbook docs/runbook.md#%s", anchor)
		}
		metrics, labels, err := promqlMetrics(r.expr)
		if err != nil {
			fail(r, "%v", err)
			continue
		}
		var exposed []*dto.MetricFamily
		for _, name := range metrics {
			if name == "up" {
				// Prometheus synthesizes up for every scrape target.
				continue
			}
			family := exposedFamily(families, name)
			if family == nil {
				fail(r, "metric %s is not exposed by /metrics", name)
				continue
			}
			exposed = append(exposed, family)
		}
		templated := templateLabel.FindAllStringSubmatch(r.annotations["summary"]+r.annotations["description"], -1)
		for _, m := range templated {
			labels = append(labels, m[1])
		}
		for _, label := range labels {
			if label == "instance" || label == "job" {
				continue
			}
			found := false
			for _, family := range exposed {
				found = found || familyHasLabel(family, label)
			}
			if !found {
				fail(r, "label %s is not a label of the metrics it uses", label)
			}
		}
		section, ok := runbookSection(runbook, r.alert)
		if !ok {
			fail(r, "docs/runbook.md lacks a ### %s section", r.alert)
			continue
		}
		for _, part := range []string{"**意義**", "**確認**", "**處置**", "**回復驗證**"} {
			if !strings.Contains(section, part) {
				fail(r, "runbook section lacks %s", part)
			}
		}
	}
	return problems
}

func runbookSection(runbook, alert string) (string, bool) {
	_, rest, ok := strings.Cut(runbook, "\n### "+alert+"\n")
	if !ok {
		return "", false
	}
	if end := strings.Index(rest, "\n#"); end >= 0 {
		rest = rest[:end]
	}
	return rest, true
}

func repositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	data, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(file), "..", "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDefaultAlertRulesUseExposedMetricsAndHaveRunbooks(t *testing.T) {
	rules, err := parseAlertRules(repositoryFile(t, "deploy", "prometheus", "jelee-alerts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	families := scrapeOps(t, newProductionTestExporter(t, &opsTestSource{snapshot: opsTestSnapshot()}, &blockedTestSource{}))
	for _, problem := range validateAlertRules(rules, families, repositoryFile(t, "docs", "runbook.md")) {
		t.Error(problem)
	}
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		names = append(names, r.alert)
	}
	// G50.6 names these alerts; G47.8 the bulk refusal one.
	for _, required := range []string{"JeleeDiskSpaceLow", "JeleeDiskSpaceCritical", "JeleeScrapeFailed", "JeleeScanConsecutiveFailures", "JeleeWebhookDeadLetters",
		"JeleeClientBlockBurst", "JeleeMemoryNearLimit", "JeleeDevModeActive", "JeleeConsistencyFindings"} {
		if !slices.Contains(names, required) {
			t.Errorf("default rules lack %s", required)
		}
	}
	t.Logf("%d alert rules checked against %d exposed families", len(rules), len(families))
}

// The checks above must be able to fail: a misspelled metric, an unknown
// label, a missing runbook and malformed YAML are each reported.
func TestAlertRuleValidationRejectsBrokenRules(t *testing.T) {
	families := scrapeOps(t, newProductionTestExporter(t, &opsTestSource{snapshot: opsTestSnapshot()}, &blockedTestSource{}))
	runbook := "# Runbook\n\n### JeleeGood\n\n**意義** a\n**確認** b\n**處置** c\n**回復驗證** d\n"
	good := alertRule{alert: "JeleeGood", expr: "max by (check) (jelee_consistency_findings) > 0", duration: "5m",
		labels: map[string]string{"severity": "warning"}, annotations: map[string]string{"summary": "s {{ $labels.check }}", "description": "d", "runbook": "docs/runbook.md#jeleegood"}}
	if problems := validateAlertRules([]alertRule{good}, families, runbook); len(problems) != 0 {
		t.Fatal(problems)
	}
	for name, mutate := range map[string]func(*alertRule){
		"misspelled metric": func(r *alertRule) { r.expr = "max(jelee_consistency_finding) > 0" },
		"unknown label":     func(r *alertRule) { r.expr = "max by (volume) (jelee_consistency_findings) > 0" },
		"template label":    func(r *alertRule) { r.annotations["summary"] = "{{ $labels.volume }}" },
		"unbalanced":        func(r *alertRule) { r.expr = "max(jelee_consistency_findings > 0" },
		"no metric":         func(r *alertRule) { r.expr = "1 > 0" },
		"duration":          func(r *alertRule) { r.duration = "5 minutes" },
		"severity":          func(r *alertRule) { r.labels["severity"] = "page" },
		"runbook link":      func(r *alertRule) { r.annotations["runbook"] = "docs/runbook.md" },
		"runbook section": func(r *alertRule) {
			r.alert = "JeleeMissing"
			r.annotations["runbook"] = "docs/runbook.md#jeleemissing"
		},
		"name": func(r *alertRule) { r.alert = "diskFull" },
	} {
		r := good
		r.labels, r.annotations = map[string]string{"severity": "warning"}, map[string]string{}
		for k, v := range good.annotations {
			r.annotations[k] = v
		}
		mutate(&r)
		if problems := validateAlertRules([]alertRule{r}, families, runbook); len(problems) == 0 {
			t.Errorf("%s passed validation", name)
		}
	}
	if problems := validateAlertRules([]alertRule{good}, families, strings.Replace(runbook, "**回復驗證** d\n", "", 1)); len(problems) == 0 {
		t.Error("a runbook section without recovery verification passed")
	}
	for name, text := range map[string]string{
		"tab":          "groups:\n\t- name: x\n",
		"unknown key":  "groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        severity: warning\n",
		"bad scalar":   "groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        labels:\n          a: {y}\n",
		"comment":      "groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        labels:\n          a: b #c\n",
		"duplicate":    "groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        labels:\n          a: b\n          a: c\n",
		"flow mapping": "groups: [ ]\n",
		"bad quote":    "groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        expr: \"up\n",
	} {
		if _, err := parseAlertRules(text); err == nil {
			t.Errorf("parser accepted %s", name)
		}
	}
	rules, err := parseAlertRules("groups:\n  - name: g\n    rules:\n      - alert: JeleeX\n        expr: |\n          max(jelee_scan_consecutive_failures)\n          >= 3\n        for: 1m\n")
	if err != nil || len(rules) != 1 || rules[0].expr != "max(jelee_scan_consecutive_failures)\n>= 3\n" || rules[0].duration != "1m" {
		t.Fatalf("block scalar: %+v %v", rules, err)
	}
}
