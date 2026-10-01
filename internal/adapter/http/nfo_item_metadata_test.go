package httpapi

import (
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func TestNFOItemSchemaIndependentOfJobsAndTMDB(t *testing.T) {
	cfg := config.Config{EnableAccounts: true}
	spec := Specification(cfg)
	paths := spec["paths"].(map[string]any)
	if paths["/api/v1/items/{id}/metadata/nfo"] == nil || paths["/api/v1/items/{id}/metadata/tmdb"] != nil {
		t.Fatal("local NFO schema depends on TMDB")
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["MetadataApplyResult"] == nil || schemas["NFOItemOrigin"] == nil {
		t.Fatal("NFO response schema missing")
	}
	if schemas["NFOItemOrigin"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("NFO origin schema not strict")
	}
	cfg.EnableAccounts = false
	if Specification(cfg)["paths"].(map[string]any)["/api/v1/items/{id}/metadata/nfo"] != nil {
		t.Fatal("disabled accounts exposed NFO apply")
	}
}
