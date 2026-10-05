package main

import "testing"

func TestExceptionsAllowFilesAndBoundaryDirectories(t *testing.T) {
	e := parseExceptions("LICENSE # legal\ninternal/adapter/compat/ # protocol\n\n# comment only\n")
	for file, want := range map[string]bool{
		"LICENSE":                             true,
		"internal/adapter/compat/system.go":   true,
		"internal/adapter/compat":             false,
		"internal/adapter/compatibility/x.go": false,
		"docs/LICENSE":                        false,
		"internal/adapter/http/server.go":     false,
	} {
		if got := e.allows(file); got != want {
			t.Errorf("allows(%q)=%v want %v", file, got, want)
		}
	}
}

func TestGateCoversServiceAndProductIdentityOnly(t *testing.T) {
	for file, want := range map[string]bool{
		"cmd/jelee/main.go":           true,
		"internal/domain/item.go":     true,
		"web/index.html":              true,
		"deploy/docker-compose.yml":   true,
		"README.md":                   true,
		"Dockerfile":                  true,
		".env.example":                true,
		"go.mod":                      true,
		"docs/branding-rename-map.md": false,
		"scripts/make.ps1":            false,
		"tools/openapi/main.go":       false,
		".github/workflows/jelee.yml": false,
		"CONTRIBUTORS.md":             false,
	} {
		if got := newService(file); got != want {
			t.Errorf("newService(%q)=%v want %v", file, got, want)
		}
	}
}
