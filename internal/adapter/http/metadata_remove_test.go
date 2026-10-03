package httpapi

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func TestMetadataRemoveSchemaIndependentOfTMDB(t *testing.T) {
	spec := Specification(config.Config{EnableAccounts: true})
	path, ok := spec["paths"].(map[string]any)["/api/v1/items/{id}/metadata/external"].(map[string]any)
	if !ok || len(path) != 1 || path["delete"] == nil {
		t.Fatal("external removal not documented without TMDB key")
	}
	op := path["delete"].(map[string]any)
	if op["requestBody"] != nil || len(op["security"].([]any)) != 1 {
		t.Fatal("DELETE documented with body or without bearer")
	}
	params := op["parameters"].([]any)
	if len(params) != 2 || params[1].(map[string]any)["name"] != "expectedRevision" || params[1].(map[string]any)["required"] != true {
		t.Fatal("revision precondition missing")
	}
	for _, status := range []string{"200", "400", "401", "403", "404", "409"} {
		if op["responses"].(map[string]any)[status] == nil {
			t.Fatal("status not documented", status)
		}
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["MetadataRemoveResult"].(map[string]any)
	if schema["additionalProperties"] != false || len(schema["required"].([]string)) != 3 {
		t.Fatal("removal result schema not strict")
	}
	if Specification(config.Config{})["paths"].(map[string]any)["/api/v1/items/{id}/metadata/external"] != nil {
		t.Fatal("disabled accounts exposed removal")
	}
}

func TestMetadataRemoveExpectedRevisionParsing(t *testing.T) {
	for raw, want := range map[string]int64{"1": 1, "42": 42} {
		if got, ok := parseExpectedRevision(raw); !ok || got != want {
			t.Fatal("valid revision rejected", raw)
		}
	}
	for _, raw := range []string{"", "0", "01", "+1", "-1", "1.0", " 1", "1e3", "9223372036854775808", "99999999999999999999"} {
		if _, ok := parseExpectedRevision(raw); ok {
			t.Fatal("invalid revision accepted", raw)
		}
	}
	if got, ok := parseExpectedRevision("2147483646"); !ok || got != domain.ItemMetadataRevisionMax-1 {
		t.Fatal("largest revision rejected")
	}
	if _, ok := parseExpectedRevision("2147483647"); ok {
		t.Fatal("revision at maximum accepted")
	}
}
