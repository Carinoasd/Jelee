package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestConsistencyCheckCatalogue(t *testing.T) {
	all := ConsistencyChecks()
	seen := map[string]bool{}
	for _, c := range all {
		if !ValidConsistencyCheck(c) || seen[c] {
			t.Fatalf("check %q invalid or repeated", c)
		}
		seen[c] = true
	}
	library, global := ConsistencyLibraryChecks(), ConsistencyGlobalChecks()
	if len(library)+len(global) != len(all) {
		t.Fatal("library and global checks do not partition the catalogue")
	}
	for _, c := range append(library[:], global[:]...) {
		if !seen[c] {
			t.Fatalf("check %q outside the catalogue", c)
		}
		delete(seen, c)
	}
	if len(seen) != 0 || ValidConsistencyCheck("") || ValidConsistencyCheck("orphan") {
		t.Fatal("catalogue accepts unknown checks")
	}
	for _, c := range global {
		if ConsistencyBaselineCheck(c) {
			t.Fatalf("global check %s cannot depend on a library baseline", c)
		}
	}
	for _, c := range []string{ConsistencyVersionCount, ConsistencyWatchStats} {
		if ConsistencyBaselineCheck(c) {
			t.Fatalf("%s does not read the baseline", c)
		}
	}
}

func TestConsistencyUnconfirmedCodes(t *testing.T) {
	for code, want := range map[string]string{
		ConsistencySourceMissing:      ConsistencySourceUnconfirmed,
		ConsistencyImageSourceMissing: ConsistencyImageSourceUnconfirmed,
		ConsistencySidecarMissing:     ConsistencySidecarUnconfirmed,
		ConsistencyProbeCacheOrphan:   ConsistencyProbeCacheOrphan,
	} {
		if got := ConsistencyUnconfirmedCode(code); got != want {
			t.Fatalf("%s: %s", code, got)
		}
	}
}

// consistencySchema validates a decoded JSON value against the subset of
// JSON Schema docs/consistency.md uses: type, const, enum, required,
// properties, additionalProperties, items and local $ref.
func consistencySchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "consistency.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(data), "<!-- consistency-report-schema -->\n```json\n")
	if !ok {
		t.Fatal("docs/consistency.md lacks the report schema block")
	}
	block, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("unterminated schema block")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(block), &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateSchema(root, schema map[string]any, value any, at string) error {
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		target, ok := root["$defs"].(map[string]any)[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: unknown $ref %s", at, ref)
		}
		return validateSchema(root, target, value, at)
	}
	if want, ok := schema["const"]; ok && value != want {
		return fmt.Errorf("%s: %v is not %v", at, value, want)
	}
	if values, ok := schema["enum"].([]any); ok && !slices.Contains(values, value) {
		return fmt.Errorf("%s: %v outside the enum", at, value)
	}
	switch schema["type"] {
	case "integer":
		if n, ok := value.(float64); !ok || n != float64(int64(n)) {
			return fmt.Errorf("%s: not an integer", at)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: not a string", at)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: not a boolean", at)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: not an array", at)
		}
		for i, item := range items {
			if err := validateSchema(root, schema["items"].(map[string]any), item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: not an object", at)
		}
		for _, key := range asSlice(schema["required"]) {
			if _, ok := object[key.(string)]; !ok {
				return fmt.Errorf("%s: missing %s", at, key)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for key, member := range object {
			if property, ok := properties[key].(map[string]any); ok {
				if err := validateSchema(root, property, member, at+"."+key); err != nil {
					return err
				}
				continue
			}
			additional, ok := schema["additionalProperties"].(map[string]any)
			if !ok {
				return fmt.Errorf("%s: undocumented member %s", at, key)
			}
			if err := validateSchema(root, additional, member, at+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func TestConsistencyReportMatchesTheDocumentedSchema(t *testing.T) {
	schema := consistencySchema(t)
	id := "00000000-0000-4000-8000-000000000001"
	finding := ConsistencyFinding{Code: ConsistencyDailyCounterDrift, LibraryID: id, ItemID: id, SourceID: id, RootID: id, Path: "[redacted]", UserID: id, Day: "2026-10-01", Object: "t.i", Fixable: true,
		Expected: map[string]int64{"sessions": 1}, Actual: map[string]int64{"sessions": 2}, RelativePath: "private/path.mkv", Confirm: ConsistencyConfirmAbsent}
	check := ConsistencyCheckResult{Check: ConsistencyWatchStats, Status: ConsistencyStatusIncomplete, Reason: ConsistencyReasonUnverified, Examined: 3, Findings: 1, Fixable: 1, Fixed: 1,
		Info: map[string]int64{ConsistencyInfoBaselineStale: 1, ConsistencyInfoUnconfirmed: 1, ConsistencyInfoPending: 1, ConsistencyInfoUnverified: 1, ConsistencyInfoSampled: 1}, Samples: []ConsistencyFinding{finding}, SamplesTruncated: true}
	report := ConsistencyReport{Schema: ConsistencyReportSchema, RunID: id, Origin: ConsistencyOriginJob, JobID: id, LibraryID: id, Mode: ConsistencyModeFix, State: ConsistencyRunPartial,
		Libraries: []ConsistencyLibraryReport{{LibraryID: id, Baseline: ConsistencyBaseline{Available: true, Revision: 2, Entries: 3}, Checks: []ConsistencyCheckResult{check}}},
		Global:    []ConsistencyCheckResult{{Check: ConsistencyConstraints, Status: ConsistencyStatusOK, Samples: []ConsistencyFinding{}}}, Totals: ConsistencyTotals{Findings: 1, Fixable: 1, Fixed: 1},
		Limits: ConsistencyLimits{StatBudget: 1, StatsUsed: 1, WatchSample: 1, VariantSample: 1, SamplesPerCheck: 1, PageSize: 1}}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private/path.mkv") {
		t.Fatal("the private relative path is serialized")
	}
	var decoded any
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = validateSchema(schema, schema, decoded, "report"); err != nil {
		t.Fatal(err)
	}
	// Every enum value the code can produce is documented.
	for _, c := range ConsistencyChecks() {
		probe := report
		probe.Global = []ConsistencyCheckResult{{Check: c, Status: ConsistencyStatusOK, Samples: []ConsistencyFinding{}}}
		data, _ = json.Marshal(probe)
		_ = json.Unmarshal(data, &decoded)
		if err = validateSchema(schema, schema, decoded, "report"); err != nil {
			t.Fatal(err)
		}
	}
	// The validator can fail: an undocumented member and a wrong enum.
	decoded.(map[string]any)["extra"] = 1
	if validateSchema(schema, schema, decoded, "report") == nil {
		t.Fatal("an undocumented member passed")
	}
	delete(decoded.(map[string]any), "extra")
	decoded.(map[string]any)["state"] = "running"
	if validateSchema(schema, schema, decoded, "report") == nil {
		t.Fatal("an undocumented state passed")
	}
}
