//go:build tools

// Package tools documents the tool dependency boundary. The Go distribution
// includes gofmt, vet and coverage; downloaded tools are pinned in manifest.json.
// Add versioned imports here when a separate Go-based tool is introduced.
package tools
