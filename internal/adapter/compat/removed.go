// Package compat isolates requests for removed legacy protocol surfaces.
package compat

import "strings"

// RemovedFeaturePath identifies only the retired root controller families.
// It receives the decoded URL path, never the query or a raw request target.
// It does not create a listener, service, job or media operation.
func RemovedFeaturePath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	root, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch strings.ToLower(root) {
	case "livetv", "channels", "dlna":
		return true
	default:
		return false
	}
}
