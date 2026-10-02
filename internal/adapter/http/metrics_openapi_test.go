package httpapi

import "testing"

func TestMetricsSpecificationUsesRolloutAndExposition(t *testing.T) {
	cfg := metricsConfig()
	paths := Specification(cfg)["paths"].(map[string]any)
	item := paths["/metrics"].(map[string]any)
	op := item["get"].(map[string]any)
	if len(item) != 1 || op["x-jelee-role"] != "administrator" || op["security"] == nil {
		t.Fatal("metrics contract lost method or administrator requirement")
	}
	content := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if len(content) != 1 || content["text/plain"] == nil {
		t.Fatal("metrics contract must expose Prometheus text, without a JSON envelope")
	}
	cfg.EnableMetrics = false
	if Specification(cfg)["paths"].(map[string]any)["/metrics"] != nil {
		t.Fatal("disabled metrics advertised")
	}
	cfg.EnableMetrics, cfg.EnableAccounts = true, false
	if Specification(cfg)["paths"].(map[string]any)["/metrics"] != nil {
		t.Fatal("metrics advertised without accounts")
	}
}
